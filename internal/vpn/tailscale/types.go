package tailscale

import (
	"sort"
	"strconv"
	"time"
)

// The types below are hand-written subsets of tailscale.com/ipn/ipnstate.Status,
// ipn.Prefs, ipn.MaskedPrefs and ipn.Notify (v1.98.10). Unknown JSON fields are
// ignored on decode, so a newer daemon only ever adds data we do not read.

// Backend states as reported in Status.BackendState (ipn.State names).
const (
	StateNoState          = "NoState"
	StateInUseOtherUser   = "InUseOtherUser"
	StateNeedsLogin       = "NeedsLogin"
	StateNeedsMachineAuth = "NeedsMachineAuth"
	StateStopped          = "Stopped"
	StateStarting         = "Starting"
	StateRunning          = "Running"
)

// stateNames maps the numeric ipn.State carried by Notify.State to its name.
var stateNames = [...]string{
	StateNoState, StateInUseOtherUser, StateNeedsLogin, StateNeedsMachineAuth,
	StateStopped, StateStarting, StateRunning,
}

// StateName converts a numeric ipn.State to its name; unknown values become "State(n)".
func StateName(n int) string {
	if n >= 0 && n < len(stateNames) {
		return stateNames[n]
	}
	return "State(" + strconv.Itoa(n) + ")"
}

// Status is the subset of ipnstate.Status bnm reads.
type Status struct {
	Version        string
	TUN            bool
	BackendState   string
	HaveNodeKey    bool
	AuthURL        string
	TailscaleIPs   []string
	Self           *PeerStatus
	ExitNodeStatus *ExitNodeStatus
	Health         []string
	MagicDNSSuffix string
	CurrentTailnet *TailnetStatus
	// Peer is keyed by the peer's node public key ("nodekey:...").
	Peer map[string]*PeerStatus
}

// PeerStatus is the subset of ipnstate.PeerStatus bnm reads.
type PeerStatus struct {
	ID             string // tailcfg.StableNodeID
	PublicKey      string
	HostName       string
	DNSName        string
	OS             string
	TailscaleIPs   []string
	Relay          string
	LastSeen       time.Time
	Online         bool
	ExitNode       bool
	ExitNodeOption bool
	Active         bool
}

// ExitNodeStatus describes the exit node in use.
type ExitNodeStatus struct {
	ID           string
	Online       bool
	TailscaleIPs []string
}

// TailnetStatus describes the tailnet the node is connected to.
type TailnetStatus struct {
	Name            string
	MagicDNSSuffix  string
	MagicDNSEnabled bool
}

// Prefs is the subset of ipn.Prefs bnm reads.
type Prefs struct {
	ControlURL             string
	RouteAll               bool
	ExitNodeID             string
	ExitNodeIP             string
	ExitNodeAllowLANAccess bool
	CorpDNS                bool
	WantRunning            bool
	LoggedOut              bool
	Hostname               string
	OperatorUser           string
}

// MaskedPrefs is the PATCH body for EditPrefs: every field to change is sent
// alongside its <Field>Set flag; fields whose flag is false are ignored by
// tailscaled. Zero values are omitted from the JSON, which is fine because an
// omitted field decodes to the same zero value on the daemon side.
type MaskedPrefs struct {
	ControlURL                string `json:",omitempty"`
	ControlURLSet             bool   `json:",omitempty"`
	RouteAll                  bool   `json:",omitempty"`
	RouteAllSet               bool   `json:",omitempty"`
	ExitNodeID                string `json:",omitempty"`
	ExitNodeIDSet             bool   `json:",omitempty"`
	ExitNodeIP                string `json:",omitempty"`
	ExitNodeIPSet             bool   `json:",omitempty"`
	ExitNodeAllowLANAccess    bool   `json:",omitempty"`
	ExitNodeAllowLANAccessSet bool   `json:",omitempty"`
	CorpDNS                   bool   `json:",omitempty"`
	CorpDNSSet                bool   `json:",omitempty"`
	WantRunning               bool   `json:",omitempty"`
	WantRunningSet            bool   `json:",omitempty"`
	LoggedOut                 bool   `json:",omitempty"`
	LoggedOutSet              bool   `json:",omitempty"`
	OperatorUser              string `json:",omitempty"`
	OperatorUserSet           bool   `json:",omitempty"`
}

// Notify is the subset of ipn.Notify bnm decodes from the IPN bus stream.
type Notify struct {
	Version     string
	SessionID   string
	ErrMessage  *string
	State       *int // ipn.State; see StateName
	Prefs       *Prefs
	BrowseToURL *string
	Health      *HealthState
}

// StateName returns the name of n.State, or "" when the notify carries no state.
func (n Notify) StateName() string {
	if n.State == nil {
		return ""
	}
	return StateName(*n.State)
}

// HealthState is the subset of health.State bnm decodes.
type HealthState struct {
	Warnings map[string]HealthWarning
}

// HealthWarning is one active health.UnhealthyState.
type HealthWarning struct {
	WarnableCode        string
	Severity            string
	Title               string
	Text                string
	ImpactsConnectivity bool
}

// Messages flattens the warnings to human strings, sorted by code for stability.
func (h *HealthState) Messages() []string {
	if h == nil || len(h.Warnings) == 0 {
		return nil
	}
	codes := make([]string, 0, len(h.Warnings))
	for c := range h.Warnings {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		w := h.Warnings[c]
		msg := w.Text
		if msg == "" {
			msg = w.Title
		}
		if msg == "" {
			msg = c
		}
		out = append(out, msg)
	}
	return out
}

// NotifyWatchOpt is the bitmask passed to WatchIPNBus (ipn.NotifyWatchOpt).
type NotifyWatchOpt uint64

const (
	NotifyWatchEngineUpdates NotifyWatchOpt = 1 << 0
	NotifyInitialState       NotifyWatchOpt = 1 << 1
	NotifyInitialPrefs       NotifyWatchOpt = 1 << 2
	NotifyInitialNetMap      NotifyWatchOpt = 1 << 3
	NotifyNoPrivateKeys      NotifyWatchOpt = 1 << 4
	NotifyInitialHealthState NotifyWatchOpt = 1 << 7
	NotifyRateLimit          NotifyWatchOpt = 1 << 8
)

// DefaultWatchMask is what the adapter subscribes with: initial state, prefs and
// health, rate-limited, and no NetMap (which is large and private-key bearing).
const DefaultWatchMask = NotifyInitialState | NotifyInitialPrefs | NotifyInitialHealthState | NotifyNoPrivateKeys | NotifyRateLimit
