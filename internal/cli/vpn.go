package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
)

// vpnSettle bounds how long up/down wait for a VPN to leave "connecting".
const vpnSettle = 20 * time.Second

func (a *app) vpnCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vpn",
		Short: "VPNs: Tailscale, WireGuard and NetworkManager plugin VPNs",
	}
	cmd.AddCommand(a.vpnListCmd(), a.vpnUpCmd(), a.vpnDownCmd(), a.vpnToggleCmd(), a.vpnAddCmd(), a.vpnTSCmd())
	return cmd
}

func (a *app) vpnListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List VPNs with their state",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			vpns, err := c.VPNs(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(nonNil(vpns))
			}
			if len(vpns) == 0 {
				fmt.Fprintln(a.out, a.ui.dim.Render("no VPNs (bnm vpn add wg <file.conf>, bnm vpn add ovpn <file.ovpn>, or install tailscale)"))
				return nil
			}
			a.renderVPNTable(vpns)
			return nil
		},
	}
}

func (a *app) renderVPNTable(vpns []core.VPN) {
	u := a.ui
	t := u.table("", "NAME", "KIND", "BACKEND", "STATE", "DETAIL").shrinkCol(5)
	for _, v := range vpns {
		t.add(u.vpnDot(v.State), truncate(v.Name, 24), v.Kind, string(v.Backend), u.vpnState(v.State), vpnDetail(v))
	}
	t.render(a.out)
}

// vpnDetail is the one-line extra shown beside the state: where it goes (or
// is), then any error or hint the backend attached.
func vpnDetail(v core.VPN) string {
	var parts []string
	switch {
	case v.Tailscale != nil:
		ts := v.Tailscale
		if v.State == core.VPNConnected {
			if len(ts.SelfIPs) > 0 {
				parts = append(parts, ts.SelfIPs[0])
			}
			if ts.ExitNodeOn && ts.ExitNodeName != "" {
				parts = append(parts, "exit "+ts.ExitNodeName)
			}
		} else if ts.Tailnet != "" && v.State != core.VPNUnavailable {
			parts = append(parts, ts.Tailnet)
		}
	case v.WireGuard != nil:
		wg := v.WireGuard
		if len(wg.Peers) > 0 && wg.Peers[0].Endpoint != "" {
			parts = append(parts, wg.Peers[0].Endpoint)
		}
		if !wg.LastHandshake.IsZero() {
			parts = append(parts, "handshake "+ago(wg.LastHandshake))
		}
	case v.NMVPN != nil:
		if v.NMVPN.Gateway != "" {
			parts = append(parts, v.NMVPN.Gateway)
		}
	}
	if v.Error != "" {
		parts = append(parts, v.Error)
	} else if v.Detail != "" {
		parts = append(parts, v.Detail)
	}
	if v.AuthURL != "" {
		parts = append(parts, v.AuthURL)
	}
	return strings.Join(parts, " · ")
}

func (a *app) vpnUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up <name|id>",
		Short: "Connect a VPN",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.vpnSwitch(cmd.Context(), args[0], true)
		},
	}
}

func (a *app) vpnDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down <name|id>",
		Short: "Disconnect a VPN",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.vpnSwitch(cmd.Context(), args[0], false)
		},
	}
}

func (a *app) vpnToggleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "toggle <name|id>",
		Short: "Connect a VPN when it is down, disconnect it when it is up",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.resolveVPN(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			up := !(v.State == core.VPNConnected || v.State == core.VPNConnecting)
			return a.vpnSwitch(cmd.Context(), v.ID, up)
		},
	}
}

// vpnSwitch connects or disconnects, then waits (bounded) for the state to settle.
func (a *app) vpnSwitch(ctx context.Context, arg string, up bool) error {
	v, err := a.resolveVPN(ctx, arg)
	if err != nil {
		return err
	}
	c, _ := a.client()
	if up {
		err = c.ConnectVPN(ctx, v.ID)
	} else {
		err = c.DisconnectVPN(ctx, v.ID)
	}
	if err != nil {
		return err
	}
	final := a.waitVPN(ctx, c, v.ID, up)
	if a.jsonOut {
		if a.quiet {
			return nil
		}
		return a.printJSON(final)
	}
	if a.quiet {
		return nil
	}
	line := fmt.Sprintf("%s %s %s", a.ui.vpnDot(final.State), final.Name, a.ui.vpnState(final.State))
	if d := vpnDetail(final); d != "" {
		line += "  " + a.ui.dim.Render(d)
	}
	fmt.Fprintln(a.out, line)
	if final.State == core.VPNNeedsAuth && final.AuthURL != "" {
		fmt.Fprintf(a.out, "open %s to log in\n", final.AuthURL)
	}
	if up && final.State == core.VPNError {
		return core.Errorf(core.KindInternal, final.Detail, "%s failed: %s", final.Name, orDash(final.Error))
	}
	return nil
}

// waitVPN polls until the VPN leaves the transitional state or vpnSettle passes.
func (a *app) waitVPN(ctx context.Context, c *client.Client, id string, up bool) core.VPN {
	deadline := time.Now().Add(vpnSettle)
	var last core.VPN
	for {
		vpns, err := c.VPNs(ctx)
		if err == nil {
			for _, v := range vpns {
				if v.ID == id {
					last = v
				}
			}
		}
		settled := last.State != core.VPNConnecting
		if up {
			settled = settled && last.State != core.VPNDisconnected
		} else {
			settled = settled && last.State != core.VPNConnected
		}
		if settled || time.Now().After(deadline) || ctx.Err() != nil {
			return last
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (a *app) vpnAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Import a VPN from a file",
	}
	var name string
	wg := &cobra.Command{
		Use:   "wg <file.conf>",
		Short: "Import a wg-quick WireGuard configuration as an NM profile",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.vpnImport(cmd.Context(), "wireguard", args[0], name)
		},
	}
	wg.Flags().StringVar(&name, "name", "", "profile name (default: the file name without .conf)")
	ovpn := &cobra.Command{
		Use:   "ovpn <file.ovpn>",
		Short: "Import an OpenVPN configuration via the NM OpenVPN plugin",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.vpnImport(cmd.Context(), "openvpn", args[0], name)
		},
	}
	ovpn.Flags().StringVar(&name, "name", "", "profile name (default: the file name without .ovpn)")
	cmd.AddCommand(wg, ovpn)
	return cmd
}

func (a *app) vpnImport(ctx context.Context, kind, path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return core.Wrap(core.KindNotFound, "", err)
	}
	if name == "" {
		name = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".conf"), ".ovpn")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	// Send the content inline so a path relative to this shell works even when
	// the daemon runs elsewhere.
	res, err := c.ImportVPN(ctx, api.ImportVPNRequest{Kind: kind, Name: name, Content: string(data)})
	if err != nil {
		return err
	}
	if a.jsonOut {
		return a.printJSON(res)
	}
	if !a.quiet {
		fmt.Fprintf(a.out, "Imported %s as %s (%s)\n", filepath.Base(path), res.Name, res.Kind)
		fmt.Fprintf(a.out, "%s\n", a.ui.dim.Render("bnm vpn up "+res.Name))
	}
	return nil
}

// --- tailscale ---------------------------------------------------------------------------------------

func (a *app) vpnTSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ts",
		Aliases: []string{"tailscale"},
		Short:   "Tailscale: status, peers, exit node, login, DNS",
	}
	cmd.AddCommand(a.tsStatusCmd(), a.tsPeersCmd(), a.tsExitNodeCmd(), a.tsLoginCmd(), a.tsLogoutCmd(), a.tsDNSCmd())
	return cmd
}

// tailscaleVPN finds the Tailscale entry in the VPN list.
func (a *app) tailscaleVPN(ctx context.Context) (core.VPN, error) {
	c, err := a.client()
	if err != nil {
		return core.VPN{}, err
	}
	vpns, err := c.VPNs(ctx)
	if err != nil {
		return core.VPN{}, err
	}
	for _, v := range vpns {
		if v.Backend == core.BackendTailscale {
			return v, nil
		}
	}
	return core.VPN{}, core.Errorf(core.KindUnsupported, "install tailscale and start tailscaled", "no Tailscale backend")
}

func (a *app) tsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Tailscale node status",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.tailscaleVPN(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(v)
			}
			u := a.ui
			fmt.Fprintf(a.out, "%s %s %s\n", u.vpnDot(v.State), u.bold.Render("Tailscale"), u.vpnState(v.State))
			k := u.kv()
			if v.Detail != "" {
				k.add("Note", v.Detail)
			}
			if v.Error != "" {
				k.add("Error", u.red.Render(v.Error))
			}
			if v.AuthURL != "" {
				k.add("Login", v.AuthURL)
			}
			if ts := v.Tailscale; ts != nil {
				k.add("Backend", ts.BackendState)
				k.add("Version", ts.Version)
				k.add("Node", ts.SelfName)
				k.add("IPs", strings.Join(ts.SelfIPs, ", "))
				k.add("Tailnet", ts.Tailnet)
				k.add("MagicDNS", ts.MagicDNS)
				k.add("Control", ts.ControlURL)
				exit := u.dim.Render("off")
				if ts.ExitNodeOn {
					exit = u.green.Render(orDash(ts.ExitNodeName))
					if ts.AllowLAN {
						exit += u.dim.Render(" (LAN access allowed)")
					}
				} else if ts.ExitNodeName != "" {
					exit = ts.ExitNodeName + u.dim.Render(" (selected, off)")
				}
				k.add("Exit node", exit)
				k.add("Accept DNS", onOffWord(ts.AcceptDNS))
				k.add("Operator", yesNo(ts.OperatorOK))
				online := 0
				for _, p := range ts.Peers {
					if p.Online {
						online++
					}
				}
				k.add("Peers", fmt.Sprintf("%d online of %d", online, len(ts.Peers)))
				if len(ts.Health) > 0 {
					k.add("Health", u.yellow.Render(strings.Join(ts.Health, "; ")))
				}
			}
			k.render(a.out)
			return nil
		},
	}
}

func (a *app) tsPeersCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "peers",
		Short: "List tailnet peers",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.tailscaleVPN(cmd.Context())
			if err != nil {
				return err
			}
			var peers []core.TailscalePeer
			if v.Tailscale != nil {
				peers = v.Tailscale.Peers
			}
			if a.jsonOut {
				return a.printJSON(nonNil(peers))
			}
			sort.SliceStable(peers, func(i, j int) bool {
				if peers[i].Online != peers[j].Online {
					return peers[i].Online
				}
				return peers[i].Name < peers[j].Name
			})
			a.renderPeers(peers, all)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include offline peers not seen in 30 days")
	return cmd
}

func (a *app) renderPeers(peers []core.TailscalePeer, all bool) {
	u := a.ui
	if len(peers) == 0 {
		fmt.Fprintln(a.out, u.dim.Render("no peers"))
		return
	}
	t := u.table("", "NAME", "IP", "OS", "EXIT", "RELAY", "LAST SEEN")
	hidden := 0
	for _, p := range peers {
		if !all && !p.Online && !p.LastSeen.IsZero() && time.Since(p.LastSeen) > 30*24*time.Hour {
			hidden++
			continue
		}
		mark := u.dot("")
		if p.Online {
			mark = u.dot("green")
		}
		ip := "-"
		if len(p.IPs) > 0 {
			ip = p.IPs[0]
		}
		exit := ""
		switch {
		case p.ExitNode:
			exit = u.green.Render("in use")
		case p.ExitNodeOption:
			exit = "offers"
		}
		seen := "now"
		if !p.Online {
			seen = ago(p.LastSeen)
		}
		t.add(mark, truncate(p.Name, 28), ip, orDash(p.OS), exit, orDash(p.Relay), seen)
	}
	t.render(a.out)
	if hidden > 0 {
		fmt.Fprintln(a.out, u.dim.Render(fmt.Sprintf("%d peers not seen in 30 days hidden (--all)", hidden)))
	}
}

func (a *app) tsExitNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exit-node",
		Short: "Show, list, set or switch off the exit node",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.tailscaleVPN(cmd.Context())
			if err != nil {
				return err
			}
			ts := v.Tailscale
			if a.jsonOut {
				return a.printJSON(ts)
			}
			if ts == nil || !ts.ExitNodeOn {
				fmt.Fprintln(a.out, "exit node: off")
				return nil
			}
			line := "exit node: " + a.ui.green.Render(orDash(ts.ExitNodeName))
			if ts.AllowLAN {
				line += a.ui.dim.Render(" (LAN access allowed)")
			}
			fmt.Fprintln(a.out, line)
			return nil
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List peers that offer an exit node",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.tailscaleVPN(cmd.Context())
			if err != nil {
				return err
			}
			var offers []core.TailscalePeer
			if v.Tailscale != nil {
				for _, p := range v.Tailscale.Peers {
					if p.ExitNodeOption || p.ExitNode {
						offers = append(offers, p)
					}
				}
			}
			if a.jsonOut {
				return a.printJSON(nonNil(offers))
			}
			a.renderPeers(offers, true)
			return nil
		},
	}
	var allowLAN bool
	set := &cobra.Command{
		Use:   "set <peer>",
		Short: "Route through a peer (by name or ID)",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.SetTailscaleExitNode(cmd.Context(), args[0], allowLAN); err != nil {
				return err
			}
			a.done("exit node: %s", args[0])
			return nil
		},
	}
	set.Flags().BoolVar(&allowLAN, "allow-lan", false, "keep direct access to the local LAN")
	off := &cobra.Command{
		Use:   "off",
		Short: "Stop using an exit node",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.SetTailscaleExitNodeEnabled(cmd.Context(), false); err != nil {
				return err
			}
			a.done("exit node: off")
			return nil
		},
	}
	on := &cobra.Command{
		Use:   "on",
		Short: "Use the selected exit node again",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.SetTailscaleExitNodeEnabled(cmd.Context(), true); err != nil {
				return err
			}
			a.done("exit node: on")
			return nil
		},
	}
	cmd.AddCommand(list, set, off, on)
	return cmd
}

func (a *app) tsLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Start a Tailscale login and print the URL to open",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			url, err := c.TailscaleLogin(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(api.LoginResponse{URL: url})
			}
			if url == "" {
				fmt.Fprintln(a.out, "already logged in")
				return nil
			}
			fmt.Fprintf(a.out, "Open this URL to log in:\n  %s\n", url)
			return nil
		},
	}
}

func (a *app) tsLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Log this node out of the tailnet",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.TailscaleLogout(cmd.Context()); err != nil {
				return err
			}
			a.done("logged out")
			return nil
		},
	}
}

func (a *app) tsDNSCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dns on|off",
		Short: "Accept (or ignore) the tailnet's DNS configuration",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			on, err := a.onOff(cmd, args[0])
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.SetTailscaleAcceptDNS(cmd.Context(), on); err != nil {
				return err
			}
			a.done("accept DNS: %s", onOffWord(on))
			return nil
		},
	}
}
