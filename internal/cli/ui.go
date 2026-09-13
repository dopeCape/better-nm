package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/dopeCape/better-nm/internal/core"
)

// ui holds the colour renderer and the handful of styles every table uses.
// Output is kept narrow enough for 80 columns.
type ui struct {
	r      *lipgloss.Renderer
	color  bool
	isTTY  bool
	header lipgloss.Style
	dim    lipgloss.Style
	bold   lipgloss.Style
	green  lipgloss.Style
	yellow lipgloss.Style
	red    lipgloss.Style
	cyan   lipgloss.Style
}

func newUI(w io.Writer, noColor bool) *ui {
	r := lipgloss.NewRenderer(w)
	profile := termenv.Ascii
	isTTY := false
	if f, ok := w.(*os.File); ok {
		isTTY = isTerminal(f)
	}
	if !noColor {
		// EnvColorProfile honours NO_COLOR and CLICOLOR_FORCE; for a non-TTY it
		// yields Ascii unless forced.
		profile = termenv.NewOutput(w).EnvColorProfile()
	}
	r.SetColorProfile(profile)
	u := &ui{r: r, color: profile != termenv.Ascii, isTTY: isTTY}
	u.header = r.NewStyle().Bold(true).Faint(true)
	u.dim = r.NewStyle().Faint(true)
	u.bold = r.NewStyle().Bold(true)
	u.green = r.NewStyle().Foreground(lipgloss.Color("2"))
	u.yellow = r.NewStyle().Foreground(lipgloss.Color("3"))
	u.red = r.NewStyle().Foreground(lipgloss.Color("1"))
	u.cyan = r.NewStyle().Foreground(lipgloss.Color("6"))
	return u
}

// dot is the ● state marker: green good, yellow in progress, red bad, dim off.
func (u *ui) dot(kind string) string {
	switch kind {
	case "green":
		return u.green.Render("●")
	case "yellow":
		return u.yellow.Render("●")
	case "red":
		return u.red.Render("●")
	}
	return u.dim.Render("○")
}

// vpnDot maps a VPN state to a dot colour.
func (u *ui) vpnDot(s core.VPNState) string {
	switch s {
	case core.VPNConnected:
		return u.dot("green")
	case core.VPNConnecting, core.VPNNeedsAuth, core.VPNNeedsSetup:
		return u.dot("yellow")
	case core.VPNError:
		return u.dot("red")
	}
	return u.dot("")
}

// vpnState colours a VPN state word.
func (u *ui) vpnState(s core.VPNState) string {
	switch s {
	case core.VPNConnected:
		return u.green.Render(string(s))
	case core.VPNConnecting, core.VPNNeedsAuth, core.VPNNeedsSetup:
		return u.yellow.Render(string(s))
	case core.VPNError:
		return u.red.Render(string(s))
	case core.VPNUnavailable:
		return u.dim.Render(string(s))
	}
	return string(s)
}

// baselineDot maps a baseline state to a dot colour.
func (u *ui) baselineDot(s core.BaselineState) string {
	switch s {
	case core.BaselineOK:
		return u.dot("green")
	case core.BaselineLearning:
		return u.dot("yellow")
	case core.BaselineDegraded:
		return u.dot("red")
	}
	return u.dot("")
}

func (u *ui) baselineState(s core.BaselineState) string {
	switch s {
	case core.BaselineOK:
		return u.green.Render(string(s))
	case core.BaselineLearning:
		return u.yellow.Render(string(s))
	case core.BaselineDegraded:
		return u.red.Render(string(s))
	case "":
		return u.dim.Render("idle")
	}
	return u.dim.Render(string(s))
}

// deviceDot maps a device state to a dot colour.
func (u *ui) deviceDot(s core.DeviceState) string {
	switch s {
	case core.DeviceConnected, core.DeviceExternal:
		return u.dot("green")
	case core.DeviceConnecting:
		return u.dot("yellow")
	case core.DeviceFailed:
		return u.dot("red")
	}
	return u.dot("")
}

// bars renders a 0-100 signal as ▂▄▆█ with the lit part coloured.
func (u *ui) bars(strength uint8) string {
	const glyphs = "▂▄▆█"
	lit := 0
	switch {
	case strength >= 75:
		lit = 4
	case strength >= 50:
		lit = 3
	case strength >= 25:
		lit = 2
	case strength > 0:
		lit = 1
	}
	style := u.green
	switch {
	case strength < 30:
		style = u.red
	case strength < 55:
		style = u.yellow
	}
	runes := []rune(glyphs)
	if !u.color {
		// Without colour a dim ghost is invisible; pad with spaces instead.
		return string(runes[:lit]) + strings.Repeat(" ", 4-lit)
	}
	return style.Render(string(runes[:lit])) + u.dim.Render(string(runes[lit:]))
}

// connectivity colours NM's connectivity word.
func (u *ui) connectivity(c core.Connectivity) string {
	switch c {
	case core.ConnFull:
		return u.green.Render("full")
	case core.ConnPortal:
		return u.yellow.Render("portal (needs a login page)")
	case core.ConnLimited:
		return u.yellow.Render("limited")
	case core.ConnNone:
		return u.red.Render("none")
	}
	return u.dim.Render("unknown")
}

// --- tables ---------------------------------------------------------------------

// table is a simple aligned column layout. Cells may carry ANSI colour; widths
// are measured with lipgloss.Width so alignment survives.
type table struct {
	u       *ui
	headers []string
	right   map[int]bool
	rows    [][]string
	shrink  int // column truncated to keep the table within maxTableWidth; -1 = none
}

// maxTableWidth is the width tables are kept under when a column may shrink.
const maxTableWidth = 80

func (u *ui) table(headers ...string) *table {
	return &table{u: u, headers: headers, right: map[int]bool{}, shrink: -1}
}

// alignRight right-aligns the given column indexes.
func (t *table) alignRight(cols ...int) *table {
	for _, c := range cols {
		t.right[c] = true
	}
	return t
}

// shrinkCol marks the free-text column that gets truncated when the table
// would otherwise exceed 80 columns.
func (t *table) shrinkCol(col int) *table {
	t.shrink = col
	return t
}

func (t *table) add(cells ...string) {
	t.rows = append(t.rows, cells)
}

func (t *table) render(w io.Writer) {
	n := len(t.headers)
	for _, r := range t.rows {
		if len(r) > n {
			n = len(r)
		}
	}
	widths := make([]int, n)
	for i, h := range t.headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if w := lipgloss.Width(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	if s := t.shrink; s >= 0 && s < n {
		total := 2 * (n - 1)
		for _, w := range widths {
			total += w
		}
		if total > maxTableWidth {
			limit := widths[s] - (total - maxTableWidth)
			if limit < 12 {
				limit = 12
			}
			widths[s] = limit
			for _, r := range t.rows {
				if s < len(r) && lipgloss.Width(r[s]) > limit {
					r[s] = truncateANSI(r[s], limit)
				}
			}
		}
	}
	line := func(cells []string, style func(string) string) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			pad := widths[i] - lipgloss.Width(c)
			if pad < 0 {
				pad = 0
			}
			if i == n-1 && !t.right[i] {
				b.WriteString(style(c))
			} else if t.right[i] {
				b.WriteString(strings.Repeat(" ", pad))
				b.WriteString(style(c))
			} else {
				b.WriteString(style(c))
				b.WriteString(strings.Repeat(" ", pad))
			}
			if i < n-1 {
				b.WriteString("  ")
			}
		}
		return strings.TrimRight(b.String(), " ")
	}
	if len(t.headers) > 0 {
		fmt.Fprintln(w, line(t.headers, func(s string) string { return t.u.header.Render(s) }))
	}
	for _, r := range t.rows {
		fmt.Fprintln(w, line(r, func(s string) string { return s }))
	}
}

// kv prints aligned "  Key   value" lines for a summary screen.
type kv struct {
	u     *ui
	pairs [][2]string
}

func (u *ui) kv() *kv { return &kv{u: u} }

func (k *kv) add(key, value string) {
	if value == "" {
		return
	}
	k.pairs = append(k.pairs, [2]string{key, value})
}

func (k *kv) render(w io.Writer) {
	width := 0
	for _, p := range k.pairs {
		if len(p[0]) > width {
			width = len(p[0])
		}
	}
	for _, p := range k.pairs {
		fmt.Fprintf(w, "  %s%s  %s\n", k.u.dim.Render(p[0]), strings.Repeat(" ", width-len(p[0])), p[1])
	}
}

// --- formatting helpers ------------------------------------------------------------

func jsonCompact(v any) ([]byte, error) { return json.Marshal(v) }

// printJSON writes v indented to stdout.
func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func securityLabel(s core.WifiSecurity) string {
	switch s {
	case core.SecOpen:
		return "open"
	case core.SecOWE:
		return "OWE"
	case core.SecWEP:
		return "WEP"
	case core.SecWPAPSK:
		return "WPA2"
	case core.SecSAE:
		return "WPA3"
	case core.SecWPAEAP:
		return "802.1X"
	case "":
		return "-"
	}
	return string(s)
}

// ago renders a time relative to now, coarsely ("3m ago", "2d ago"); "-" for zero.
func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours())/24)
}

// shortDuration renders 1h2m as "1h2m", 45s as "45s", 3d as "3d".
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

// ms renders a millisecond float; -1 (not measured) is "-".
func ms(v float64) string {
	if v < 0 {
		return "-"
	}
	if v < 10 {
		return fmt.Sprintf("%.1f ms", v)
	}
	return fmt.Sprintf("%.0f ms", v)
}

// pct renders a 0..1 ratio as a percentage.
func pct(v float64) string {
	if v < 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", v*100)
}

// bytesHuman renders a byte count in the usual units.
func bytesHuman(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

func mbps(v float64) string {
	if v <= 0 {
		return "-"
	}
	if v < 10 {
		return fmt.Sprintf("%.2f Mbps", v)
	}
	return fmt.Sprintf("%.1f Mbps", v)
}

// clock renders a time as HH:MM:SS, or with the date when not today.
func clock(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	t = t.Local()
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04:05")
	}
	return t.Format("2006-01-02 15:04")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func onOffWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// truncateANSI is truncate for cells that may carry colour codes.
func truncateANSI(s string, width int) string {
	return ansi.Truncate(s, width, "…")
}

// truncate shortens s to max runes with an ellipsis.
func truncate(s string, max int) string {
	if max <= 1 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func joinOrDash(ss []string) string {
	if len(ss) == 0 {
		return "-"
	}
	return strings.Join(ss, ", ")
}
