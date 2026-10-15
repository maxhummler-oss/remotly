// Package protocol defines the signaling messages exchanged over the
// WebSocket connection between the server, agents and viewers.
//
// Media and input never travel over this channel: once a session is
// negotiated, video/audio flow over WebRTC (direct LAN path when possible,
// TURN relay on the server otherwise) and input over WebRTC data channels.
package protocol

import "encoding/json"

// Message types.
const (
	// agent -> server (first message)
	TypeHello = "hello"

	// server -> client
	TypeWelcome = "welcome" // reply to hello (includes ICE servers)
	TypeDevices = "devices" // device list (viewers only)
	TypeError   = "error"
	TypeNotice  = "notice" // informational message for the viewer

	// viewer -> server -> agent
	TypeConnect = "connect" // viewer asks to open a session to an agent

	// routed in both directions inside a session
	TypeOffer     = "offer"
	TypeAnswer    = "answer"
	TypeCandidate = "candidate"
	TypeEnd       = "end" // session closed (by either side or the server)

	// server -> viewer
	TypeSessionStarted = "session-started"
)

// ICEServer mirrors the browser's RTCIceServer dictionary.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// AgentInfo is what an agent reports about itself on connect.
type AgentInfo struct {
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Version  string   `json:"version"`
	Encoder  string   `json:"encoder,omitempty"`
	Capture  string   `json:"capture,omitempty"`
	Audio    bool     `json:"audio,omitempty"`
	MACs     []string `json:"macs,omitempty"`
	Desktop  string   `json:"desktop,omitempty"` // Windows: input desktop (Default/Winlogon)
}

// Device is a device as shown to viewers.
type Device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OS      string `json:"os"`
	Online  bool   `json:"online"`
	Encoder string `json:"encoder,omitempty"`
	// InUseBy lists display names of users currently connected.
	InUseBy []string `json:"inUseBy,omitempty"`
	CanWake bool     `json:"canWake,omitempty"`
	// Pending devices await admin approval and cannot be used yet.
	Pending bool `json:"pending,omitempty"`
	// Groups are the names of the groups the device belongs to.
	Groups []string `json:"groups,omitempty"`
}

// Policy restricts what a session may do. It is decided by the server and
// enforced by the agent, because media and input flow peer-to-peer.
type Policy struct {
	// ViewOnly sessions receive video/audio but their input is ignored.
	ViewOnly bool `json:"viewOnly,omitempty"`
	// Clipboard: "both", "to-remote" (viewer -> PC only), "to-local"
	// (PC -> viewer only) or "off".
	Clipboard string `json:"clipboard,omitempty"`
}

// ClipToRemote reports whether clipboard text may flow viewer -> PC.
func (p *Policy) ClipToRemote() bool {
	return p == nil || p.Clipboard == "" || p.Clipboard == "both" || p.Clipboard == "to-remote"
}

// ClipToLocal reports whether clipboard text may flow PC -> viewer.
func (p *Policy) ClipToLocal() bool {
	return p == nil || p.Clipboard == "" || p.Clipboard == "both" || p.Clipboard == "to-local"
}

// Message is the single envelope used for all signaling traffic. Only the
// fields relevant to a given Type are populated.
type Message struct {
	Type string `json:"type"`

	// hello (agent)
	DeviceID string     `json:"deviceId,omitempty"`
	Secret   string     `json:"secret,omitempty"`
	Agent    *AgentInfo `json:"agent,omitempty"`

	// welcome
	ICEServers []ICEServer `json:"iceServers,omitempty"`
	ServerName string      `json:"serverName,omitempty"`
	// MaxBitrateKbps (server -> agent) caps video bitrate (0 = no cap).
	MaxBitrateKbps int    `json:"maxBitrateKbps,omitempty"`
	User           string `json:"user,omitempty"`
	// UpdateFile/UpdateSHA256 (server -> agent) announce the agent binary
	// the server offers for this agent's platform.
	UpdateFile   string `json:"updateFile,omitempty"`
	UpdateSHA256 string `json:"updateSha256,omitempty"`

	// devices
	Devices []Device `json:"devices,omitempty"`

	// session routing
	Session string `json:"session,omitempty"`
	Target  string `json:"target,omitempty"`
	// ForceRelay asks both peers to only use the TURN relay on the server.
	ForceRelay bool `json:"forceRelay,omitempty"`
	// Takeover (admins) ends other users' sessions on the target.
	Takeover bool `json:"takeover,omitempty"`
	// Codecs lists the video codecs the viewer can decode (MIME types,
	// e.g. "video/H264"), viewer -> server -> agent.
	Codecs []string `json:"codecs,omitempty"`
	// Viewer is the display name of the user (server -> agent, for logs).
	Viewer string `json:"viewer,omitempty"`
	// ViewOnly (viewer -> server) requests a watch-only session.
	ViewOnly bool `json:"viewOnly,omitempty"`
	// Policy (server -> agent and viewer) applies to the session.
	Policy *Policy `json:"policy,omitempty"`

	SDP       string          `json:"sdp,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`

	// end / error
	Reason string `json:"reason,omitempty"`
}
