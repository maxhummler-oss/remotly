// Package server implements the Remotly hub: user accounts, device
// registry, WebRTC signaling, embedded TURN relay, web viewer and agent
// installers.
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/maxhummler-oss/remotly/internal/protocol"
	"github.com/pion/turn/v4"
)

const (
	writeWait    = 10 * time.Second
	pongWait     = 60 * time.Second
	pingInterval = 20 * time.Second
	helloWait    = 10 * time.Second
)

// Server is the Remotly hub.
type Server struct {
	cfg     Config
	version string
	store   *Store
	auth    *auth
	audit   *Audit
	pki     *pki

	mu       sync.Mutex
	agents   map[string]*client // by device ID
	viewers  map[*client]struct{}
	sessions map[string]*session

	turn     *turn.Server
	upgrader websocket.Upgrader
}

type client struct {
	srv  *Server
	conn *websocket.Conn
	send chan []byte
	addr string

	// viewer
	user    User
	loginID string
	// agent
	deviceID string
	info     protocol.AgentInfo

	closeOnce sync.Once
	done      chan struct{}
}

func (c *client) isAgent() bool { return c.deviceID != "" }

type session struct {
	id      string
	agent   *client
	viewer  *client
	started time.Time
}

// New creates a server. Call Run to start it.
func New(cfg Config, version string, store *Store) *Server {
	return &Server{
		cfg:      cfg,
		version:  version,
		store:    store,
		auth:     newAuth(time.Duration(cfg.SessionHours) * time.Hour),
		audit:    NewAudit(cfg.DataDir + "/audit.log"),
		agents:   map[string]*client{},
		viewers:  map[*client]struct{}{},
		sessions: map[string]*session{},
		upgrader: websocket.Upgrader{ReadBufferSize: 16 << 10, WriteBufferSize: 16 << 10},
	}
}

// Run starts TURN and the HTTP(S) server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	if s.store.UserCount() == 0 {
		log.Printf("no users yet: create the first admin with 'remotly-server user add <name> --admin'")
	}

	t, err := startTURN(s.cfg, s.store.TURNSecret())
	if err != nil {
		return err
	}
	s.turn = t
	defer func() {
		if s.turn != nil {
			s.turn.Close()
		}
	}()

	httpSrv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           securityHeaders(s.routes()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	var redirect *http.Server
	scheme := "http"
	switch s.cfg.TLS {
	case "auto":
		if err := s.reloadPKI(); err != nil {
			return fmt.Errorf("TLS: %w", err)
		}
		httpSrv.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				s.mu.Lock()
				defer s.mu.Unlock()
				return &s.pki.certificate, nil
			},
		}
		go s.renewLoop(ctx)
		scheme = "https"
	case "files":
		httpSrv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		scheme = "https"
	}
	if scheme == "https" && s.cfg.HTTPRedirect != "" {
		redirect = &http.Server{
			Addr:              s.cfg.HTTPRedirect,
			ReadHeaderTimeout: 10 * time.Second,
			Handler:           http.HandlerFunc(s.redirectHTTPS),
		}
		go func() {
			if err := redirect.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("HTTP redirect listener: %v", err)
			}
		}()
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
		if redirect != nil {
			redirect.Shutdown(shutdownCtx)
		}
	}()

	_, port, _ := net.SplitHostPort(s.cfg.Listen)
	log.Printf("Remotly server %s listening on %s://%s:%s", s.version, scheme, s.cfg.PublicIP, port)

	switch s.cfg.TLS {
	case "auto":
		err = httpSrv.ListenAndServeTLS("", "")
	case "files":
		err = httpSrv.ListenAndServeTLS(s.cfg.CertFile, s.cfg.KeyFile)
	default:
		log.Printf("WARNING: TLS is off – logins and signaling are unencrypted")
		err = httpSrv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) reloadPKI() error {
	p, err := loadOrCreatePKI(s.cfg.DataDir, []string{s.cfg.PublicIP}, s.cfg.Hostnames)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.pki = p
	s.mu.Unlock()
	return nil
}

// renewLoop re-issues the server certificate before it expires.
func (s *Server) renewLoop(ctx context.Context) {
	t := time.NewTicker(12 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.reloadPKI(); err != nil {
				log.Printf("certificate renewal: %v", err)
			}
		}
	}
}

func (s *Server) redirectHTTPS(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if _, port, _ := net.SplitHostPort(s.cfg.Listen); port != "" && port != "443" {
		host = net.JoinHostPort(host, port)
	}
	http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' wss: ws:; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'")
		hd.Set("Permissions-Policy", "clipboard-read=(self), clipboard-write=(self), fullscreen=(self), keyboard-map=(self)")
		if r.TLS != nil {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
		}
		h.ServeHTTP(w, r)
	})
}

// iceServers returns STUN/TURN servers with fresh short-lived credentials.
func (s *Server) iceServers(subject string) []protocol.ICEServer {
	if s.cfg.TURNPort == 0 {
		return nil
	}
	user, pass := turnCredentials(s.store.TURNSecret(), subject, 12*time.Hour)
	hostPort := net.JoinHostPort(s.cfg.PublicIP, strconv.Itoa(s.cfg.TURNPort))
	return []protocol.ICEServer{
		{URLs: []string{"stun:" + hostPort}},
		{URLs: []string{"turn:" + hostPort + "?transport=udp"}, Username: user, Credential: pass},
	}
}

// ---------------------------------------------------------------------------
// WebSocket connections

func (s *Server) newClient(w http.ResponseWriter, r *http.Request) *client {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil
	}
	return &client{srv: s, conn: conn, send: make(chan []byte, 256), addr: remoteIP(r), done: make(chan struct{})}
}

// handleViewerWS serves browsers. Authentication is the login cookie; the
// Origin check prevents other websites from using that cookie.
func (s *Server) handleViewerWS(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	u, ws, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	c := s.newClient(w, r)
	if c == nil {
		return
	}
	c.user, c.loginID = u, ws.id
	s.mu.Lock()
	s.viewers[c] = struct{}{}
	s.mu.Unlock()

	go c.writeLoop()
	c.post(protocol.Message{Type: protocol.TypeWelcome, ServerName: s.cfg.Name, User: u.DisplayName})
	c.post(protocol.Message{Type: protocol.TypeDevices, Devices: s.devicesFor(c.user)})
	c.readLoop()
}

// handleAgentWS serves agents, which authenticate with their device secret.
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	if wait := s.auth.locked("ip:" + ip); wait > 0 {
		http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
		return
	}
	c := s.newClient(w, r)
	if c == nil {
		return
	}
	c.conn.SetReadDeadline(time.Now().Add(helloWait))
	var hello protocol.Message
	if err := c.conn.ReadJSON(&hello); err != nil || hello.Type != protocol.TypeHello || hello.Agent == nil {
		c.conn.Close()
		return
	}
	dev, ok := s.store.AuthenticateDevice(hello.DeviceID, hello.Secret)
	if !ok {
		s.auth.fail("ip:" + ip)
		s.audit.Log(AuditEntry{Event: "agent.auth_failed", Device: hello.DeviceID, IP: ip})
		c.conn.SetWriteDeadline(time.Now().Add(writeWait))
		c.conn.WriteJSON(protocol.Message{Type: protocol.TypeError, Reason: "unauthorized"})
		c.conn.Close()
		return
	}
	s.auth.succeed("ip:" + ip)
	c.deviceID, c.info = dev.ID, *hello.Agent
	s.store.DeviceSeen(dev.ID, c.info.Hostname, c.info.OS, c.info.Arch, ip, c.info.MACs)

	s.mu.Lock()
	old := s.agents[dev.ID]
	s.agents[dev.ID] = c
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	log.Printf("agent online: %q (%s/%s, %s, %s) from %s", dev.Name, c.info.OS, c.info.Arch, c.info.Encoder, c.info.Capture, ip)

	go c.writeLoop()
	c.post(protocol.Message{Type: protocol.TypeWelcome, ServerName: s.cfg.Name})
	s.broadcastDevices()
	c.readLoop()
}

func (c *client) readLoop() {
	defer c.close()
	c.conn.SetReadLimit(1 << 20)
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		var msg protocol.Message
		if err := c.conn.ReadJSON(&msg); err != nil {
			return
		}
		if c.isAgent() {
			c.srv.handleAgent(c, msg)
		} else {
			c.srv.handleViewer(c, msg)
		}
	}
}

func (c *client) writeLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case data := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				c.close()
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.close()
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *client) post(msg protocol.Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	case <-c.done:
	default:
		log.Printf("%s: send buffer full, dropping connection", c.addr)
		go c.close()
	}
}

func (c *client) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.conn.Close()
		c.srv.unregister(c)
	})
}

func (s *Server) unregister(c *client) {
	s.mu.Lock()
	var ended []*session
	for id, sess := range s.sessions {
		if sess.agent == c || sess.viewer == c {
			ended = append(ended, sess)
			delete(s.sessions, id)
		}
	}
	wasAgent := false
	if c.isAgent() {
		if s.agents[c.deviceID] == c {
			delete(s.agents, c.deviceID)
			wasAgent = true
		}
	} else {
		delete(s.viewers, c)
	}
	s.mu.Unlock()

	for _, sess := range ended {
		peer := sess.viewer
		reason := "Verbindung zum Gerät verloren"
		if peer == c {
			peer, reason = sess.agent, "viewer disconnected"
		}
		peer.post(protocol.Message{Type: protocol.TypeEnd, Session: sess.id, Reason: reason})
		s.logSessionEnd(sess, reason)
	}
	if wasAgent {
		log.Printf("agent offline: %s", c.deviceID)
	}
	if wasAgent || len(ended) > 0 {
		s.broadcastDevices()
	}
}

// ---------------------------------------------------------------------------
// Device lists

func (s *Server) devicesFor(u User) []protocol.Device {
	records := s.store.Devices()
	s.mu.Lock()
	defer s.mu.Unlock()
	users := map[string][]string{}
	for _, sess := range s.sessions {
		name := sess.viewer.user.DisplayName
		if !slices.Contains(users[sess.agent.deviceID], name) {
			users[sess.agent.deviceID] = append(users[sess.agent.deviceID], name)
		}
	}
	list := []protocol.Device{}
	for _, d := range records {
		if !u.CanAccess(d.ID) {
			continue
		}
		dev := protocol.Device{ID: d.ID, Name: d.Name, OS: d.OS, InUseBy: users[d.ID]}
		if a := s.agents[d.ID]; a != nil {
			dev.Online = true
			dev.Encoder = a.info.Encoder
		} else {
			dev.CanWake = len(d.MACs) > 0
		}
		list = append(list, dev)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Online && !list[j].Online })
	return list
}

func (s *Server) broadcastDevices() {
	s.mu.Lock()
	viewers := make([]*client, 0, len(s.viewers))
	for v := range s.viewers {
		viewers = append(viewers, v)
	}
	s.mu.Unlock()
	for _, v := range viewers {
		// Permissions may have changed: always use the stored user.
		u, ok := s.store.GetUser(v.userName())
		if !ok || u.Disabled {
			continue
		}
		s.mu.Lock()
		v.user = u
		s.mu.Unlock()
		v.post(protocol.Message{Type: protocol.TypeDevices, Devices: s.devicesFor(u)})
	}
}

func (c *client) userName() string {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()
	return c.user.Name
}

// ---------------------------------------------------------------------------
// Message routing

func (s *Server) handleViewer(c *client, msg protocol.Message) {
	switch msg.Type {
	case protocol.TypeConnect:
		s.connect(c, msg)
	case protocol.TypeAnswer, protocol.TypeCandidate:
		if peer := s.peerOf(c, msg.Session); peer != nil {
			peer.post(protocol.Message{Type: msg.Type, Session: msg.Session, SDP: msg.SDP, Candidate: msg.Candidate})
		}
	case protocol.TypeEnd:
		s.endSession(c, msg.Session, msg.Reason)
	}
}

func (s *Server) handleAgent(c *client, msg protocol.Message) {
	switch msg.Type {
	case protocol.TypeOffer, protocol.TypeCandidate:
		if peer := s.peerOf(c, msg.Session); peer != nil {
			peer.post(protocol.Message{Type: msg.Type, Session: msg.Session, SDP: msg.SDP, Candidate: msg.Candidate})
		}
	case protocol.TypeEnd:
		s.endSession(c, msg.Session, msg.Reason)
	case protocol.TypeHello:
		// Agents re-send their info when it changes (e.g. desktop switch).
		if msg.Agent != nil {
			s.mu.Lock()
			c.info = *msg.Agent
			s.mu.Unlock()
		}
	}
}

func (s *Server) connect(c *client, msg protocol.Message) {
	// Re-read the user: permissions may have changed since login.
	u, ok := s.store.GetUser(c.user.Name)
	if !ok || u.Disabled {
		c.close()
		return
	}
	s.mu.Lock()
	c.user = u
	s.mu.Unlock()
	dev, _ := s.store.GetDevice(msg.Target)
	if !u.CanAccess(msg.Target) {
		s.audit.Log(AuditEntry{Event: "session.denied", User: u.Name, Device: dev.Name, IP: c.addr, Detail: "no permission " + msg.Target})
		c.post(protocol.Message{Type: protocol.TypeError, Reason: "Keine Berechtigung für dieses Gerät"})
		return
	}

	s.mu.Lock()
	agent := s.agents[msg.Target]
	if agent == nil {
		s.mu.Unlock()
		c.post(protocol.Message{Type: protocol.TypeError, Reason: "Gerät ist offline"})
		return
	}
	var displaced []*session
	var busyBy string
	for _, sess := range s.sessions {
		if sess.agent != agent {
			continue
		}
		switch {
		case sess.viewer.user.Name == u.Name:
			// Same user from another workstation: roam the session.
			displaced = append(displaced, sess)
		case msg.Takeover && u.Admin:
			displaced = append(displaced, sess)
		case u.Admin:
			// Admins may join alongside (support / shadowing).
		default:
			busyBy = sess.viewer.user.DisplayName
		}
	}
	if busyBy != "" {
		s.mu.Unlock()
		c.post(protocol.Message{Type: protocol.TypeError, Reason: "Gerät wird gerade von " + busyBy + " verwendet"})
		return
	}
	for _, sess := range displaced {
		delete(s.sessions, sess.id)
	}
	sess := &session{id: randomHex(8), agent: agent, viewer: c, started: time.Now()}
	s.sessions[sess.id] = sess
	s.mu.Unlock()

	for _, old := range displaced {
		reason := "Sitzung wurde an einem anderen Arbeitsplatz geöffnet"
		if old.viewer.user.Name != u.Name {
			reason = "Sitzung wurde von " + u.DisplayName + " übernommen"
		}
		old.viewer.post(protocol.Message{Type: protocol.TypeEnd, Session: old.id, Reason: reason})
		old.agent.post(protocol.Message{Type: protocol.TypeEnd, Session: old.id, Reason: reason})
		s.logSessionEnd(old, reason)
	}

	s.audit.Log(AuditEntry{Event: "session.start", User: u.Name, Device: dev.Name, IP: c.addr, Detail: sess.id})
	c.post(protocol.Message{
		Type:       protocol.TypeSessionStarted,
		Session:    sess.id,
		Target:     msg.Target,
		ICEServers: s.iceServers("u-" + u.Name),
	})
	agent.post(protocol.Message{
		Type:       protocol.TypeConnect,
		Session:    sess.id,
		ForceRelay: msg.ForceRelay,
		Codecs:     msg.Codecs,
		Viewer:     u.DisplayName,
		ICEServers: s.iceServers("d-" + agent.deviceID),
	})
	s.broadcastDevices()
}

func (s *Server) endSession(c *client, id, reason string) {
	s.mu.Lock()
	sess := s.sessions[id]
	if sess == nil || (sess.agent != c && sess.viewer != c) {
		s.mu.Unlock()
		return
	}
	delete(s.sessions, id)
	s.mu.Unlock()

	peer := sess.viewer
	if peer == c {
		peer = sess.agent
	}
	peer.post(protocol.Message{Type: protocol.TypeEnd, Session: id, Reason: reason})
	s.logSessionEnd(sess, reason)
	s.broadcastDevices()
}

func (s *Server) logSessionEnd(sess *session, reason string) {
	dev, _ := s.store.GetDevice(sess.agent.deviceID)
	s.audit.Log(AuditEntry{
		Event: "session.end", User: sess.viewer.user.Name, Device: dev.Name, IP: sess.viewer.addr,
		Detail: fmt.Sprintf("%s, %s, %s", sess.id, time.Since(sess.started).Round(time.Second), reason),
	})
}

// peerOf returns the other participant of a session c belongs to.
func (s *Server) peerOf(c *client, sessionID string) *client {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[sessionID]
	switch {
	case sess == nil:
		return nil
	case sess.agent == c:
		return sess.viewer
	case sess.viewer == c:
		return sess.agent
	}
	return nil
}

// kickSession ends a session (admin action).
func (s *Server) kickSession(id, by string) bool {
	s.mu.Lock()
	sess := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if sess == nil {
		return false
	}
	reason := "Sitzung von Administrator " + by + " beendet"
	sess.viewer.post(protocol.Message{Type: protocol.TypeEnd, Session: id, Reason: reason})
	sess.agent.post(protocol.Message{Type: protocol.TypeEnd, Session: id, Reason: reason})
	s.logSessionEnd(sess, reason)
	s.broadcastDevices()
	return true
}

// disconnectUser closes all viewer connections of a user.
func (s *Server) disconnectUser(name string) {
	s.mu.Lock()
	var list []*client
	for v := range s.viewers {
		if v.user.Name == name {
			list = append(list, v)
		}
	}
	s.mu.Unlock()
	for _, v := range list {
		v.close()
	}
}

// disconnectDevice closes a device's agent connection (device revoked).
func (s *Server) disconnectDevice(id string) {
	s.mu.Lock()
	a := s.agents[id]
	s.mu.Unlock()
	if a != nil {
		a.close()
	}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
