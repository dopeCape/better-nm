package api

import (
	"time"

	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/daemon"
)

// Prefix is the path prefix of every route.
const Prefix = "/v1"

// SSE event names.
const (
	SSEChange   = "change"   // GET /events/stream: data is a core.Change
	SSEEvent    = "event"    // GET /events/stream: data is a core.Event
	SSEProgress = "progress" // POST /speed: data is a core.SpeedProgress
	SSEResult   = "result"   // POST /speed: data is a core.SpeedResult
	SSEError    = "error"    // POST /speed: data is an ErrorResponse
)

// StatusResponse is GET /status: the NM status plus daemon identity.
type StatusResponse struct {
	core.Status
	Version         string    `json:"version"`
	APIVersion      int       `json:"api_version"`
	UptimeSeconds   float64   `json:"uptime_seconds"`
	Started         time.Time `json:"started"`
	SnapshotVersion uint64    `json:"snapshot_version"`
}

// ErrorResponse is every non-2xx body.
type ErrorResponse struct {
	Error string `json:"error"`
	Hint  string `json:"hint,omitempty"`
	Code  string `json:"code"` // core.ErrorKind
}

// OKResponse is the body of actions that return nothing else.
type OKResponse struct {
	OK bool `json:"ok"`
}

// DeviceRequest names a device; "" = the first Wi-Fi device.
type DeviceRequest struct {
	Device string `json:"device,omitempty"`
}

// UUIDRequest names a profile.
type UUIDRequest struct {
	UUID string `json:"uuid"`
}

// OnRequest toggles something.
type OnRequest struct {
	On bool `json:"on"`
}

// IPRequest is PUT /profiles/{uuid}/ip; a nil family is left untouched.
type IPRequest struct {
	IPv4 *core.IPConfig `json:"ipv4,omitempty"`
	IPv6 *core.IPConfig `json:"ipv6,omitempty"`
}

// ExitNodeRequest is POST /vpn/tailscale/exit-node; Peer "" clears it.
type ExitNodeRequest struct {
	Peer     string `json:"peer"`
	AllowLAN bool   `json:"allow_lan"`
}

// LoginResponse is POST /vpn/tailscale/login.
type LoginResponse struct {
	URL string `json:"url"`
}

// KeyRequest names a Network Key; "" = the current one.
type KeyRequest struct {
	Key string `json:"key,omitempty"`
}

// ConfigSetRequest is PUT /config.
type ConfigSetRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ImportVPNRequest and ImportVPNResult are POST /vpn/import.
type (
	ImportVPNRequest = daemon.ImportVPNRequest
	ImportVPNResult  = daemon.ImportVPNResult
)

// StreamItem is one decoded item of GET /events/stream.
type StreamItem = daemon.StreamItem
