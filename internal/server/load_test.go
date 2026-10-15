package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/maxhummler-oss/remotly/internal/protocol"
)

// TestLoad simulates an office: many agents and viewers on one server.
// Run with: REMOTLY_LOAD=1 go test -run TestLoad -v ./internal/server
// Sizes: REMOTLY_AGENTS (default 500), REMOTLY_VIEWERS (default 200).
func TestLoad(t *testing.T) {
	if os.Getenv("REMOTLY_LOAD") == "" {
		t.Skip("set REMOTLY_LOAD=1 to run the load test")
	}
	nAgents := envInt("REMOTLY_AGENTS", 500)
	nViewers := envInt("REMOTLY_VIEWERS", 200)

	dir := t.TempDir()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	cfg := DefaultConfig()
	cfg.Listen, cfg.TLS, cfg.TURNPort, cfg.HTTPRedirect = addr, "off", 0, ""
	cfg.DataDir, cfg.AgentsDir, cfg.ApproveDevices = dir, dir, false
	cfg.Resolve()
	store, err := OpenStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.AddUser("admin", "Admin", "Load-Test-Passwort-2026", true)

	type cred struct{ id, secret string }
	creds := make([]cred, nAgents)
	for i := range creds {
		id, secret, err := store.Enroll(store.EnrollKey(), fmt.Sprintf("PC %03d", i), "pc", "windows", "amd64", "", false)
		if err != nil {
			t.Fatal(err)
		}
		creds[i] = cred{id, secret}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := New(cfg, "load", store)
	go srv.Run(ctx)
	waitListening(t, addr)

	var ms0 runtime.MemStats
	runtime.ReadMemStats(&ms0)

	// --- agents: answer every connect with an offer immediately
	start := time.Now()
	var agentsUp sync.WaitGroup
	agentConns := make([]*websocket.Conn, nAgents)
	for i := range creds {
		agentsUp.Add(1)
		go func(i int) {
			defer agentsUp.Done()
			c, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/agent", nil)
			if err != nil {
				t.Error(err)
				return
			}
			agentConns[i] = c
			c.WriteJSON(protocol.Message{Type: protocol.TypeHello, DeviceID: creds[i].id, Secret: creds[i].secret,
				Agent: &protocol.AgentInfo{Hostname: "pc", OS: "windows", Arch: "amd64", Encoder: "h264_nvenc"}})
			var w protocol.Message
			c.ReadJSON(&w)
			go func() {
				var mu sync.Mutex
				for {
					var m protocol.Message
					if c.ReadJSON(&m) != nil {
						return
					}
					if m.Type == protocol.TypeConnect {
						mu.Lock()
						c.WriteJSON(protocol.Message{Type: protocol.TypeOffer, Session: m.Session, SDP: "v=0"})
						mu.Unlock()
					}
				}
			}()
		}(i)
	}
	agentsUp.Wait()
	t.Logf("%d agents connected in %v", nAgents, time.Since(start).Round(time.Millisecond))

	// --- viewers: one login, many browser tabs
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	req, _ := http.NewRequest("POST", "http://"+addr+"/api/login", strings.NewReader(`{"username":"admin","password":"Load-Test-Passwort-2026"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+addr)
	resp, err := hc.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login: %v %v", err, resp)
	}
	u, _ := url.Parse("http://" + addr)
	header := http.Header{"Origin": {"http://" + addr}}
	for _, c := range jar.Cookies(u) {
		header.Add("Cookie", c.Name+"="+c.Value)
	}

	type viewer struct {
		c       *websocket.Conn
		devices atomic.Int64 // online devices in last list
		lists   atomic.Int64
		offers  chan string
		started chan string
		wmu     sync.Mutex
	}
	viewers := make([]*viewer, nViewers)
	start = time.Now()
	for i := range viewers {
		c, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/viewer", header)
		if err != nil {
			t.Fatal(err)
		}
		v := &viewer{c: c, offers: make(chan string, 16), started: make(chan string, 16)}
		viewers[i] = v
		go func() {
			for {
				var m protocol.Message
				if c.ReadJSON(&m) != nil {
					return
				}
				switch m.Type {
				case protocol.TypeDevices:
					n := 0
					for _, d := range m.Devices {
						if d.Online {
							n++
						}
					}
					v.devices.Store(int64(n))
					v.lists.Add(1)
				case protocol.TypeSessionStarted:
					v.started <- m.Session
				case protocol.TypeOffer:
					v.offers <- m.Session
				}
			}
		}()
	}
	t.Logf("%d viewers connected in %v", nViewers, time.Since(start).Round(time.Millisecond))
	waitFor(t, "initial lists", func() bool {
		for _, v := range viewers {
			if v.devices.Load() != int64(nAgents) {
				return false
			}
		}
		return true
	})

	// --- fan-out: one machine goes offline; how fast do all viewers know?
	before := make([]int64, nViewers)
	for i, v := range viewers {
		before[i] = v.lists.Load()
	}
	start = time.Now()
	agentConns[0].Close()
	waitFor(t, "fan-out", func() bool {
		for _, v := range viewers {
			if v.devices.Load() != int64(nAgents-1) {
				return false
			}
		}
		return true
	})
	t.Logf("device list update reached all %d viewers in %v (incl. 150 ms coalescing)", nViewers, time.Since(start).Round(time.Millisecond))

	// --- burst: 30 agents reconnect at once -> coalesced into few broadcasts
	for i := range before {
		before[i] = viewers[i].lists.Load()
	}
	for i := 1; i <= 30; i++ {
		agentConns[i].Close()
	}
	time.Sleep(time.Second)
	var extra int64
	for i, v := range viewers {
		extra += v.lists.Load() - before[i]
	}
	t.Logf("30 agents dropped at once -> %.1f list updates per viewer (coalesced)", float64(extra)/float64(nViewers))

	// --- signaling: every viewer opens a session to a different machine at once
	nSessions := min(nViewers, nAgents-31)
	lat := make([]time.Duration, nSessions)
	var wg sync.WaitGroup
	start = time.Now()
	for i := 0; i < nSessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := viewers[i]
			t0 := time.Now()
			v.wmu.Lock()
			v.c.WriteJSON(protocol.Message{Type: protocol.TypeConnect, Target: creds[31+i].id})
			v.wmu.Unlock()
			select {
			case <-v.offers:
				lat[i] = time.Since(t0)
			case <-time.After(10 * time.Second):
				t.Errorf("session %d: no offer", i)
			}
		}(i)
	}
	wg.Wait()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	t.Logf("%d simultaneous session setups: p50 %v, p95 %v, max %v (connect -> agent -> offer back)",
		nSessions, lat[nSessions/2].Round(time.Microsecond), lat[nSessions*95/100].Round(time.Microsecond), lat[nSessions-1].Round(time.Microsecond))

	var ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms1)
	t.Logf("server heap with %d agents, %d viewers, %d sessions: %.1f MB, goroutines: %d",
		nAgents, nViewers, nSessions, float64(ms1.HeapAlloc)/(1<<20), runtime.NumGoroutine())

	srv.mu.Lock()
	sessions := len(srv.sessions)
	srv.mu.Unlock()
	if sessions != nSessions {
		t.Errorf("server tracks %d sessions, want %d", sessions, nSessions)
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func waitListening(t *testing.T, addr string) {
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not start")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
