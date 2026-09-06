package core

import "time"

// Sample is one probe round against one anchor on one Network Key.
type Sample struct {
	Time       time.Time `json:"time"`
	NetworkKey string    `json:"network_key"`
	Anchor     string    `json:"anchor"` // "gateway" or the configured host/IP
	AnchorAddr string    `json:"anchor_addr,omitempty"`
	RTTms      float64   `json:"rtt_ms"` // median of the probes; -1 when all lost
	Loss       float64   `json:"loss"`   // 0..1
	DNSms      float64   `json:"dns_ms"` // -1 when not measured / failed; only on the first anchor row of a round
	Method     string    `json:"method"` // icmp | tcp
}

// BaselineState is the monitor's verdict for a Network Key.
type BaselineState string

const (
	BaselineLearning BaselineState = "learning"
	BaselineOK       BaselineState = "ok"
	BaselineDegraded BaselineState = "degraded"
	BaselineIdle     BaselineState = "idle" // not connected / no samples yet
)

// Baseline is the rolling statistic for one (Network Key, anchor).
type Baseline struct {
	NetworkKey   string        `json:"network_key"`
	Anchor       string        `json:"anchor"`
	State        BaselineState `json:"state"`
	SampleCount  int           `json:"sample_count"`
	BaselineRTT  float64       `json:"baseline_rtt_ms"`
	BaselineLoss float64       `json:"baseline_loss"`
	CurrentRTT   float64       `json:"current_rtt_ms"`
	CurrentLoss  float64       `json:"current_loss"`
	CurrentDNS   float64       `json:"current_dns_ms"`
	Since        time.Time     `json:"since,omitempty"` // when State began
	UpdatedAt    time.Time     `json:"updated_at"`
}

// MonitorStatus is the summary the surfaces show.
type MonitorStatus struct {
	NetworkKey string        `json:"network_key"`
	State      BaselineState `json:"state"`
	Anchors    []Baseline    `json:"anchors"`
	LastSample time.Time     `json:"last_sample,omitempty"`
	Interval   time.Duration `json:"interval"`
	Paused     bool          `json:"paused"`
}

// SpeedResult is one on-demand bandwidth test.
type SpeedResult struct {
	Time         time.Time     `json:"time"`
	NetworkKey   string        `json:"network_key"`
	Provider     string        `json:"provider"` // cloudflare | iperf3 | librespeed
	Server       string        `json:"server,omitempty"`
	DownloadMbps float64       `json:"download_mbps"`
	UploadMbps   float64       `json:"upload_mbps"`
	LatencyMs    float64       `json:"latency_ms"`
	JitterMs     float64       `json:"jitter_ms"`
	BytesMoved   int64         `json:"bytes_moved"`
	Duration     time.Duration `json:"duration"`
	Quick        bool          `json:"quick"`
}

// SpeedProgress streams during a test.
type SpeedProgress struct {
	Phase   string  `json:"phase"` // latency | download | upload | done
	Mbps    float64 `json:"mbps"`
	Percent float64 `json:"percent"`
	Bytes   int64   `json:"bytes"`
}

// EventType enumerates the facts the daemon emits.
type EventType string

const (
	EventConnected        EventType = "connected"
	EventDisconnected     EventType = "disconnected"
	EventNoInternet       EventType = "no-internet"
	EventInternetRestored EventType = "internet-restored"
	EventVPNUp            EventType = "vpn-up"
	EventVPNDown          EventType = "vpn-down"
	EventDegraded         EventType = "degraded"
	EventRecovered        EventType = "recovered"
	EventWifiScan         EventType = "wifi-scan"     // state change only, never notified
	EventStateChanged     EventType = "state-changed" // generic "refresh" hint for surfaces
)

// Event is a fact that happened; surfaces render it, notify delivers the notable ones.
type Event struct {
	Time       time.Time         `json:"time"`
	Type       EventType         `json:"type"`
	NetworkKey string            `json:"network_key,omitempty"`
	Title      string            `json:"title"`
	Body       string            `json:"body,omitempty"`
	Urgency    string            `json:"urgency,omitempty"` // low | normal | critical
	Data       map[string]string `json:"data,omitempty"`
}
