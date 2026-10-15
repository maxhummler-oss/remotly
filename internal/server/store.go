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

type state struct {
	Users      map[string]*User         `json:"users"`
	Devices    map[string]*DeviceRecord `json:"devices"`
	EnrollKey  string                   `json:"enrollKey"`
	TURNSecret string                   `json:"turnSecret"`
}

// Store is the persistent server state (JSON file, written atomically).
// It reloads the file when it was modified externally, so the CLI can
// manage users while the server is running.
type Store struct {
	path    string
	mu      sync.Mutex
	st      state
	modTime time.Time
}

var userNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// OpenStore loads or initializes the state file.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.load(); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		s.st = state{Users: map[string]*User{}, Devices: map[string]*DeviceRecord{}}
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
	s.st = st
	s.modTime = info.ModTime()
	return nil
}

// refresh reloads the file if another process changed it. Caller holds mu.
func (s *Store) refresh() {
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

// ValidatePassword enforces a minimal password policy.
func ValidatePassword(pw string) error {
	if len(pw) < 10 {
		return errors.New("Passwort muss mindestens 10 Zeichen lang sein")
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
	s.update(func(st *state) error {
		if p := st.Users[name]; p != nil {
			p.LastLogin = time.Now()
		}
		return nil
	})
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
	if err := ValidatePassword(password); err != nil {
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
		st.Users[name] = &User{Name: name, DisplayName: display, PasswordHash: hash, Admin: admin, Devices: []string{}, Created: time.Now()}
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
	ResetTOTP   bool      `json:"resetTotp"`
}

// UpdateUser applies a patch.
func (s *Store) UpdateUser(name string, p UserPatch) error {
	var hash string
	if p.Password != nil {
		if err := ValidatePassword(*p.Password); err != nil {
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

// DeviceSeen updates the information an agent reports on connect.
func (s *Store) DeviceSeen(id, hostname, osName, arch, ip string, macs []string) {
	s.update(func(st *state) error {
		if d := st.Devices[id]; d != nil {
			d.Hostname, d.OS, d.Arch, d.LastIP = hostname, osName, arch, ip
			if len(macs) > 0 {
				d.MACs = macs
			}
			d.LastSeen = time.Now()
		}
		return nil
	})
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
			kept := u.Devices[:0]
			for _, d := range u.Devices {
				if d != id {
					kept = append(kept, d)
				}
			}
			u.Devices = kept
		}
		return nil
	})
}

// CanAccess reports whether user may control device.
func (u *User) CanAccess(deviceID string) bool {
	if u.Admin {
		return true
	}
	for _, d := range u.Devices {
		if d == deviceID {
			return true
		}
	}
	return false
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
