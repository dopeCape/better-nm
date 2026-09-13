package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dopeCape/better-nm/internal/core"
)

// speedTab runs a bandwidth test on demand (s full, q quick), shows one
// progress bar per phase fed by the client's progress callback, and lists the
// stored history below.
type speedTab struct {
	history []core.SpeedResult
	err     error
	loaded  bool

	running bool
	quick   bool
	phase   string
	pct     map[string]float64
	rate    map[string]float64
	ch      chan tea.Msg
	cancel  context.CancelFunc
	last    *core.SpeedResult
	lastErr error
}

type (
	speedProgressMsg struct{ p core.SpeedProgress }
	speedDoneMsg     struct {
		result core.SpeedResult
		err    error
	}
)

var speedPhases = []string{"latency", "download", "upload"}

func (t *speedTab) load(m *Model) tea.Cmd { return m.l.speedHistory() }

func (t *speedTab) capturing() bool { return false }

func (t *speedTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case speedHistoryMsg:
		t.loaded = true
		t.err = msg.err
		if msg.err == nil {
			t.history = msg.results
		}
	case speedProgressMsg:
		t.apply(msg.p)
		return waitSpeed(t.ch)
	case speedDoneMsg:
		t.running = false
		t.cancel = nil
		t.ch = nil
		if msg.err != nil {
			t.lastErr = msg.err
			m.setFlash("speed test: "+errText(msg.err), true)
			return m.flashTimer()
		}
		r := msg.result
		t.last = &r
		for _, p := range speedPhases {
			t.pct[p] = 100
		}
		return m.l.speedHistory()
	case tea.KeyMsg:
		switch msg.String() {
		case "s":
			return t.start(m, false)
		case "q":
			return t.start(m, true)
		case "esc", "x":
			if t.running && t.cancel != nil {
				t.cancel()
			}
		case "r":
			return m.l.speedHistory()
		}
	}
	return nil
}

func (t *speedTab) apply(p core.SpeedProgress) {
	if t.pct == nil {
		t.pct, t.rate = map[string]float64{}, map[string]float64{}
	}
	if p.Phase == "done" {
		for _, ph := range speedPhases {
			t.pct[ph] = 100
		}
		return
	}
	// every phase before the current one is complete
	for _, ph := range speedPhases {
		if ph == p.Phase {
			break
		}
		t.pct[ph] = 100
	}
	t.phase = p.Phase
	t.pct[p.Phase] = p.Percent
	if p.Mbps > 0 {
		t.rate[p.Phase] = p.Mbps
	}
}

// start runs the test in a goroutine; progress and the result arrive as
// messages through a channel the Cmd chain drains one item at a time.
func (t *speedTab) start(m *Model, quick bool) tea.Cmd {
	if t.running {
		return nil
	}
	t.running, t.quick, t.phase, t.lastErr = true, quick, "latency", nil
	t.pct = map[string]float64{}
	t.rate = map[string]float64{}
	ch := make(chan tea.Msg, 256)
	t.ch = ch
	ctx, cancel := context.WithCancel(m.l.ctx)
	t.cancel = cancel
	c := m.l.c
	go func() {
		defer close(ch)
		res, err := c.Speed(ctx, core.SpeedOptions{Quick: quick}, func(p core.SpeedProgress) {
			select {
			case ch <- speedProgressMsg{p}:
			default: // never block the stream reader; a dropped tick is redrawn by the next
			}
		})
		select {
		case ch <- speedDoneMsg{res, err}:
		case <-ctx.Done():
		}
	}()
	return tea.Batch(m.spin.Tick, waitSpeed(ch))
}

func waitSpeed(ch chan tea.Msg) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (t *speedTab) hints(m *Model) string {
	if t.running {
		return keyHints("esc", "cancel test")
	}
	return keyHints("s", "full test", "q", "quick test", "r", "reload history")
}

func (t *speedTab) view(m *Model, w, h int) string {
	title := stTitle.Render("Speed")
	if t.running {
		kind := "full"
		if t.quick {
			kind = "quick"
		}
		title += "  " + m.spin.View() + " " + stDim.Render(kind+" test running")
	}
	lines := []string{fit(title, w)}
	if t.err != nil {
		lines = append(lines, stBad.Render(truncate("error: "+errText(t.err), w)))
	}
	if t.running || t.last != nil || t.lastErr != nil {
		barW := min(w-30, 50)
		for _, ph := range speedPhases {
			p := t.pct[ph]
			label := pad(ph, 9)
			extra := ""
			if r, ok := t.rate[ph]; ok && r > 0 {
				extra = mbps(r)
			}
			if t.last != nil && !t.running {
				switch ph {
				case "latency":
					extra = fmt.Sprintf("%s (jitter %s)", ms(t.last.LatencyMs), ms(t.last.JitterMs))
				case "download":
					extra = mbps(t.last.DownloadMbps)
				case "upload":
					extra = mbps(t.last.UploadMbps)
				}
			}
			cur := ""
			if t.running && ph == t.phase {
				cur = m.spin.View()
			} else {
				cur = " "
			}
			lines = append(lines, fmt.Sprintf("%s %s %s %s", cur, label, progressBar(p, barW), extra))
		}
		if t.lastErr != nil {
			lines = append(lines, stBad.Render(truncate("✗ "+errText(t.lastErr), w)))
		}
		lines = append(lines, "")
	} else {
		lines = append(lines, stDim.Render("press s for a full test or q for a quick one; nothing runs in the background"), "")
	}
	lines = append(lines, stBold.Render("history"))
	if len(t.history) == 0 {
		msg := "no tests yet"
		if !t.loaded {
			msg = "loading…"
		}
		lines = append(lines, stDim.Render(msg))
		return strings.Join(lines, "\n")
	}
	cells := make([][]string, 0, len(t.history))
	// newest first
	for i := len(t.history) - 1; i >= 0; i-- {
		r := t.history[i]
		when := r.Time.Local().Format("Jan 02 15:04")
		kind := r.Provider
		if r.Quick {
			kind += " quick"
		}
		cells = append(cells, []string{when, kind, mbps(r.DownloadMbps), mbps(r.UploadMbps), ms(r.LatencyMs), r.NetworkKey})
	}
	avail := max(2, h-len(lines)-1)
	if len(cells) > avail {
		cells = cells[:avail]
	}
	lines = append(lines, table(w, []int{12, 16, 11, 11, 9, 0}, []string{"when", "provider", "down", "up", "latency", "network"}, cells, -1))
	return strings.Join(lines, "\n")
}
