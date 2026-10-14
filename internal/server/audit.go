package server

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
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
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("audit log: %v", err)
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
	matchDirOwner(a.path)
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
