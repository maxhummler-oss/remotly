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
	User       string      `json:"user,omitempty"`

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

	SDP       string          `json:"sdp,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`

	// end / error
	Reason string `json:"reason,omitempty"`
}
