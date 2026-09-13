package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dopeCape/better-nm/internal/core"
)

// monitorTab shows the baseline verdict, one row per anchor, and sparklines
// of the last sparkSamples RTTs for the gateway and the first public anchor.
type monitorTab struct {
	status  core.MonitorStatus
	err     error
	loaded  bool
	samples map[string][]core.Sample // anchor -> oldest first
}

const (
	sparkSamples    = 60
	learningSamples = 40 // the engine's default MinSamples
)

func (t *monitorTab) load(m *Model) tea.Cmd {
	cmds := []tea.Cmd{m.l.monitor(), m.l.samples("gateway", sparkSamples)}
	if a := t.publicAnchor(); a != "" {
		cmds = append(cmds, m.l.samples(a, sparkSamples))
	}
	return tea.Batch(cmds...)
}

func (t *monitorTab) capturing() bool { return false }

// publicAnchor is the first anchor that is not the gateway.
func (t *monitorTab) publicAnchor() string {
	for _, a := range t.status.Anchors {
		if a.Anchor != "gateway" {
			return a.Anchor
		}
	}
	return ""
}

func (t *monitorTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case monitorMsg:
		t.err = msg.err
		if msg.err != nil {
			return nil
		}
		hadPublic := t.publicAnchor()
		t.status = msg.st
		t.loaded = true
		// The first status told us the anchors; fetch the public sparkline now.
		if a := t.publicAnchor(); a != "" && hadPublic == "" && m.tab == tabMonitor {
			return m.l.samples(a, sparkSamples)
		}
	case samplesMsg:
		if msg.err == nil {
			if t.samples == nil {
				t.samples = map[string][]core.Sample{}
			}
			t.samples[msg.anchor] = msg.samples
		}
	case actionMsg:
		if msg.err == nil {
			return t.load(m)
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "p":
			if t.status.Paused {
				return m.l.action(int(tabMonitor), "resume", func(ctx context.Context) error { return m.l.c.ResumeMonitor(ctx) })
			}
			return m.l.action(int(tabMonitor), "pause", func(ctx context.Context) error { return m.l.c.PauseMonitor(ctx) })
		case "R":
			return m.l.action(int(tabMonitor), "reset baseline", func(ctx context.Context) error { return m.l.c.ResetBaseline(ctx, "") })
		case "r":
			return t.load(m)
		}
	}
	return nil
}

func (t *monitorTab) hints(m *Model) string {
	p := "pause"
	if t.status.Paused {
		p = "resume"
	}
	return keyHints("p", p, "R", "reset baseline", "r", "reload")
}

func (t *monitorTab) view(m *Model, w, h int) string {
	lines := []string{fit(stTitle.Render("Monitor")+"  "+t.banner(m), w)}
	if t.err != nil {
		lines = append(lines, stBad.Render(truncate("error: "+errText(t.err), w)))
	}
	if !t.loaded {
		lines = append(lines, stDim.Render("loading…"))
		return strings.Join(lines, "\n")
	}
	st := t.status
	if st.NetworkKey == "" && len(st.Anchors) == 0 {
		lines = append(lines, stDim.Render("not connected; nothing to probe"))
		return strings.Join(lines, "\n")
	}
	meta := []string{}
	if st.NetworkKey != "" {
		meta = append(meta, "network "+st.NetworkKey)
	}
	if st.Interval > 0 {
		meta = append(meta, "every "+st.Interval.String())
	}
	if !st.LastSample.IsZero() {
		meta = append(meta, "last sample "+ago(st.LastSample, m.now()))
	}
	if dns := t.dnsTime(); dns >= 0 {
		meta = append(meta, "dns "+ms(dns))
	}
	lines = append(lines, stDim.Render(truncate(strings.Join(meta, "  ·  "), w)))
	lines = append(lines, "")

	cells := make([][]string, 0, len(st.Anchors))
	for _, a := range st.Anchors {
		state := string(a.State)
		if a.State == core.BaselineLearning {
			state = fmt.Sprintf("learning %d/%d", min(a.SampleCount, learningSamples), learningSamples)
		}
		rtt := ms(a.CurrentRTT)
		if a.State != core.BaselineLearning && a.BaselineRTT > 0 {
			rtt += stDim.Render(" / " + ms(a.BaselineRTT))
		}
		loss := pct(a.CurrentLoss)
		if a.CurrentLoss > 0 {
			loss = stWarn.Render(loss)
		}
		cells = append(cells, []string{a.Anchor, stateStyle(string(a.State)).Render(state), rtt, loss})
	}
	if len(cells) > 0 {
		lines = append(lines, table(w, []int{18, 16, 22, 0}, []string{"anchor", "state", "rtt now / baseline", "loss"}, cells, -1))
	}
	lines = append(lines, "")
	sparkW := min(w-2, sparkSamples)
	gw := t.samples["gateway"]
	lines = append(lines, stBold.Render("gateway")+stDim.Render(fmt.Sprintf("  last %d samples%s", len(gw), t.rangeText(gw))))
	lines = append(lines, sparkline(gw, sparkW))
	if pa := t.publicAnchor(); pa != "" {
		ps := t.samples[pa]
		lines = append(lines, "", stBold.Render(pa)+stDim.Render(fmt.Sprintf("  last %d samples%s", len(ps), t.rangeText(ps))))
		rows := max(1, min(4, h-len(lines)-1))
		lines = append(lines, sparkTall(ps, sparkW, rows))
	}
	return strings.Join(lines, "\n")
}

// banner is the state summary: learning n/40, ok, degraded since …, paused.
func (t *monitorTab) banner(m *Model) string {
	st := t.status
	if st.Paused {
		return stWarn.Render("paused")
	}
	switch st.State {
	case core.BaselineLearning:
		n := 0
		for _, a := range st.Anchors {
			n = max(n, a.SampleCount)
		}
		return stWarn.Render(fmt.Sprintf("learning %d/%d", min(n, learningSamples), learningSamples))
	case core.BaselineOK:
		return stGood.Render("ok")
	case core.BaselineDegraded:
		since := ""
		for _, a := range st.Anchors {
			if a.State == core.BaselineDegraded && !a.Since.IsZero() {
				since = " since " + clock(a.Since) + " (" + ago(a.Since, m.now()) + ")"
				break
			}
		}
		return stBad.Render("degraded" + since)
	case core.BaselineIdle, "":
		return stDim.Render("idle")
	}
	return stDim.Render(string(st.State))
}

func (t *monitorTab) dnsTime() float64 {
	for _, a := range t.status.Anchors {
		if a.CurrentDNS > 0 {
			return a.CurrentDNS
		}
	}
	for _, s := range t.samples["gateway"] {
		if s.DNSms >= 0 {
			return s.DNSms
		}
	}
	return -1
}

func (t *monitorTab) rangeText(s []core.Sample) string {
	lo, hi := -1.0, -1.0
	for _, x := range s {
		if x.RTTms < 0 {
			continue
		}
		if lo < 0 || x.RTTms < lo {
			lo = x.RTTms
		}
		if x.RTTms > hi {
			hi = x.RTTms
		}
	}
	if lo < 0 {
		return ""
	}
	return fmt.Sprintf("  %s – %s", ms(lo), ms(hi))
}
