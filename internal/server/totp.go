package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"rsc.io/qr"
)

// TOTP (RFC 6238): 6 digits, 30 s steps, SHA-1 – the parameters every
// authenticator app (Microsoft/Google Authenticator, Apple Passwords,
// 1Password, …) supports.

const totpStep = 30

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b32.EncodeToString(b)
}

func totpCode(key []byte, counter int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// verifyTOTP checks code against the current step ±1 (clock drift) and
// returns the matching step.
func verifyTOTP(secret, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	key, err := b32.DecodeString(strings.ToUpper(secret))
	if err != nil {
		return 0, false
	}
	step := now.Unix() / totpStep
	for _, s := range []int64{step, step - 1, step + 1} {
		if subtle.ConstantTimeCompare([]byte(totpCode(key, s)), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

// totpGuard prevents reuse of a code (replay) and keeps pending setups.
type totpGuard struct {
	mu      sync.Mutex
	used    map[string]int64  // user -> last accepted step
	pending map[string]string // web session id -> secret being set up
}

func newTOTPGuard() *totpGuard {
	return &totpGuard{used: map[string]int64{}, pending: map[string]string{}}
}

// check verifies a code once: the same (or an older) step is rejected.
func (g *totpGuard) check(user, secret, code string) bool {
	step, ok := verifyTOTP(secret, code, time.Now())
	if !ok {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if step <= g.used[user] {
		return false
	}
	g.used[user] = step
	return true
}

func totpURI(issuer, user, secret string) string {
	label := url.PathEscape(issuer + ":" + user)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func qrDataURI(text string) string {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	c.Scale = 6
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(c.PNG())
}

// ---------------------------------------------------------------------------
// handlers

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request, u User) {
	_, ws, _ := s.currentUser(r)
	secret := newTOTPSecret()
	s.totp.mu.Lock()
	s.totp.pending[ws.id] = secret
	s.totp.mu.Unlock()
	uri := totpURI(s.cfg.Name, u.Name, secret)
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": strings.Join(chunk(secret, 4), " "),
		"uri":    uri,
		"qr":     qrDataURI(uri),
	})
}

func chunk(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request, u User) {
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	_, ws, _ := s.currentUser(r)
	s.totp.mu.Lock()
	secret := s.totp.pending[ws.id]
	s.totp.mu.Unlock()
	if secret == "" {
		fail(w, http.StatusBadRequest, "Einrichtung abgelaufen – bitte neu starten")
		return
	}
	if !s.totp.check(u.Name, secret, req.Code) {
		fail(w, http.StatusBadRequest, "Code ungültig – Uhrzeit des Telefons prüfen")
		return
	}
	if err := s.store.SetTOTP(u.Name, secret); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.totp.mu.Lock()
	delete(s.totp.pending, ws.id)
	s.totp.mu.Unlock()
	s.audit.Log(AuditEntry{Event: "user.2fa_enabled", User: u.Name, IP: remoteIP(r)})
	u.TOTPSecret = secret
	writeJSON(w, http.StatusOK, s.meResponse(u))
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request, u User) {
	if s.cfg.Require2FA {
		fail(w, http.StatusForbidden, "Zwei-Faktor-Anmeldung ist auf diesem Server Pflicht")
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := s.store.Authenticate(u.Name, req.Password); !ok || !s.totp.check(u.Name, u.TOTPSecret, req.Code) {
		fail(w, http.StatusForbidden, "Passwort oder Code falsch")
		return
	}
	s.store.SetTOTP(u.Name, "")
	s.audit.Log(AuditEntry{Event: "user.2fa_disabled", User: u.Name, IP: remoteIP(r)})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
