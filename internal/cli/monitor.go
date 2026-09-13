package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dopeCape/better-nm/internal/core"
)

func (a *app) monitorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Background probes: state, history, baseline, pause/resume",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.monitorStatus(cmd)
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Current network key, verdict and per-anchor baselines",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.monitorStatus(cmd)
		},
	}
	cmd.AddCommand(status, a.monitorHistoryCmd(), a.monitorBaselineCmd(), a.monitorPauseCmd(true), a.monitorPauseCmd(false))
	return cmd
}

func (a *app) monitorStatus(cmd *cobra.Command) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	mon, err := c.Monitor(cmd.Context())
	if err != nil {
		return err
	}
	if a.jsonOut {
		return a.printJSON(mon)
	}
	u := a.ui
	head := u.baselineState(mon.State)
	if mon.Paused {
		head = u.yellow.Render("paused")
	}
	key := mon.NetworkKey
	if key == "" {
		key = u.dim.Render("(no network)")
	}
	fmt.Fprintf(a.out, "%s %s  %s\n", u.baselineDot(mon.State), u.bold.Render(key), head)
	k := u.kv()
	if mon.Interval > 0 {
		k.add("Interval", mon.Interval.String())
	}
	if !mon.LastSample.IsZero() {
		k.add("Last sample", ago(mon.LastSample))
	}
	k.render(a.out)
	if len(mon.Anchors) > 0 {
		fmt.Fprintln(a.out)
		a.renderBaselines(mon.Anchors)
	}
	return nil
}

func (a *app) renderBaselines(bs []core.Baseline) {
	u := a.ui
	t := u.table("", "ANCHOR", "STATE", "RTT", "BASELINE", "LOSS", "DNS", "SAMPLES").alignRight(3, 4, 5, 6, 7)
	for _, b := range bs {
		base, rtt, dns, loss := "-", "-", "-", "-"
		if b.BaselineRTT > 0 {
			base = ms(b.BaselineRTT)
		}
		if b.SampleCount > 0 {
			rtt, loss = ms(b.CurrentRTT), pct(b.CurrentLoss)
			if b.CurrentDNS > 0 {
				dns = ms(b.CurrentDNS)
			}
			if b.CurrentRTT < 0 {
				rtt = u.red.Render("lost")
			}
		}
		t.add(u.baselineDot(b.State), b.Anchor, u.baselineState(b.State), rtt, base, loss, dns, fmt.Sprint(b.SampleCount))
	}
	t.render(a.out)
}

func (a *app) monitorHistoryCmd() *cobra.Command {
	var anchor, key string
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Recent probe samples, newest last",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			samples, err := c.Samples(cmd.Context(), key, anchor, limit)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(nonNil(samples))
			}
			if len(samples) == 0 {
				fmt.Fprintln(a.out, a.ui.dim.Render("no samples yet"))
				return nil
			}
			u := a.ui
			t := u.table("TIME", "ANCHOR", "RTT", "LOSS", "DNS", "VIA").alignRight(2, 3, 4)
			for _, s := range samples {
				rtt := ms(s.RTTms)
				if s.RTTms < 0 {
					rtt = u.red.Render("lost")
				}
				loss := pct(s.Loss)
				if s.Loss > 0 {
					loss = u.yellow.Render(loss)
				}
				t.add(clock(s.Time), s.Anchor, rtt, loss, ms(s.DNSms), s.Method)
			}
			t.render(a.out)
			return nil
		},
	}
	cmd.Flags().StringVar(&anchor, "anchor", "", "only this anchor (gateway, 1.1.1.1, ...)")
	cmd.Flags().StringVar(&key, "key", "", "network key (default: the current network)")
	cmd.Flags().IntVar(&limit, "limit", 40, "number of samples")
	return cmd
}

func (a *app) monitorBaselineCmd() *cobra.Command {
	var reset bool
	var key string
	cmd := &cobra.Command{
		Use:   "baseline",
		Short: "Show the learned baselines, or forget them with --reset",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if reset {
				if err := c.ResetBaseline(cmd.Context(), key); err != nil {
					return err
				}
				what := key
				if what == "" {
					what = "the current network"
				}
				a.done("Baseline reset for %s; learning starts over", what)
				return nil
			}
			mon, err := c.Monitor(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(nonNil(mon.Anchors))
			}
			if len(mon.Anchors) == 0 {
				fmt.Fprintln(a.out, a.ui.dim.Render("no baseline yet"))
				return nil
			}
			a.renderBaselines(mon.Anchors)
			return nil
		},
	}
	cmd.Flags().BoolVar(&reset, "reset", false, "forget the baseline and start learning again")
	cmd.Flags().StringVar(&key, "key", "", "network key (default: the current network)")
	return cmd
}

func (a *app) monitorPauseCmd(pause bool) *cobra.Command {
	use, short := "pause", "Stop probing until resumed"
	if !pause {
		use, short = "resume", "Start probing again"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if pause {
				err = c.PauseMonitor(cmd.Context())
			} else {
				err = c.ResumeMonitor(cmd.Context())
			}
			if err != nil {
				return err
			}
			a.done("monitor %sd", use)
			return nil
		},
	}
}

// --- speed ------------------------------------------------------------------------------------------

func (a *app) speedCmd() *cobra.Command {
	var quick bool
	var provider, server string
	cmd := &cobra.Command{
		Use:   "speed",
		Short: "Run a bandwidth test (never runs in the background)",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			opts := core.SpeedOptions{Provider: provider, Server: server, Quick: quick}
			prog := newProgress(a)
			res, err := c.Speed(cmd.Context(), opts, prog.update)
			prog.finish()
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(res)
			}
			a.renderSpeedResult(res)
			return nil
		},
	}
	cmd.Flags().BoolVar(&quick, "quick", false, "move about a tenth of the bytes")
	cmd.Flags().StringVar(&provider, "provider", "", "cloudflare | iperf3 | librespeed (default: config speed.provider)")
	cmd.Flags().StringVar(&server, "server", "", "iperf3 host[:port] or librespeed base URL")
	cmd.AddCommand(a.speedHistoryCmd())
	return cmd
}

// progress renders one live line per phase on a terminal, one line per phase
// change otherwise, and nothing with --json or --quiet.
type progress struct {
	a       *app
	live    bool
	silent  bool
	phase   string
	started time.Time
	dirty   bool
}

func newProgress(a *app) *progress {
	return &progress{a: a, live: a.ui.isTTY, silent: a.jsonOut || a.quiet, started: time.Now()}
}

func (p *progress) update(sp core.SpeedProgress) {
	if p.silent {
		return
	}
	u := p.a.ui
	label := sp.Phase
	switch sp.Phase {
	case "latency":
		label = "latency "
	case "download":
		label = "download"
	case "upload":
		label = "upload  "
	case "done":
		label = "done    "
	}
	bar := progressBar(sp.Percent, 24)
	rate := ""
	if sp.Mbps > 0 {
		rate = mbps(sp.Mbps)
	}
	line := strings.TrimRight(fmt.Sprintf("%s %s %3.0f%%  %s", u.cyan.Render(label), bar, sp.Percent, rate), " ")
	if p.live {
		fmt.Fprintf(p.a.out, "\r\x1b[2K%s", line)
		p.dirty = true
		return
	}
	if sp.Phase != p.phase {
		p.phase = sp.Phase
		fmt.Fprintln(p.a.out, line)
	}
}

func (p *progress) finish() {
	if p.dirty {
		fmt.Fprint(p.a.out, "\r\x1b[2K")
	}
}

func progressBar(percent float64, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	fill := int(percent / 100 * float64(width))
	return "[" + repeatRune('█', fill) + repeatRune('░', width-fill) + "]"
}

func repeatRune(r rune, n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]rune, n)
	for i := range b {
		b[i] = r
	}
	return string(b)
}

func (a *app) renderSpeedResult(r core.SpeedResult) {
	u := a.ui
	head := fmt.Sprintf("Speed test via %s", orDash(r.Provider))
	if r.Server != "" {
		head += " (" + r.Server + ")"
	}
	if r.Quick {
		head += u.dim.Render(" quick")
	}
	fmt.Fprintln(a.out, u.bold.Render(head))
	k := u.kv()
	k.add("Download", u.green.Render(mbps(r.DownloadMbps)))
	k.add("Upload", u.green.Render(mbps(r.UploadMbps)))
	lat := ms(r.LatencyMs)
	if r.JitterMs > 0 {
		lat += u.dim.Render(fmt.Sprintf("  jitter %s", ms(r.JitterMs)))
	}
	k.add("Latency", lat)
	if r.BytesMoved > 0 {
		k.add("Moved", fmt.Sprintf("%s in %s", bytesHuman(r.BytesMoved), r.Duration.Round(100*time.Millisecond)))
	}
	k.add("Network", r.NetworkKey)
	k.render(a.out)
}

func (a *app) speedHistoryCmd() *cobra.Command {
	var key string
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Past speed tests, newest last",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			rs, err := c.SpeedHistory(cmd.Context(), key, limit)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(nonNil(rs))
			}
			if len(rs) == 0 {
				fmt.Fprintln(a.out, a.ui.dim.Render("no speed tests yet (bnm speed)"))
				return nil
			}
			t := a.ui.table("TIME", "NETWORK", "DOWN", "UP", "LATENCY", "VIA").alignRight(2, 3, 4)
			for _, r := range rs {
				via := r.Provider
				if r.Quick {
					via += " (quick)"
				}
				t.add(clock(r.Time), truncate(orDash(r.NetworkKey), 24), mbps(r.DownloadMbps), mbps(r.UploadMbps), ms(r.LatencyMs), via)
			}
			t.render(a.out)
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "only this network key")
	cmd.Flags().IntVar(&limit, "limit", 20, "number of results")
	return cmd
}

// --- events -----------------------------------------------------------------------------------------

func (a *app) eventsCmd() *cobra.Command {
	var follow, changes bool
	var limit int
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Event history, or follow the live stream with --follow",
		Long: `Without --follow: the stored events, oldest first (--limit N).
With --follow: print events as the daemon emits them until interrupted;
--limit N then stops after N items and --changes also shows the coarse
change hints (status, devices, wifi, profiles, active, vpn, monitor).
With --json each item is one JSON line.`,
		Args: a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if !follow {
				evs, err := c.EventHistory(ctx, limit)
				if err != nil {
					return err
				}
				if a.jsonOut {
					return a.printJSON(nonNil(evs))
				}
				if len(evs) == 0 {
					fmt.Fprintln(a.out, a.ui.dim.Render("no events yet"))
					return nil
				}
				t := a.ui.table("TIME", "TYPE", "NETWORK", "WHAT").shrinkCol(3)
				for _, e := range evs {
					t.add(clock(e.Time), a.eventType(e.Type), truncate(orDash(e.NetworkKey), 20), eventText(e))
				}
				t.render(a.out)
				return nil
			}
			stream, err := c.Events(ctx)
			if err != nil {
				return err
			}
			n := 0
			for item := range stream {
				if item.Change != nil && !changes {
					continue
				}
				if a.jsonOut {
					if err := a.printJSONLine(item); err != nil {
						return err
					}
				} else if item.Event != nil {
					e := item.Event
					fmt.Fprintf(a.out, "%s  %s  %s  %s\n", clock(e.Time), a.eventType(e.Type), a.ui.dim.Render(orDash(e.NetworkKey)), eventText(*e))
				} else if item.Change != nil {
					fmt.Fprintf(a.out, "%s  %s  %s\n", clock(time.Now()), a.ui.dim.Render("change:"+string(item.Change.Kind)), a.ui.dim.Render(item.Change.Path))
				}
				n++
				if limit > 0 && n >= limit {
					return nil
				}
			}
			if ctx.Err() != nil {
				return nil // interrupted: not an error
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream live events instead of history")
	cmd.Flags().BoolVar(&changes, "changes", false, "with --follow: also print change hints")
	cmd.Flags().IntVar(&limit, "limit", 0, "history: number of events (default 100); follow: stop after N items")
	return cmd
}

func (a *app) printJSONLine(v any) error {
	data, err := jsonCompact(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.out, string(data))
	return err
}

func (a *app) eventType(t core.EventType) string {
	u := a.ui
	switch t {
	case core.EventConnected, core.EventInternetRestored, core.EventVPNUp, core.EventRecovered:
		return u.green.Render(string(t))
	case core.EventDisconnected, core.EventNoInternet, core.EventVPNDown:
		return u.red.Render(string(t))
	case core.EventDegraded:
		return u.yellow.Render(string(t))
	}
	return u.dim.Render(string(t))
}

func eventText(e core.Event) string {
	if e.Body != "" {
		return e.Title + " — " + e.Body
	}
	return e.Title
}
