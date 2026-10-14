package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreUsersAndDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddUser("Admin", "Ada", "short", true); err == nil {
		t.Fatal("short password accepted")
	}
	if err := s.AddUser("Admin", "Ada", "correct horse battery", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Authenticate("admin", "wrong password!!"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, ok := s.Authenticate(" ADMIN ", "correct horse battery"); !ok {
		t.Fatal("valid login rejected")
	}
	if err := s.DeleteUser("admin"); err == nil {
		t.Fatal("last admin deleted")
	}

	if _, _, err := s.Enroll("bad", "pc", "pc", "windows", "amd64", ""); err == nil {
		t.Fatal("bad enrollment key accepted")
	}
	id, secret, err := s.Enroll(s.EnrollKey(), "PC 1", "pc1", "windows", "amd64", "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AuthenticateDevice(id, secret+"x"); ok {
		t.Fatal("wrong device secret accepted")
	}
	if _, ok := s.AuthenticateDevice(id, secret); !ok {
		t.Fatal("device secret rejected")
	}

	if err := s.AddUser("max", "", "another long password", false); err != nil {
		t.Fatal(err)
	}
	u, _ := s.GetUser("max")
	if u.CanAccess(id) {
		t.Fatal("user can access unassigned device")
	}
	devs := []string{id, "unknown"}
	s.UpdateUser("max", UserPatch{Devices: &devs})
	u, _ = s.GetUser("max")
	if !u.CanAccess(id) || len(u.Devices) != 1 {
		t.Fatalf("assignment wrong: %v", u.Devices)
	}

	// Reload from disk; secrets are stored hashed only.
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := s2.GetDevice(id)
	if strings.Contains(d.SecretHash, secret) || d.SecretHash == "" {
		t.Fatal("device secret not hashed")
	}
	s2.DeleteDevice(id)
	u, _ = s2.GetUser("max")
	if len(u.Devices) != 0 {
		t.Fatal("deleted device still assigned")
	}
}

func TestTURNCredentials(t *testing.T) {
	user, pass := turnCredentials("secret", "u-max", time.Hour)
	if turnPassword("secret", user) != pass {
		t.Fatal("password mismatch")
	}
	if turnPassword("other", user) == pass {
		t.Fatal("password independent of secret")
	}
}

func TestLoginLockout(t *testing.T) {
	a := &auth{sessions: map[string]*webSession{}, failures: map[string]*failure{}, ttl: time.Hour}
	for i := 0; i < 4; i++ {
		a.fail("ip:1.2.3.4")
	}
	if a.locked("ip:1.2.3.4") > 0 {
		t.Fatal("locked too early")
	}
	a.fail("ip:1.2.3.4")
	if a.locked("ip:1.2.3.4") < 20*time.Second {
		t.Fatal("not locked after 5 failures")
	}
	a.succeed("ip:1.2.3.4")
	if a.locked("ip:1.2.3.4") > 0 {
		t.Fatal("still locked after success")
	}
}

func TestPKI(t *testing.T) {
	dir := t.TempDir()
	p, err := loadOrCreatePKI(dir, []string{"192.168.1.10"}, []string{"remotly.firma.local"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := loadOrCreatePKI(dir, []string{"192.168.1.10"}, []string{"remotly.firma.local"})
	if err != nil {
		t.Fatal(err)
	}
	if p.caFP != p2.caFP {
		t.Fatal("CA regenerated on reload")
	}
	if len(p.certificate.Certificate) != 2 {
		t.Fatal("chain does not include the CA")
	}
}
