package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/maxhummler-oss/remotly/internal/agent/stream"
)

// Config is the agent configuration (JSON).
type Config struct {
	Server string `json:"server"` // e.g. https://192.168.1.10
	// CAFingerprint pins the server's CA certificate (SHA-256, hex). Empty
	// means the system trust store is used (company PKI / public CA).
	CAFingerprint string `json:"caFingerprint,omitempty"`

	DeviceID string `json:"deviceId"`
	Secret   string `json:"secret"`

	FFmpeg           string   `json:"ffmpeg,omitempty"`
	Encoder          string   `json:"encoder"` // auto | h264_nvenc | h264_qsv | h264_amf | h264_videotoolbox | libx264
	Capture          string   `json:"capture"` // auto | ddagrab | gdigrab | avfoundation | x11grab
	Display          int      `json:"display"`
	FPS              int      `json:"fps"`
	BitrateKbps      int      `json:"bitrateKbps"`
	MaxWidth         int      `json:"maxWidth"` // 0 = native resolution (enables zero-copy on Windows)
	KeyframeInterval float64  `json:"keyframeInterval"`
	InputArgs        []string `json:"inputArgs,omitempty"`
	EncoderArgs      []string `json:"encoderArgs,omitempty"`

	// LocalCursor hides the cursor in the video and lets the viewer draw it
	// locally with the remote cursor's shape: zero perceived mouse latency.
	// "auto" enables it where shape tracking is supported (Windows).
	LocalCursor string `json:"localCursor"`
	// Audio: "auto", "off" or custom ffmpeg input arguments in AudioArgs.
	Audio     string   `json:"audio"`
	AudioArgs []string `json:"audioArgs,omitempty"`
	// Clipboard enables text clipboard sync.
	Clipboard bool `json:"clipboard"`
}

// DefaultConfigPath: next to the executable on Windows (C:\Program
// Files\Remotly\agent.json for the service), ~/Library/Application
// Support/Remotly/agent.json on macOS, ~/.config/remotly/agent.json on Linux.
func DefaultConfigPath() string {
	if runtime.GOOS == "windows" {
		if exe, err := os.Executable(); err == nil {
			return filepath.Join(filepath.Dir(exe), "agent.json")
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	name := "Remotly"
	if runtime.GOOS == "linux" {
		name = "remotly"
	}
	return filepath.Join(dir, name, "agent.json")
}

func defaults() Config {
	return Config{
		Encoder:          "auto",
		Capture:          "auto",
		FPS:              60,
		BitrateKbps:      30000,
		KeyframeInterval: 4,
		LocalCursor:      "auto",
		Audio:            "auto",
		Clipboard:        true,
	}
}

// LoadConfig reads the config, filling defaults. A missing file yields
// defaults and os.ErrNotExist.
func LoadConfig(path string) (Config, error) {
	cfg := defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// SaveConfig writes the config with owner-only permissions.
func SaveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Validate checks required fields and normalizes values.
func (c *Config) Validate() error {
	if c.Server == "" || c.DeviceID == "" || c.Secret == "" {
		return errors.New("device not enrolled (run 'remotly-agent enroll')")
	}
	if c.FPS <= 0 || c.FPS > 240 {
		c.FPS = 60
	}
	if c.BitrateKbps <= 0 {
		c.BitrateKbps = 30000
	}
	if c.KeyframeInterval <= 0 {
		c.KeyframeInterval = 4
	}
	return nil
}

// UseLocalCursor reports whether the viewer draws the cursor.
func (c *Config) UseLocalCursor() bool {
	switch c.LocalCursor {
	case "on", "true":
		return true
	case "off", "false":
		return false
	}
	return runtime.GOOS == "windows"
}

// StreamOptions converts the config into capture/encoder options.
func (c *Config) StreamOptions() stream.Options {
	return stream.Options{
		FFmpeg:           stream.FFmpegPath(c.FFmpeg),
		Encoder:          c.Encoder,
		Capture:          c.Capture,
		Display:          c.Display,
		FPS:              c.FPS,
		BitrateKbps:      c.BitrateKbps,
		MaxWidth:         c.MaxWidth,
		KeyframeInterval: c.KeyframeInterval,
		DrawCursor:       !c.UseLocalCursor(),
		InputArgs:        c.InputArgs,
		EncoderArgs:      c.EncoderArgs,
		Audio:            c.Audio,
		AudioArgs:        c.AudioArgs,
	}
}

// TLSConfig returns the client TLS configuration. With a pinned CA
// fingerprint the server chain must be signed by exactly that CA (the
// server sends it along); otherwise the system roots are used.
func TLSConfig(caFingerprint, serverURL string) *tls.Config {
	if caFingerprint == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	host := serverURL
	if u, err := url.Parse(serverURL); err == nil {
		host = u.Hostname()
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by the pinned verification below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			var leaf, ca *x509.Certificate
			for i, der := range raw {
				cert, err := x509.ParseCertificate(der)
				if err != nil {
					return err
				}
				if i == 0 {
					leaf = cert
				}
				sum := sha256.Sum256(der)
				if strings.EqualFold(hex.EncodeToString(sum[:]), caFingerprint) {
					ca = cert
				}
			}
			if leaf == nil || ca == nil {
				return errors.New("server certificate is not signed by the pinned Remotly CA")
			}
			roots := x509.NewCertPool()
			roots.AddCert(ca)
			_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host})
			return err
		},
	}
}

// Enroll registers this machine with the server using the enrollment key
// and returns the device credentials.
func Enroll(ctx context.Context, server, caFP, key, name string) (string, string, error) {
	host, _ := os.Hostname()
	if name == "" {
		name = strings.TrimSuffix(host, ".local")
	}
	body, _ := json.Marshal(map[string]string{
		"key": key, "name": name, "hostname": host, "os": runtime.GOOS, "arch": runtime.GOARCH,
	})
	client := &http.Client{
		Timeout:   20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: TLSConfig(caFP, server), Proxy: http.ProxyFromEnvironment},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+"/api/agent/enroll", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		json.Unmarshal(data, &e)
		return "", "", fmt.Errorf("server: %s (%s)", e.Error, resp.Status)
	}
	var out struct{ ID, Secret string }
	if err := json.Unmarshal(data, &out); err != nil {
		return "", "", err
	}
	return out.ID, out.Secret, nil
}
