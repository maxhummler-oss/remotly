package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// User is a person allowed to log in to the web viewer.
type User struct {
	Name         string    `json:"name"` // login name, lowercase
	DisplayName  string    `json:"displayName"`
	PasswordHash string    `json:"passwordHash"`
	Admin        bool      `json:"admin"`
	Disabled     bool      `json:"disabled,omitempty"`
	Devices      []string  `json:"devices"` // device IDs this user may control
	Groups       []string  `json:"groups"`  // group IDs this user may control
	TOTPSecret   string    `json:"totpSecret,omitempty"`
	Created      time.Time `json:"created"`
	LastLogin    time.Time `json:"lastLogin,omitempty"`
}

// DeviceRecord is an enrolled agent.
type DeviceRecord struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	SecretHash string `json:"secretHash"` // sha256 of the agent secret
	// Pending devices are connected but unusable until an admin approves
	// them, so a leaked enrollment key cannot inject rogue machines.
	Pending  bool      `json:"pending,omitempty"`
	MACs     []string  `json:"macs,omitempty"`
	LastIP   string    `json:"lastIp,omitempty"`
	Enrolled time.Time `json:"enrolled"`
	LastSeen time.Time `json:"lastSeen,omitempty"`
}

// Group is a named set of devices (a department, a rack, a room). Users
// assigned to a group may control all of its devices.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Devices []string `json:"devices"`
}

type state struct {
	Users      map[string]*User         `json:"users"`
	Devices    map[string]*DeviceRecord `json:"devices"`
	Groups     map[string]*Group        `json:"groups"`
	EnrollKey  string                   `json:"enrollKey"`
	TURNSecret string                   `json:"turnSecret"`
}

// Store is the persistent server state (JSON file, written atomically).
// It reloads the file when it was modified externally, so the CLI can
// manage users while the server is running.
type Store struct {
	path      string
	mu        sync.Mutex
	st        state
	modTime   time.Time
	lastCheck time.Time
	flushing  bool // a deferred save is scheduled
}

// saveSoon persists non-critical changes (last seen, last login) at most
// once per second instead of on every change. Caller holds mu.
func (s *Store) saveSoon() {
	if s.flushing {
		return
	}
	s.flushing = true
	time.AfterFunc(time.Second, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.flushing = false
		s.save()
	})
}

var userNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// OpenStore loads or initializes the state file.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.load(); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		s.st = state{Users: map[string]*User{}, Devices: map[string]*DeviceRecord{}, Groups: map[string]*Group{}}
	}
	changed := false
	if s.st.EnrollKey == "" {
		s.st.EnrollKey = randomString(18)
		changed = true
	}
	if s.st.TURNSecret == "" {
		s.st.TURNSecret = randomString(32)
		changed = true
	}
	if changed {
		if err := s.save(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) load() error {
	info, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	if st.Users == nil {
		st.Users = map[string]*User{}
	}
	if st.Devices == nil {
		st.Devices = map[string]*DeviceRecord{}
	}
	if st.Groups == nil {
		st.Groups = map[string]*Group{}
	}
	s.st = st
	s.modTime = info.ModTime()
	return nil
}

// refresh reloads the file if another process changed it. Caller holds mu.
func (s *Store) refresh() {
	// At most one stat per second: reads happen for every device-list
	// broadcast, and with hundreds of viewers that adds up.
	if time.Since(s.lastCheck) < time.Second {
		return
	}
	s.lastCheck = time.Now()
	if info, err := os.Stat(s.path); err == nil && !info.ModTime().Equal(s.modTime) {
		s.load()
	}
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s.st, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	matchDirOwner(tmp)
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if info, err := os.Stat(s.path); err == nil {
		s.modTime = info.ModTime()
	}
	return nil
}

// update runs fn under the lock and persists the result.
func (s *Store) update(fn func(st *state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	if err := fn(&s.st); err != nil {
		return err
	}
	return s.save()
}

// view runs fn under the lock without saving.
func (s *Store) view(fn func(st *state)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	fn(&s.st)
}

// ---------------------------------------------------------------------------
// Users

// commonPasswords are rejected outright (lowercased, digits/symbols at the
// end stripped before comparison).
var commonPasswords = map[string]bool{
	"password": true, "passwort": true, "qwertz": true, "qwerty": true, "qwertzuiop": true,
	"asdfghjkl": true, "letmein": true, "welcome": true, "willkommen": true, "admin": true,
	"administrator": true, "sommer": true, "winter": true, "fruehling": true, "herbst": true,
	"hallo": true, "geheim": true, "remotly": true, "test": true, "changeme": true, "iloveyou": true,
}

// ValidatePassword enforces the password policy: at least 10 characters,
// not a common password, not derived from the user name.
func ValidatePassword(pw, user string) error {
	if len([]rune(pw)) < 10 {
		return errors.New("Passwort muss mindestens 10 Zeichen lang sein")
	}
	lower := strings.ToLower(pw)
	base := strings.TrimRightFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) })
	if commonPasswords[base] || strings.Trim(lower, "0123456789") == "" {
		return errors.New("Dieses Passwort ist zu leicht zu erraten")
	}
	if user != "" {
		for _, part := range append([]string{strings.ToLower(user)}, strings.FieldsFunc(strings.ToLower(user), func(r rune) bool { return r == '.' || r == '_' || r == '-' })...) {
			if len(part) >= 3 && strings.Contains(lower, part) {
				return errors.New("Passwort darf den Benutzernamen nicht enthalten")
			}
		}
	}
	distinct := map[rune]bool{}
	for _, r := range pw {
		distinct[r] = true
	}
	if len(distinct) < 5 {
		return errors.New("Passwort ist zu einfach (zu wenige verschiedene Zeichen)")
	}
	return nil
}

func hashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(h), err
}

// dummyHash is compared against when a user does not exist, so login
// timing does not reveal valid user names.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("remotly-dummy-password"), 12)

// Authenticate checks credentials and returns a copy of the user.
func (s *Store) Authenticate(name, password string) (User, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	var u User
	var found bool
	s.view(func(st *state) {
		if p := st.Users[name]; p != nil {
			u, found = *p, true
		}
	})
	if !found {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return User{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil || u.Disabled {
		return User{}, false
	}
	s.mu.Lock()
	if p := s.st.Users[name]; p != nil {
		p.LastLogin = time.Now()
		s.saveSoon()
	}
	s.mu.Unlock()
	return u, true
}

// GetUser returns a copy of the user.
func (s *Store) GetUser(name string) (User, bool) {
	var u User
	var ok bool
	s.view(func(st *state) {
		if p := st.Users[name]; p != nil {
			u, ok = *p, true
			u.Devices = append([]string{}, p.Devices...)
			u.Groups = append([]string{}, p.Groups...)
		}
	})
	return u, ok
}

// Users returns all users sorted by name.
func (s *Store) Users() []User {
	var list []User
	s.view(func(st *state) {
		for _, u := range st.Users {
			c := *u
			c.Devices = append([]string{}, u.Devices...)
			c.Groups = append([]string{}, u.Groups...)
			list = append(list, c)
		}
	})
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// UserCount returns the number of users.
func (s *Store) UserCount() int {
	n := 0
	s.view(func(st *state) { n = len(st.Users) })
	return n
}

// AddUser creates a user.
func (s *Store) AddUser(name, display, password string, admin bool) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if !userNameRE.MatchString(name) {
		return errors.New("ungültiger Benutzername (a-z, 0-9, . _ -)")
	}
	if err := ValidatePassword(password, name); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	if display == "" {
		display = name
	}
	return s.update(func(st *state) error {
		if st.Users[name] != nil {
			return errors.New("Benutzer existiert bereits")
		}
		st.Users[name] = &User{Name: name, DisplayName: display, PasswordHash: hash, Admin: admin, Devices: []string{}, Groups: []string{}, Created: time.Now()}
		return nil
	})
}

// UserPatch describes a partial user update.
type UserPatch struct {
	DisplayName *string   `json:"displayName"`
	Password    *string   `json:"password"`
	Admin       *bool     `json:"admin"`
	Disabled    *bool     `json:"disabled"`
	Devices     *[]string `json:"devices"`
	Groups      *[]string `json:"groups"`
	ResetTOTP   bool      `json:"resetTotp"`
}

// UpdateUser applies a patch.
func (s *Store) UpdateUser(name string, p UserPatch) error {
	var hash string
	if p.Password != nil {
		if err := ValidatePassword(*p.Password, name); err != nil {
			return err
		}
		var err error
		if hash, err = hashPassword(*p.Password); err != nil {
			return err
		}
	}
	return s.update(func(st *state) error {
		u := st.Users[name]
		if u == nil {
			return errors.New("Benutzer nicht gefunden")
		}
		if p.DisplayName != nil && strings.TrimSpace(*p.DisplayName) != "" {
			u.DisplayName = strings.TrimSpace(*p.DisplayName)
		}
		if hash != "" {
			u.PasswordHash = hash
		}
		if p.Admin != nil {
			if !*p.Admin && u.Admin && countAdmins(st) == 1 {
				return errors.New("der letzte Administrator kann nicht herabgestuft werden")
			}
			u.Admin = *p.Admin
		}
		if p.Disabled != nil {
			if *p.Disabled && u.Admin && countAdmins(st) == 1 {
				return errors.New("der letzte Administrator kann nicht deaktiviert werden")
			}
			u.Disabled = *p.Disabled
		}
		if p.ResetTOTP {
			u.TOTPSecret = ""
		}
		if p.Devices != nil {
			ids := []string{}
			for _, id := range *p.Devices {
				if st.Devices[id] != nil {
					ids = append(ids, id)
				}
			}
			u.Devices = ids
		}
		if p.Groups != nil {
			ids := []string{}
			for _, id := range *p.Groups {
				if st.Groups[id] != nil {
					ids = append(ids, id)
				}
			}
			u.Groups = ids
		}
		return nil
	})
}

func countAdmins(st *state) int {
	n := 0
	for _, u := range st.Users {
		if u.Admin && !u.Disabled {
			n++
		}
	}
	return n
}

// DeleteUser removes a user.
func (s *Store) DeleteUser(name string) error {
	return s.update(func(st *state) error {
		u := st.Users[name]
		if u == nil {
			return errors.New("Benutzer nicht gefunden")
		}
		if u.Admin && countAdmins(st) == 1 {
			return errors.New("der letzte Administrator kann nicht gelöscht werden")
		}
		delete(st.Users, name)
		return nil
	})
}

// ---------------------------------------------------------------------------
// Devices & enrollment

// EnrollKey returns the current agent enrollment key.
func (s *Store) EnrollKey() string {
	var k string
	s.view(func(st *state) { k = st.EnrollKey })
	return k
}

// RotateEnrollKey replaces the enrollment key. Already enrolled agents keep
// working; only new enrollments need the new key.
func (s *Store) RotateEnrollKey() (string, error) {
	k := randomString(18)
	return k, s.update(func(st *state) error { st.EnrollKey = k; return nil })
}

// TURNSecret returns the shared secret for TURN credentials.
func (s *Store) TURNSecret() string {
	var k string
	s.view(func(st *state) { k = st.TURNSecret })
	return k
}

// Enroll registers a new device if key is valid and returns its ID and
// secret. The secret is only stored hashed.
func (s *Store) Enroll(key, name, hostname, osName, arch, ip string, pending bool) (string, string, error) {
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.EnrollKey())) != 1 {
		return "", "", errBadEnrollKey
	}
	id := randomHex(8)
	secret := randomString(32)
	if name == "" {
		name = hostname
	}
	err := s.update(func(st *state) error {
		st.Devices[id] = &DeviceRecord{
			ID: id, Name: name, Hostname: hostname, OS: osName, Arch: arch,
			SecretHash: hashSecret(secret), LastIP: ip, Enrolled: time.Now(), Pending: pending,
		}
		return nil
	})
	return id, secret, err
}

var errBadEnrollKey = errors.New("invalid enrollment key")

// AuthenticateDevice verifies an agent's credentials.
func (s *Store) AuthenticateDevice(id, secret string) (DeviceRecord, bool) {
	var d DeviceRecord
	var ok bool
	s.view(func(st *state) {
		if p := st.Devices[id]; p != nil {
			d = *p
			ok = subtle.ConstantTimeCompare([]byte(p.SecretHash), []byte(hashSecret(secret))) == 1
		}
	})
	return d, ok
}

// DeviceSeen updates the information an agent reports on connect. The
// file is only rewritten when something changed (or LastSeen is stale), so
// hundreds of agents reconnecting after a server restart do not cause
// hundreds of full database writes.
func (s *Store) DeviceSeen(id, hostname, osName, arch, ip string, macs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	d := s.st.Devices[id]
	if d == nil {
		return
	}
	changed := d.Hostname != hostname || d.OS != osName || d.Arch != arch || d.LastIP != ip ||
		(len(macs) > 0 && strings.Join(macs, ",") != strings.Join(d.MACs, ","))
	d.Hostname, d.OS, d.Arch, d.LastIP = hostname, osName, arch, ip
	if len(macs) > 0 {
		d.MACs = macs
	}
	stale := time.Since(d.LastSeen) > time.Hour
	d.LastSeen = time.Now()
	if changed || stale {
		s.saveSoon()
	}
}

// GetDevice returns a copy of a device record.
func (s *Store) GetDevice(id string) (DeviceRecord, bool) {
	var d DeviceRecord
	var ok bool
	s.view(func(st *state) {
		if p := st.Devices[id]; p != nil {
			d, ok = *p, true
		}
	})
	return d, ok
}

// Devices returns all device records sorted by name.
func (s *Store) Devices() []DeviceRecord {
	var list []DeviceRecord
	s.view(func(st *state) {
		for _, d := range st.Devices {
			list = append(list, *d)
		}
	})
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return list
}

// SetTOTP stores (or clears) a user's TOTP secret.
func (s *Store) SetTOTP(name, secret string) error {
	return s.update(func(st *state) error {
		u := st.Users[name]
		if u == nil {
			return errors.New("Benutzer nicht gefunden")
		}
		u.TOTPSecret = secret
		return nil
	})
}

// ApproveDevice releases a pending device.
func (s *Store) ApproveDevice(id string) error {
	return s.update(func(st *state) error {
		d := st.Devices[id]
		if d == nil {
			return errors.New("Gerät nicht gefunden")
		}
		d.Pending = false
		return nil
	})
}

// RenameDevice sets a device's display name.
func (s *Store) RenameDevice(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return errors.New("ungültiger Name")
	}
	return s.update(func(st *state) error {
		d := st.Devices[id]
		if d == nil {
			return errors.New("Gerät nicht gefunden")
		}
		d.Name = name
		return nil
	})
}

// DeleteDevice revokes a device and removes it from all users.
func (s *Store) DeleteDevice(id string) error {
	return s.update(func(st *state) error {
		if st.Devices[id] == nil {
			return errors.New("Gerät nicht gefunden")
		}
		delete(st.Devices, id)
		for _, u := range st.Users {
			u.Devices = without(u.Devices, id)
		}
		for _, g := range st.Groups {
			g.Devices = without(g.Devices, id)
		}
		return nil
	})
}

func without(list []string, id string) []string {
	kept := []string{}
	for _, v := range list {
		if v != id {
			kept = append(kept, v)
		}
	}
	return kept
}

// Access returns the set of devices u may control (all=true for admins),
// resolving direct assignments and group memberships.
func (s *Store) Access(u User) (all bool, set map[string]bool) {
	if u.Admin {
		return true, nil
	}
	set = map[string]bool{}
	for _, d := range u.Devices {
		set[d] = true
	}
	s.view(func(st *state) {
		for _, gid := range u.Groups {
			if g := st.Groups[gid]; g != nil {
				for _, d := range g.Devices {
					set[d] = true
				}
			}
		}
	})
	return false, set
}

// CanAccess reports whether u may control the device.
func (s *Store) CanAccess(u User, deviceID string) bool {
	all, set := s.Access(u)
	return all || set[deviceID]
}

// ---------------------------------------------------------------------------
// Groups

// Groups returns all groups sorted by name.
func (s *Store) Groups() []Group {
	var list []Group
	s.view(func(st *state) {
		for _, g := range st.Groups {
			c := *g
			c.Devices = append([]string{}, g.Devices...)
			list = append(list, c)
		}
	})
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return list
}

// GroupNames maps device ID -> names of its groups.
func (s *Store) GroupNames() map[string][]string {
	out := map[string][]string{}
	for _, g := range s.Groups() {
		for _, d := range g.Devices {
			out[d] = append(out[d], g.Name)
		}
	}
	return out
}

// SaveGroup creates (empty id) or updates a group.
func (s *Store) SaveGroup(id, name string, devices []string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", errors.New("ungültiger Gruppenname")
	}
	err := s.update(func(st *state) error {
		for _, g := range st.Groups {
			if g.ID != id && strings.EqualFold(g.Name, name) {
				return errors.New("Gruppe existiert bereits")
			}
		}
		if id == "" {
			id = randomHex(6)
			st.Groups[id] = &Group{ID: id}
		}
		g := st.Groups[id]
		if g == nil {
			return errors.New("Gruppe nicht gefunden")
		}
		g.Name = name
		if devices != nil {
			ids := []string{}
			for _, d := range devices {
				if st.Devices[d] != nil {
					ids = append(ids, d)
				}
			}
			g.Devices = ids
		}
		return nil
	})
	return id, err
}

// DeleteGroup removes a group and its user assignments.
func (s *Store) DeleteGroup(id string) error {
	return s.update(func(st *state) error {
		if st.Groups[id] == nil {
			return errors.New("Gruppe nicht gefunden")
		}
		delete(st.Groups, id)
		for _, u := range st.Users {
			u.Groups = without(u.Groups, id)
		}
		return nil
	})
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
