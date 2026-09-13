package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/dopeCape/better-nm/internal/core"
)

// Text helpers shared by every tab: width-aware truncation and padding, signal
// bars, sparklines and the small formatters for durations and rates.

const ellipsis = "…"

// truncate cuts s to at most w cells, ending in … when it had to cut. It is
// ANSI-aware, so styled text may be passed.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if w == 1 {
		return ellipsis
	}
	return ansi.Truncate(s, w, ellipsis)
}

// pad right-pads (or truncates) s to exactly w cells.
func pad(s string, w int) string {
	s = truncate(s, w)
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// padLeft left-pads (or truncates) s to exactly w cells.
func padLeft(s string, w int) string {
	s = truncate(s, w)
	if n := w - lipgloss.Width(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// fit forces a rendered line to exactly w cells (styled text included).
func fit(s string, w int) string {
	if lipgloss.Width(s) > w {
		s = truncate(s, w)
	}
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

// clampLines returns at most n lines of s, padding with blank lines.
func clampLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// clampBox forces s into exactly w x h cells: lines truncated and padded,
// missing lines added.
func clampBox(s string, w, h int) string {
	ls := strings.Split(s, "\n")
	if len(ls) > h {
		ls = ls[:h]
	}
	for len(ls) < h {
		ls = append(ls, "")
	}
	for i := range ls {
		ls[i] = fit(ls[i], w)
	}
	return strings.Join(ls, "\n")
}

// signalBars renders a 0-100 strength as up to four rising bars.
func signalBars(strength uint8) string {
	glyphs := []string{"▂", "▄", "▆", "█"}
	n := 0
	switch {
	case strength >= 80:
		n = 4
	case strength >= 55:
		n = 3
	case strength >= 30:
		n = 2
	case strength > 0:
		n = 1
	}
	var b strings.Builder
	for i := 0; i < 4; i++ {
		if i < n {
			b.WriteString(glyphs[i])
		} else {
			b.WriteString(" ")
		}
	}
	return b.String()
}

// signalStyle colours a strength.
func signalStyle(strength uint8) lipgloss.Style {
	switch {
	case strength >= 55:
		return stGood
	case strength >= 30:
		return stWarn
	default:
		return stBad
	}
}

// sparkline draws the RTTs of samples (oldest first) in w cells using block
// characters; lost samples show as ×. The scale is the max RTT seen.
func sparkline(samples []core.Sample, w int) string {
	if w <= 0 {
		return ""
	}
	if len(samples) > w {
		samples = samples[len(samples)-w:]
	}
	maxRTT := 0.0
	for _, s := range samples {
		if s.RTTms > maxRTT {
			maxRTT = s.RTTms
		}
	}
	levels := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	var b strings.Builder
	for i := 0; i < w-len(samples); i++ {
		b.WriteString(" ")
	}
	for _, s := range samples {
		switch {
		case s.RTTms < 0:
			b.WriteString(stBad.Render("×"))
		case maxRTT <= 0:
			b.WriteString(levels[0])
		default:
			idx := int(s.RTTms / maxRTT * float64(len(levels)-1))
			idx = min(max(idx, 0), len(levels)-1)
			st := stGood
			if s.Loss > 0 {
				st = stWarn
			}
			b.WriteString(st.Render(levels[idx]))
		}
	}
	return b.String()
}

// sparkTall is sparkline over rows lines: each column is one sample, filled
// from the bottom up, so a rise in RTT reads as a taller bar.
func sparkTall(samples []core.Sample, w, rows int) string {
	if w <= 0 || rows <= 0 {
		return ""
	}
	if rows == 1 {
		return sparkline(samples, w)
	}
	if len(samples) > w {
		samples = samples[len(samples)-w:]
	}
	maxRTT := 0.0
	for _, s := range samples {
		if s.RTTms > maxRTT {
			maxRTT = s.RTTms
		}
	}
	levels := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	steps := rows * len(levels)
	lines := make([]strings.Builder, rows)
	for r := range lines {
		for i := 0; i < w-len(samples); i++ {
			lines[r].WriteString(" ")
		}
	}
	for _, s := range samples {
		lvl := 1
		if s.RTTms >= 0 && maxRTT > 0 {
			lvl = int(s.RTTms/maxRTT*float64(steps-1)) + 1
		}
		st := stGood
		if s.Loss > 0 {
			st = stWarn
		}
		for r := 0; r < rows; r++ {
			row := rows - 1 - r // 0 is the top line
			base := r * len(levels)
			switch {
			case s.RTTms < 0:
				if r == 0 {
					lines[row].WriteString(stBad.Render("×"))
				} else {
					lines[row].WriteString(" ")
				}
			case lvl >= base+len(levels):
				lines[row].WriteString(st.Render(levels[len(levels)-1]))
			case lvl > base:
				lines[row].WriteString(st.Render(levels[lvl-base-1]))
			default:
				lines[row].WriteString(" ")
			}
		}
	}
	out := make([]string, rows)
	for i := range lines {
		out[i] = lines[i].String()
	}
	return strings.Join(out, "\n")
}

// progressBar draws a w-cell bar for pct (0-100).
func progressBar(pct float64, w int) string {
	if w <= 0 {
		return ""
	}
	pct = min(max(pct, 0), 100)
	filled := int(pct/100*float64(w) + 0.5)
	return stAccent.Render(strings.Repeat("█", filled)) + stDim.Render(strings.Repeat("░", w-filled))
}

// ms formats a millisecond value; negative means lost / unknown.
func ms(v float64) string {
	if v < 0 {
		return "—"
	}
	if v >= 10 {
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.1f ms", v)
}

// pct formats a 0..1 loss ratio.
func pct(v float64) string {
	return fmt.Sprintf("%.0f%%", v*100)
}

// mbps formats a rate.
func mbps(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("%.0f Mbps", v)
	}
	return fmt.Sprintf("%.1f Mbps", v)
}

// ago formats how long ago t was, coarsely.
func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// clock formats a wall time for the footer and the event log.
func clock(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.Local().Format("15:04:05")
}

// firstIP returns the first address of a list without its prefix length.
func firstIP(cidrs []string) string {
	if len(cidrs) == 0 {
		return ""
	}
	ip, _, _ := strings.Cut(cidrs[0], "/")
	return ip
}

// joinIPs lists addresses without prefix lengths.
func joinIPs(cidrs []string) string {
	out := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		ip, _, _ := strings.Cut(c, "/")
		out = append(out, ip)
	}
	return strings.Join(out, " ")
}

// securityLabel is the short text for a Wi-Fi security family.
func securityLabel(s core.WifiSecurity) string {
	switch s {
	case core.SecOpen:
		return "open"
	case core.SecOWE:
		return "owe"
	case core.SecWEP:
		return "wep"
	case core.SecWPAPSK:
		return "wpa2"
	case core.SecSAE:
		return "wpa3"
	case core.SecWPAEAP:
		return "eap"
	}
	return string(s)
}

// secured says whether joining s needs a secret.
func secured(s core.WifiSecurity) bool {
	return s != core.SecOpen && s != core.SecOWE && s != ""
}

// errText renders an error with its hint, if any.
func errText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if h := core.HintOf(err); h != "" && !strings.Contains(msg, h) {
		msg += " — " + h
	}
	return msg
}

// keyHints joins key/label pairs for the footer.
func keyHints(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, stKey.Render(pairs[i])+" "+stDim.Render(pairs[i+1]))
	}
	return strings.Join(parts, "  ")
}

// table renders header + rows with column widths; the first zero width (else
// the last column) takes the slack. Every cell is truncated to its width and
// the cursor row carries a › gutter marker.
func table(width int, widths []int, header []string, rows [][]string, cursor int) string {
	if width <= 3 || len(widths) == 0 {
		return ""
	}
	flex := -1
	fixed := 0
	for i, w := range widths {
		if w == 0 && flex < 0 {
			flex = i
			continue
		}
		fixed += w
	}
	if flex < 0 {
		flex = len(widths) - 1
		fixed -= widths[flex]
	}
	const gutter = 2
	gaps := len(widths) - 1
	ws := append([]int(nil), widths...)
	ws[flex] = max(4, width-gutter-fixed-gaps)
	line := func(cells []string, st lipgloss.Style, g string) string {
		parts := make([]string, len(ws))
		for i := range ws {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			parts[i] = st.Render(pad(c, ws[i]))
		}
		return fit(g+strings.Join(parts, " "), width)
	}
	var b strings.Builder
	if header != nil {
		b.WriteString(line(header, stHeader, "  "))
	}
	for i, r := range rows {
		if header != nil || i > 0 {
			b.WriteString("\n")
		}
		if i == cursor {
			b.WriteString(line(r, stSelected, stAccent.Render("› ")))
		} else {
			b.WriteString(line(r, stText, "  "))
		}
	}
	return b.String()
}
