package server

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Event  string    `json:"event"`
	User   string    `json:"user,omitempty"`
	Device string    `json:"device,omitempty"`
	IP     string    `json:"ip,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Audit appends security-relevant events as JSON lines.
type Audit struct {
	mu   sync.Mutex
	path string
}

// NewAudit opens (creates) the audit log.
func NewAudit(path string) *Audit { return &Audit{path: path} }

// Log records an event.
func (a *Audit) Log(e AuditEntry) {
	e.Time = time.Now().UTC()
	line, _ := json.Marshal(e)
	log.Printf("audit: %s", line)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rotate()
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("audit log: %v", err)
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
	matchDirOwner(a.path)
}

const (
	auditMaxSize = 20 << 20 // rotate at 20 MB
	auditKeep    = 5        // audit.log.1 … audit.log.5
)

// rotate keeps the log bounded: audit.log -> .1 -> … -> .5. Caller holds mu.
func (a *Audit) rotate() {
	info, err := os.Stat(a.path)
	if err != nil || info.Size() < auditMaxSize {
		return
	}
	os.Remove(fmt.Sprintf("%s.%d", a.path, auditKeep))
	for i := auditKeep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", a.path, i), fmt.Sprintf("%s.%d", a.path, i+1))
	}
	os.Rename(a.path, a.path+".1")
}

// All returns every entry of the current log file, oldest first.
func (a *Audit) All() []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.Open(a.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e AuditEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// WriteCSV exports entries; cells that a spreadsheet would treat as a
// formula are prefixed with an apostrophe (CSV injection).
func WriteCSV(w io.Writer, entries []AuditEntry) error {
	cw := csv.NewWriter(w)
	cw.Comma = ';' // what German Excel expects
	safe := func(v string) string {
		if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
			return "'" + v
		}
		return v
	}
	cw.Write([]string{"Zeit (UTC)", "Ereignis", "Benutzer", "Gerät", "IP", "Details"})
	for _, e := range entries {
		cw.Write([]string{e.Time.Format(time.RFC3339), safe(e.Event), safe(e.User), safe(e.Device), safe(e.IP), safe(e.Detail)})
	}
	cw.Flush()
	return cw.Error()
}

// Tail returns up to n most recent entries, newest first.
func (a *Audit) Tail(n int) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.Open(a.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	ring := make([]AuditEntry, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e AuditEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, e)
	}
	for i, j := 0, len(ring)-1; i < j; i, j = i+1, j-1 {
		ring[i], ring[j] = ring[j], ring[i]
	}
	return ring
}
