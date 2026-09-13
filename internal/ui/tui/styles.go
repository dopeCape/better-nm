package tui

import "github.com/charmbracelet/lipgloss"

// The palette: a handful of adaptive colours so the same screen reads on a
// light or a dark terminal. NO_COLOR is honoured by lipgloss itself (it drops
// to the ASCII profile), so nothing here needs to check it.
var (
	colAccent = lipgloss.AdaptiveColor{Light: "#005f87", Dark: "#5fafd7"} // selection, active tab
	colGood   = lipgloss.AdaptiveColor{Light: "#008700", Dark: "#5fd75f"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#af8700", Dark: "#ffd75f"}
	colBad    = lipgloss.AdaptiveColor{Light: "#af0000", Dark: "#ff5f5f"}
	colDim    = lipgloss.AdaptiveColor{Light: "#8a8a8a", Dark: "#6c6c6c"}
	colText   = lipgloss.AdaptiveColor{Light: "#262626", Dark: "#d0d0d0"}
	colBar    = lipgloss.AdaptiveColor{Light: "#e4e4e4", Dark: "#303030"} // status/footer background
)

var (
	stText   = lipgloss.NewStyle().Foreground(colText)
	stDim    = lipgloss.NewStyle().Foreground(colDim)
	stAccent = lipgloss.NewStyle().Foreground(colAccent)
	stGood   = lipgloss.NewStyle().Foreground(colGood)
	stWarn   = lipgloss.NewStyle().Foreground(colWarn)
	stBad    = lipgloss.NewStyle().Foreground(colBad)
	stBold   = lipgloss.NewStyle().Bold(true)

	stBar       = lipgloss.NewStyle().Background(colBar).Foreground(colText)
	stBarDim    = lipgloss.NewStyle().Background(colBar).Foreground(colDim)
	stBarAccent = lipgloss.NewStyle().Background(colBar).Foreground(colAccent).Bold(true)
	stBarGood   = lipgloss.NewStyle().Background(colBar).Foreground(colGood)
	stBarWarn   = lipgloss.NewStyle().Background(colBar).Foreground(colWarn)
	stBarBad    = lipgloss.NewStyle().Background(colBar).Foreground(colBad)

	stSelected = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stRailOn   = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stRailOff  = lipgloss.NewStyle().Foreground(colDim)
	stHeader   = lipgloss.NewStyle().Foreground(colDim).Underline(true)
	stTitle    = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stBox      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(0, 1)
	stKey      = lipgloss.NewStyle().Foreground(colAccent)
)

// stateStyle colours a connectivity / VPN / device state word.
func stateStyle(s string) lipgloss.Style {
	switch s {
	case "connected", "activated", "full", "ok", "up", "reachable", "external":
		return stGood
	case "connecting", "activating", "limited", "portal", "learning", "needs-auth", "stale", "deactivating":
		return stWarn
	case "error", "failed", "none", "degraded", "incomplete", "unavailable":
		return stBad
	default:
		return stDim
	}
}
