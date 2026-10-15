package server

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "remotly_sid"

type webSession struct {
	id       string
	user     string
	ip       string
	created  time.Time
	expires  time.Time
	lastSeen time.Time
	open     int // connected viewer pages (WebSockets)
}

type failure struct {
	count int
	until time.Time
	last  time.Time
}

// auth manages browser login sessions and brute-force protection.
type auth struct {
	mu       sync.Mutex
	sessions map[string]*webSession
	ttl      time.Duration
	idle     time.Duration
	failures map[string]*failure // keyed by "ip:<addr>" and "user:<name>"
}

func newAuth(ttl, idle time.Duration) *auth {
	a := &auth{sessions: map[string]*webSession{}, ttl: ttl, idle: idle, failures: map[string]*failure{}}
	go a.gc()
	return a
}

func (a *auth) gc() {
	for range time.Tick(10 * time.Minute) {
		a.mu.Lock()
		now := time.Now()
		for id, s := range a.sessions {
			if a.expired(s, now) {
				delete(a.sessions, id)
			}
		}
		for k, f := range a.failures {
			if now.Sub(f.last) > time.Hour {
				delete(a.failures, k)
			}
		}
		a.mu.Unlock()
	}
}

// locked reports how long logins for these keys are blocked.
func (a *auth) locked(keys ...string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	var wait time.Duration
	for _, k := range keys {
		if f := a.failures[k]; f != nil {
			if d := time.Until(f.until); d > wait {
				wait = d
			}
		}
	}
	return wait
}

// fail records a failed attempt. After 5 failures the key is locked out
// with exponential backoff (30s, 1m, 2m, … up to 15m).
func (a *auth) fail(keys ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range keys {
		f := a.failures[k]
		if f == nil {
			f = &failure{}
			a.failures[k] = f
		}
		f.count++
		f.last = time.Now()
		if f.count >= 5 {
			d := 30 * time.Second << min(f.count-5, 5)
			f.until = time.Now().Add(min(d, 15*time.Minute))
		}
	}
}

func (a *auth) succeed(keys ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range keys {
		delete(a.failures, k)
	}
}

func (a *auth) create(user, ip string) *webSession {
	now := time.Now()
	s := &webSession{id: randomString(32), user: user, ip: ip, created: now, expires: now.Add(a.ttl), lastSeen: now}
	a.mu.Lock()
	a.sessions[s.id] = s
	a.mu.Unlock()
	return s
}

// expired: past the absolute lifetime, or idle (no open page and no
// request) for longer than the idle timeout. Caller holds mu.
func (a *auth) expired(s *webSession, now time.Time) bool {
	if now.After(s.expires) {
		return true
	}
	return a.idle > 0 && s.open == 0 && now.Sub(s.lastSeen) > a.idle
}

func (a *auth) get(id string) *webSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[id]
	now := time.Now()
	if s == nil || a.expired(s, now) {
		delete(a.sessions, id)
		return nil
	}
	s.lastSeen = now
	return s
}

// attach/detach count open viewer pages; an open page is never idle.
func (a *auth) attach(id string, delta int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[id]; s != nil {
		s.open += delta
		s.lastSeen = time.Now()
	}
}

func (a *auth) remove(id string) {
	a.mu.Lock()
	delete(a.sessions, id)
	a.mu.Unlock()
}

// revokeUser ends all login sessions of a user (password change, disable).
func (a *auth) revokeUser(user string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, s := range a.sessions {
		if s.user == user {
			delete(a.sessions, id)
		}
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, ws *webSession) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    ws.id,
		Path:     "/",
		Expires:  ws.expires,
		HttpOnly: true,
		Secure:   s.cfg.TLSEnabled(),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.TLSEnabled(), SameSite: http.SameSiteStrictMode,
	})
}

// currentUser resolves the logged-in user of a request.
func (s *Server) currentUser(r *http.Request) (User, *webSession, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return User{}, nil, false
	}
	ws := s.auth.get(c.Value)
	if ws == nil {
		return User{}, nil, false
	}
	u, ok := s.store.GetUser(ws.user)
	if !ok || u.Disabled {
		s.auth.remove(ws.id)
		return User{}, nil, false
	}
	return u, ws, true
}

// sameOrigin rejects cross-site requests: the Origin header (sent by all
// browsers on POST and WebSocket requests) must match the Host.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
