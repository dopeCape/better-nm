package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/core"
)

// statusJSON is what `bnm status --json` prints: everything the screen shows.
type statusJSON struct {
	Status  api.StatusResponse `json:"status"`
	VPNs    []core.VPN         `json:"vpns"`
	Monitor core.MonitorStatus `json:"monitor"`
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "One-screen summary: connection, internet, VPNs, monitor, daemon",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			st, err := c.Status(ctx)
			if err != nil {
				return err
			}
			vpns, err := c.VPNs(ctx)
			if err != nil {
				return err
			}
			mon, err := c.Monitor(ctx)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(statusJSON{Status: st, VPNs: nonNil(vpns), Monitor: mon})
			}
			var wifi *core.WifiNetwork
			if st.Primary != nil && st.Primary.Type == core.ProfileWifi {
				if nets, err := c.Wifi(ctx, ""); err == nil {
					for i := range nets {
						if nets[i].Active && nets[i].ProfileUUID == st.Primary.ProfileUUID {
							wifi = &nets[i]
							break
						}
					}
				}
			}
			a.renderStatus(st, vpns, mon, wifi)
			return nil
		},
	}
}

func (a *app) renderStatus(st api.StatusResponse, vpns []core.VPN, mon core.MonitorStatus, wifi *core.WifiNetwork) {
	u := a.ui
	out := a.out
	p := st.Primary
	switch {
	case p == nil && !st.Networking:
		fmt.Fprintf(out, "%s %s\n", u.dot("red"), u.bold.Render("Networking is off"))
	case p == nil:
		fmt.Fprintf(out, "%s %s\n", u.dot(""), u.bold.Render("Not connected"))
		if !st.WifiEnabled && st.WifiHardware {
			fmt.Fprintf(out, "  %s\n", u.dim.Render("Wi-Fi is off (bnm wifi on)"))
		}
	default:
		head := fmt.Sprintf("Connected to %s", p.ProfileName)
		via := string(p.Type)
		if len(p.Devices) > 0 {
			via += " via " + strings.Join(p.Devices, ", ")
		}
		dot := u.dot("green")
		if p.State == core.ActiveActivating {
			dot = u.dot("yellow")
			head = fmt.Sprintf("Connecting to %s", p.ProfileName)
		}
		fmt.Fprintf(out, "%s %s %s\n", dot, u.bold.Render(head), u.dim.Render("("+via+")"))
	}

	k := u.kv()
	if p != nil {
		if wifi != nil {
			k.add("Wi-Fi", fmt.Sprintf("%s %d%%  %s GHz ch %d  %s", u.bars(wifi.Strength), wifi.Strength, wifi.Band, wifi.Channel, securityLabel(wifi.Security)))
		}
		k.add("IPv4", joinOrDash(p.IPv4))
		if len(p.IPv6) > 0 {
			k.add("IPv6", strings.Join(p.IPv6, ", "))
		}
		k.add("Gateway", orDash(p.Gateway4))
		k.add("DNS", joinOrDash(p.DNS))
	}
	k.add("Internet", u.connectivity(st.Connectivity))

	var up, other []string
	for _, v := range vpns {
		switch v.State {
		case core.VPNConnected:
			up = append(up, v.Name)
		case core.VPNConnecting, core.VPNNeedsAuth, core.VPNError:
			other = append(other, v.Name+" "+string(v.State))
		}
	}
	vpnLine := u.dim.Render("none up")
	if len(up) > 0 {
		vpnLine = u.green.Render(strings.Join(up, ", "))
	}
	if len(other) > 0 {
		vpnLine += "  " + u.yellow.Render(strings.Join(other, ", "))
	}
	if len(vpns) > 0 {
		k.add("VPN", vpnLine)
	}

	monLine := u.baselineState(mon.State)
	if mon.Paused {
		monLine = u.dim.Render("paused")
	} else {
		var parts []string
		for _, b := range mon.Anchors {
			if b.State == core.BaselineIdle || b.SampleCount == 0 || b.CurrentRTT == 0 {
				continue
			}
			s := fmt.Sprintf("%s %s", b.Anchor, ms(b.CurrentRTT))
			if b.CurrentLoss > 0 {
				s += " " + pct(b.CurrentLoss) + " loss"
			}
			parts = append(parts, s)
		}
		if len(parts) > 0 {
			monLine += "  " + u.dim.Render(strings.Join(parts, " · "))
		}
	}
	k.add("Monitor", monLine)

	daemon := fmt.Sprintf("bnmd %s  up %s  NM %s", st.Version, shortDuration(time.Duration(st.UptimeSeconds)*time.Second), orDash(st.NMVersion))
	k.add("Daemon", u.dim.Render(daemon))
	k.render(out)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (a *app) devicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List network interfaces",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			devs, err := c.Devices(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(nonNil(devs))
			}
			u := a.ui
			t := u.table("", "DEVICE", "KIND", "STATE", "IPV4", "PROFILE", "OWNER")
			add := func(d core.Device) {
				owner := d.Owner
				if d.Class == core.ClassInfra && owner == "" {
					owner = "virtual"
				}
				t.add(u.deviceDot(d.State), d.Name, string(d.Kind), string(d.State), joinOrDash(d.IPv4), orDash(d.ActiveName), orDash(owner))
			}
			for _, d := range devs {
				if d.Class == core.ClassPhysical {
					add(d)
				}
			}
			for _, d := range devs {
				if d.Class != core.ClassPhysical && d.Class != core.ClassLoopback {
					add(d)
				}
			}
			t.render(a.out)
			return nil
		},
	}
}
