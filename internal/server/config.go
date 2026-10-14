package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Config is the server configuration, stored as JSON (default
// /etc/remotly/server.json). Users, devices and keys live in
// DataDir/state.json and are managed through the web UI or CLI.
type Config struct {
	// Name shown in the web UI.
	Name string `json:"name"`
	// Listen is the HTTPS listen address.
	Listen string `json:"listen"`
	// HTTPRedirect, if set (e.g. ":80"), serves a redirect to HTTPS.
	HTTPRedirect string `json:"httpRedirect,omitempty"`

	// TLS: "auto" = certificate signed by a local CA generated in DataDir
	// (install DataDir/ca.crt on the thin clients once), "files" = use
	// CertFile/KeyFile (e.g. from your company PKI), "off" = plain HTTP
	// (not recommended: no clipboard, no keyboard lock, no encryption of the
	// login).
	TLS      string `json:"tls"`
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
	// Hostnames added to the auto certificate besides the detected IP.
	Hostnames []string `json:"hostnames,omitempty"`

	// TURNPort is the UDP port of the embedded TURN relay (0 disables it).
	TURNPort int `json:"turnPort"`
	// RelayPortMin/Max is the UDP port range used for relayed media.
	RelayPortMin int `json:"relayPortMin"`
	RelayPortMax int `json:"relayPortMax"`
	// PublicIP is the address peers use to reach the server/TURN relay.
	// Detected automatically when empty.
	PublicIP string `json:"publicIp,omitempty"`

	// SessionHours is how long a web login stays valid.
	SessionHours int `json:"sessionHours"`

	// DataDir holds state.json, audit.log and generated certificates.
	DataDir string `json:"dataDir"`
	// AgentsDir contains agent binaries offered for download
	// (remotly-agent-<os>-<arch>[.exe]).
	AgentsDir string `json:"agentsDir"`
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Name:         "Remotly",
		Listen:       ":443",
		HTTPRedirect: ":80",
		TLS:          "auto",
		TURNPort:     3478,
		RelayPortMin: 50000,
		RelayPortMax: 50999,
		SessionHours: 12,
		DataDir:      "/var/lib/remotly",
		AgentsDir:    "/usr/local/share/remotly/agents",
	}
}

// LoadConfig reads the config file. If it does not exist and create is
// true, the defaults are written.
func LoadConfig(path string, create bool) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist) && create:
		if err := SaveConfig(path, cfg); err != nil {
			return cfg, err
		}
	default:
		return cfg, err
	}
	if cfg.RelayPortMin <= 0 || cfg.RelayPortMax < cfg.RelayPortMin || cfg.RelayPortMax > 65535 {
		cfg.RelayPortMin, cfg.RelayPortMax = 50000, 50999
	}
	if cfg.SessionHours <= 0 {
		cfg.SessionHours = 12
	}
	return cfg, nil
}

// Resolve fills in values detected at runtime. The result is not meant to
// be saved, so auto-detected values follow network changes.
func (c *Config) Resolve() {
	if c.PublicIP == "" {
		c.PublicIP = DetectLANIP()
	}
}

// TLSEnabled reports whether the server speaks HTTPS.
func (c *Config) TLSEnabled() bool { return c.TLS == "auto" || c.TLS == "files" }

// StatePath is the location of the user/device database.
func (c *Config) StatePath() string { return filepath.Join(c.DataDir, "state.json") }

// SaveConfig writes cfg to path.
func SaveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(path, append(data, '\n'), 0o640)
}

// DetectLANIP returns the IP of the interface used for outbound traffic,
// which on a LAN server is the address other devices reach it at.
func DetectLANIP() string {
	// UDP "dial" does not send packets, it only selects a route.
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err == nil {
		defer conn.Close()
		return conn.LocalAddr().(*net.UDPAddr).IP.String()
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	return "127.0.0.1"
}
