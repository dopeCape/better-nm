package desktop

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/core"
)

// headerState is what the header strip and the tray show.
type headerState struct {
	status  *api.StatusResponse
	monitor *core.MonitorStatus
}

// connectionName is the primary connection's name, or "Not connected".
func (s headerState) connectionName() string {
	if s.status == nil || s.status.Primary == nil {
		return "Not connected"
	}
	return s.status.Primary.ProfileName
}

func (s headerState) ip() string {
	if s.status == nil || s.status.Primary == nil {
		return ""
	}
	return firstOr(s.status.Primary.IPv4, firstOr(s.status.Primary.IPv6, ""))
}

func (s headerState) connectivity() string {
	if s.status == nil {
		return string(core.ConnUnknown)
	}
	if s.status.Primary == nil {
		return "none"
	}
	return string(s.status.Connectivity)
}

// verdict is the monitor's one-word state for the header and tray.
func (s headerState) verdict() string {
	if s.monitor == nil {
		return "unknown"
	}
	if s.monitor.Paused {
		return "paused"
	}
	if s.monitor.State == "" {
		return string(core.BaselineIdle)
	}
	return string(s.monitor.State)
}

// header is the compact strip: connectivity dot, connection name, IP, verdict.
type header struct {
	dot     *dot
	name    *widget.Label
	ip      *widget.Label
	verdict *widget.Label
	box     fyne.CanvasObject
}

func newHeader() *header {
	h := &header{
		dot:     newDot(10),
		name:    bold("Loading"),
		ip:      mono(""),
		verdict: widget.NewLabel(""),
	}
	left := container.NewHBox(inset(h.dot, 0, 4, 0, 0), h.name, inset(h.ip, 0, 0, 0, 8))
	row := container.NewBorder(nil, nil, left, h.verdict)
	h.box = inset(row, 10, 24, 10, 20)
	return h
}

func (h *header) content() fyne.CanvasObject { return h.box }

func (h *header) set(s headerState) {
	h.name.SetText(s.connectionName())
	h.ip.SetText(s.ip())
	h.dot.setColor(semanticColor(s.connectivity()))
	v := s.verdict()
	h.verdict.SetText("Quality: " + v)
	switch v {
	case "degraded":
		h.verdict.Importance = widget.WarningImportance
	case "ok":
		h.verdict.Importance = widget.SuccessImportance
	default:
		h.verdict.Importance = widget.LowImportance
	}
	h.verdict.Refresh()
}
