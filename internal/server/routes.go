package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/maxhummler-oss/remotly/internal/protocol"
)

//go:embed web
var webFS embed.FS

//go:embed install/*.tmpl
var installFS embed.FS

var installTemplates = template.Must(template.ParseFS(installFS, "install/*.tmpl"))

var agentFileRE = regexp.MustCompile(`^remotly-agent-(windows|darwin|linux)-(amd64|arm64)(\.exe)?$`)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	web, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", noCache(http.FileServer(http.FS(web))))
	mux.HandleFunc("GET /ca.crt", s.handleCACert)

	mux.HandleFunc("GET /ws/viewer", s.handleViewerWS)
	mux.HandleFunc("GET /ws/agent", s.handleAgentWS)

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/password", s.user(s.handleChangePassword))
	mux.HandleFunc("POST /api/totp/setup", s.userSetup(s.handleTOTPSetup))
	mux.HandleFunc("POST /api/totp/confirm", s.userSetup(s.handleTOTPConfirm))
	mux.HandleFunc("POST /api/totp/disable", s.user(s.handleTOTPDisable))
	mux.HandleFunc("POST /api/devices/{id}/wake", s.user(s.handleWake))

	mux.HandleFunc("GET /api/admin/overview", s.admin(s.handleOverview))
	mux.HandleFunc("GET /api/admin/audit", s.admin(s.handleAudit))
	mux.HandleFunc("POST /api/admin/users", s.admin(s.handleAddUser))
	mux.HandleFunc("PATCH /api/admin/users/{name}", s.admin(s.handlePatchUser))
	mux.HandleFunc("DELETE /api/admin/users/{name}", s.admin(s.handleDeleteUser))
	mux.HandleFunc("PATCH /api/admin/devices/{id}", s.admin(s.handlePatchDevice))
	mux.HandleFunc("DELETE /api/admin/devices/{id}", s.admin(s.handleDeleteDevice))
	mux.HandleFunc("DELETE /api/admin/sessions/{id}", s.admin(s.handleKick))
	mux.HandleFunc("POST /api/admin/enroll-key", s.admin(s.handleRotateEnrollKey))

	mux.HandleFunc("POST /api/agent/enroll", s.handleEnroll)
	mux.HandleFunc("GET /download/{file}", s.handleDownload)
	mux.HandleFunc("GET /install/agent.sh", s.handleInstall("agent.sh.tmpl", "text/x-shellscript"))
	mux.HandleFunc("GET /install/agent.ps1", s.handleInstall("agent.ps1.tmpl", "text/plain"))
	return mux
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// helpers

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{msg})
}

func readJSON(r *http.Request, v any) error {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		return errors.New("expected application/json")
	}
	return json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v)
}

// csrfOK: state-changing browser requests must come from our own origin.
func csrfOK(r *http.Request) bool {
	return r.Method == http.MethodGet || sameOrigin(r)
}

// user requires a logged-in user who satisfies the 2FA policy.
func (s *Server) user(h func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
	return s.userSetup(func(w http.ResponseWriter, r *http.Request, u User) {
		if s.mustSetup2FA(u) {
			fail(w, http.StatusForbidden, "Bitte zuerst die Zwei-Faktor-Anmeldung einrichten")
			return
		}
		h(w, r, u)
	})
}

// userSetup requires a logged-in user, but allows users who still have to
// set up 2FA (for the setup endpoints themselves).
func (s *Server) userSetup(h func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !csrfOK(r) {
			fail(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		u, _, ok := s.currentUser(r)
		if !ok {
			fail(w, http.StatusUnauthorized, "nicht angemeldet")
			return
		}
		h(w, r, u)
	}
}

func (s *Server) mustSetup2FA(u User) bool {
	return s.cfg.Require2FA && u.TOTPSecret == ""
}

func (s *Server) admin(h func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
	return s.user(func(w http.ResponseWriter, r *http.Request, u User) {
		if !u.Admin {
			fail(w, http.StatusForbidden, "nur für Administratoren")
			return
		}
		h(w, r, u)
	})
}

// ---------------------------------------------------------------------------
// login

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	ip := remoteIP(r)
	name := strings.ToLower(strings.TrimSpace(req.Username))
	keys := []string{"ip:" + ip, "user:" + name}
	if wait := s.auth.locked(keys...); wait > 0 {
		fail(w, http.StatusTooManyRequests, "Zu viele Fehlversuche. Bitte in "+wait.Round(time.Second).String()+" erneut versuchen.")
		return
	}
	u, ok := s.store.Authenticate(name, req.Password)
	if !ok {
		s.auth.fail(keys...)
		s.audit.Log(AuditEntry{Event: "login.failed", User: name, IP: ip})
		fail(w, http.StatusUnauthorized, "Benutzername oder Passwort falsch")
		return
	}
	if u.TOTPSecret != "" {
		if req.Code == "" {
			// Password is right; ask for the second factor.
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Bestätigungscode erforderlich", "needsCode": true})
			return
		}
		if !s.totp.check(u.Name, u.TOTPSecret, req.Code) {
			s.auth.fail(keys...)
			s.audit.Log(AuditEntry{Event: "login.failed", User: name, IP: ip, Detail: "2FA code"})
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Bestätigungscode ungültig", "needsCode": true})
			return
		}
	}
	s.auth.succeed(keys...)
	ws := s.auth.create(u.Name, ip)
	s.setSessionCookie(w, ws)
	s.audit.Log(AuditEntry{Event: "login", User: u.Name, IP: ip})
	writeJSON(w, http.StatusOK, s.meResponse(u))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.remove(c.Value)
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) meResponse(u User) map[string]any {
	return map[string]any{
		"name":         u.Name,
		"displayName":  u.DisplayName,
		"admin":        u.Admin,
		"serverName":   s.cfg.Name,
		"version":      s.version,
		"totp":         u.TOTPSecret != "",
		"mustSetup2FA": s.mustSetup2FA(u),
		"require2FA":   s.cfg.Require2FA,
	}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"serverName": s.cfg.Name, "needsSetup": s.store.UserCount() == 0})
		return
	}
	writeJSON(w, http.StatusOK, s.meResponse(u))
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, u User) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := s.store.Authenticate(u.Name, req.Old); !ok {
		fail(w, http.StatusForbidden, "aktuelles Passwort falsch")
		return
	}
	if err := s.store.UpdateUser(u.Name, UserPatch{Password: &req.New}); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit.Log(AuditEntry{Event: "user.password_changed", User: u.Name, IP: remoteIP(r)})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleWake(w http.ResponseWriter, r *http.Request, u User) {
	id := r.PathValue("id")
	if !u.CanAccess(id) {
		fail(w, http.StatusForbidden, "Keine Berechtigung")
		return
	}
	d, ok := s.store.GetDevice(id)
	if !ok {
		fail(w, http.StatusNotFound, "Gerät nicht gefunden")
		return
	}
	if err := wake(d.MACs, d.LastIP); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit.Log(AuditEntry{Event: "device.wake", User: u.Name, Device: d.Name, IP: remoteIP(r)})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// admin

type adminDevice struct {
	DeviceRecord
	SecretHash string              `json:"secretHash,omitempty"` // never sent
	Online     bool                `json:"online"`
	Info       *protocol.AgentInfo `json:"info,omitempty"`
}

type adminSession struct {
	ID       string    `json:"id"`
	User     string    `json:"user"`
	Device   string    `json:"device"`
	DeviceID string    `json:"deviceId"`
	IP       string    `json:"ip"`
	Started  time.Time `json:"started"`
}

type adminUser struct {
	User
	PasswordHash string `json:"passwordHash,omitempty"` // never sent
	TOTPSecret   string `json:"totpSecret,omitempty"`   // never sent
	TOTP         bool   `json:"totp"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request, _ User) {
	records := s.store.Devices()
	users := s.store.Users()

	s.mu.Lock()
	devices := make([]adminDevice, 0, len(records))
	names := map[string]string{}
	for _, d := range records {
		names[d.ID] = d.Name
		ad := adminDevice{DeviceRecord: d}
		if a := s.agents[d.ID]; a != nil {
			info := a.info
			ad.Online, ad.Info = true, &info
		}
		devices = append(devices, ad)
	}
	sessions := []adminSession{}
	for _, sess := range s.sessions {
		sessions = append(sessions, adminSession{
			ID: sess.id, User: sess.viewer.user.DisplayName, DeviceID: sess.agent.deviceID,
			Device: names[sess.agent.deviceID], IP: sess.viewer.addr, Started: sess.started,
		})
	}
	caFP := ""
	if s.pki != nil {
		caFP = s.pki.caFP
	}
	s.mu.Unlock()
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Started.Before(sessions[j].Started) })

	us := make([]adminUser, len(users))
	for i, u := range users {
		us[i] = adminUser{User: u, TOTP: u.TOTPSecret != ""}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users":         us,
		"devices":       devices,
		"sessions":      sessions,
		"enrollKey":     s.store.EnrollKey(),
		"caFingerprint": caFP,
		"agents":        s.agentBinaries(),
		"tls":           s.cfg.TLS,
		"require2FA":    s.cfg.Require2FA,
	})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, _ User) {
	writeJSON(w, http.StatusOK, s.audit.Tail(300))
}

func (s *Server) handleAddUser(w http.ResponseWriter, r *http.Request, admin User) {
	var req struct {
		Name        string   `json:"name"`
		DisplayName string   `json:"displayName"`
		Password    string   `json:"password"`
		Admin       bool     `json:"admin"`
		Devices     []string `json:"devices"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.AddUser(req.Name, req.DisplayName, req.Password, req.Admin); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if len(req.Devices) > 0 {
		s.store.UpdateUser(name, UserPatch{Devices: &req.Devices})
	}
	s.audit.Log(AuditEntry{Event: "user.created", User: admin.Name, IP: remoteIP(r), Detail: name})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request, admin User) {
	name := r.PathValue("name")
	var p UserPatch
	if err := readJSON(r, &p); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpdateUser(name, p); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var what []string
	if p.Password != nil {
		what = append(what, "password")
	}
	if p.Admin != nil {
		what = append(what, "admin")
	}
	if p.Disabled != nil {
		what = append(what, "disabled")
	}
	if p.Devices != nil {
		what = append(what, "devices")
	}
	if p.ResetTOTP {
		what = append(what, "2fa-reset")
	}
	if p.DisplayName != nil {
		what = append(what, "name")
	}
	// Credentials or rights changed: force a fresh login everywhere.
	if p.Password != nil || (p.Disabled != nil && *p.Disabled) || p.Admin != nil || p.ResetTOTP {
		s.auth.revokeUser(name)
		s.disconnectUser(name)
	} else {
		s.broadcastDevices()
	}
	s.audit.Log(AuditEntry{Event: "user.updated", User: admin.Name, IP: remoteIP(r), Detail: name + ": " + strings.Join(what, ",")})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, admin User) {
	name := r.PathValue("name")
	if err := s.store.DeleteUser(name); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.auth.revokeUser(name)
	s.disconnectUser(name)
	s.audit.Log(AuditEntry{Event: "user.deleted", User: admin.Name, IP: remoteIP(r), Detail: name})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePatchDevice(w http.ResponseWriter, r *http.Request, admin User) {
	id := r.PathValue("id")
	var req struct {
		Name    string `json:"name"`
		Approve bool   `json:"approve"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name != "" {
		if err := s.store.RenameDevice(id, req.Name); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		s.audit.Log(AuditEntry{Event: "device.renamed", User: admin.Name, Device: req.Name, IP: remoteIP(r), Detail: id})
	}
	if req.Approve {
		if err := s.store.ApproveDevice(id); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		d, _ := s.store.GetDevice(id)
		s.audit.Log(AuditEntry{Event: "device.approved", User: admin.Name, Device: d.Name, IP: remoteIP(r), Detail: id})
	}
	s.broadcastDevices()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request, admin User) {
	id := r.PathValue("id")
	d, _ := s.store.GetDevice(id)
	if err := s.store.DeleteDevice(id); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.disconnectDevice(id)
	s.audit.Log(AuditEntry{Event: "device.revoked", User: admin.Name, Device: d.Name, IP: remoteIP(r), Detail: id})
	s.broadcastDevices()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleKick(w http.ResponseWriter, r *http.Request, admin User) {
	if !s.kickSession(r.PathValue("id"), admin.DisplayName) {
		fail(w, http.StatusNotFound, "Sitzung nicht gefunden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRotateEnrollKey(w http.ResponseWriter, r *http.Request, admin User) {
	key, err := s.store.RotateEnrollKey()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit.Log(AuditEntry{Event: "enroll_key.rotated", User: admin.Name, IP: remoteIP(r)})
	writeJSON(w, http.StatusOK, map[string]string{"enrollKey": key})
}

// ---------------------------------------------------------------------------
// agents: enrollment, downloads, installers

func (s *Server) caFingerprint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pki == nil {
		return ""
	}
	return s.pki.caFP
}

func (s *Server) handleCACert(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	p := s.pki
	s.mu.Unlock()
	if p == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="remotly-ca.crt"`)
	w.Write(p.caCertPEM)
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	if wait := s.auth.locked("ip:" + ip); wait > 0 {
		fail(w, http.StatusTooManyRequests, "too many failed attempts")
		return
	}
	var req struct {
		Key      string `json:"key"`
		Name     string `json:"name"`
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	id, secret, err := s.store.Enroll(req.Key, req.Name, req.Hostname, req.OS, req.Arch, ip, s.cfg.ApproveDevices)
	if err != nil {
		s.auth.fail("ip:" + ip)
		s.audit.Log(AuditEntry{Event: "agent.enroll_failed", IP: ip, Detail: req.Hostname})
		fail(w, http.StatusForbidden, "invalid enrollment key")
		return
	}
	s.auth.succeed("ip:" + ip)
	s.audit.Log(AuditEntry{Event: "agent.enrolled", Device: req.Name, IP: ip, Detail: id + " " + req.Hostname + " " + req.OS})
	s.broadcastDevices()
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "secret": secret})
}

type agentBinary struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	File string `json:"file"`
	Size int64  `json:"size"`
}

func (s *Server) agentBinaries() []agentBinary {
	entries, _ := os.ReadDir(s.cfg.AgentsDir)
	list := []agentBinary{}
	for _, e := range entries {
		m := agentFileRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, agentBinary{OS: m[1], Arch: m[2], File: e.Name(), Size: info.Size()})
	}
	return list
}

var (
	hashMu    sync.Mutex
	hashCache = map[string]struct {
		mod  time.Time
		hash string
	}{}
)

// agentHashes returns the SHA-256 of every agent binary, so install scripts
// can verify their download.
func (s *Server) agentHashes() map[string]string {
	out := map[string]string{}
	hashMu.Lock()
	defer hashMu.Unlock()
	for _, b := range s.agentBinaries() {
		path := filepath.Join(s.cfg.AgentsDir, b.File)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if c, ok := hashCache[path]; ok && c.mod.Equal(info.ModTime()) {
			out[b.File] = c.hash
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		h := sha256.New()
		io.Copy(h, f)
		f.Close()
		sum := hex.EncodeToString(h.Sum(nil))
		hashCache[path] = struct {
			mod  time.Time
			hash string
		}{info.ModTime(), sum}
		out[b.File] = sum
	}
	return out
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !agentFileRE.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cfg.AgentsDir, name)
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "agent binary "+name+" is not available on this server", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}

// handleInstall serves an agent install script pre-configured with this
// server's URL, the enrollment key and the CA fingerprint to pin. The key
// must be supplied as ?key=, so the script cannot be used to discover it.
func (s *Server) handleInstall(tmpl, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r)
		if wait := s.auth.locked("ip:" + ip); wait > 0 {
			http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
			return
		}
		key := r.URL.Query().Get("key")
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.store.EnrollKey())) != 1 {
			s.auth.fail("ip:" + ip)
			http.Error(w, "invalid or missing ?key=", http.StatusForbidden)
			return
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		w.Header().Set("Content-Type", contentType+"; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		installTemplates.ExecuteTemplate(w, tmpl, map[string]any{
			"URL":           scheme + "://" + r.Host,
			"Key":           key,
			"CAFingerprint": s.caFingerprint(),
			"Hashes":        s.agentHashes(),
		})
	}
}
