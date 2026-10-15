package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrUpdated is returned by Run after a successful self-update; the
// process should exit non-zero so its supervisor (Windows service,
// launchd, systemd, task scheduler) starts the new binary.
var ErrUpdated = errors.New("agent updated, restarting")

// autoUpdateEnabled: on by default, except on macOS where replacing the
// binary would revoke the Screen Recording/Accessibility permissions.
func (c *Config) autoUpdateEnabled() bool {
	switch c.AutoUpdate {
	case "on", "true":
		return true
	case "off", "false":
		return false
	}
	return runtime.GOOS != "darwin"
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// maybeUpdate is called when the server announces the agent binary it
// offers. It waits until nobody is connected, downloads the binary over the
// pinned TLS connection, verifies the SHA-256 the server announced over the
// authenticated signaling channel, and swaps it in.
func (a *Agent) maybeUpdate(ctx context.Context, file, want string) {
	if file == "" || want == "" || !a.cfg.autoUpdateEnabled() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return
	}
	have, err := fileSHA256(exe)
	if err != nil || strings.EqualFold(have, want) {
		return
	}
	a.mu.Lock()
	if a.updating || time.Since(a.lastUpdateTry) < time.Hour {
		a.mu.Unlock()
		return
	}
	a.updating = true
	a.lastUpdateTry = time.Now()
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.updating = false
		a.mu.Unlock()
	}()

	log.Printf("update available (%s), waiting until no session is active", want[:12])
	for {
		a.mu.Lock()
		busy := len(a.sessions) > 0
		a.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}

	if err := a.installUpdate(ctx, exe, file, want); err != nil {
		log.Printf("update failed: %v", err)
		return
	}
	log.Printf("update installed, restarting")
	a.cancelRun(ErrUpdated)
}

func (a *Agent) installUpdate(ctx context.Context, exe, file, want string) error {
	client := &http.Client{
		Timeout:   5 * time.Minute,
		Transport: &http.Transport{TLSClientConfig: TLSConfig(a.cfg.CAFingerprint, a.cfg.Server), Proxy: http.ProxyFromEnvironment},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.cfg.Server, "/")+"/download/"+file, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp.Status)
	}

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".remotly-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 200<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch (got %s, want %s) – not installing", got[:12], want[:12])
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		// A running .exe cannot be overwritten, but it can be renamed.
		old := exe + ".old"
		if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
			old = fmt.Sprintf("%s.old-%d", exe, time.Now().Unix()) // previous one still in use
		}
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), exe); err != nil {
			os.Rename(old, exe) // roll back
			return err
		}
		return nil
	}
	return os.Rename(tmp.Name(), exe) // atomic on Unix
}
