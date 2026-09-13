package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
)

// scanWait bounds how long `wifi list --rescan` waits for fresh results.
const scanWait = 6 * time.Second

func (a *app) wifiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wifi",
		Short: "Wi-Fi networks: list, connect, disconnect, forget, on/off, saved",
	}
	cmd.AddCommand(a.wifiListCmd(), a.wifiConnectCmd(), a.wifiDisconnectCmd(), a.wifiForgetCmd(),
		a.wifiOnOffCmd("on", true), a.wifiOnOffCmd("off", false), a.wifiSavedCmd())
	return cmd
}

func (a *app) wifiListCmd() *cobra.Command {
	var rescan bool
	var device string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Wi-Fi networks in range",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			if rescan {
				if err := a.rescan(ctx, c, device); err != nil {
					return err
				}
			}
			nets, err := c.Wifi(ctx, device)
			if err != nil {
				return err
			}
			sort.SliceStable(nets, func(i, j int) bool {
				if nets[i].Active != nets[j].Active {
					return nets[i].Active
				}
				return nets[i].Strength > nets[j].Strength
			})
			if a.jsonOut {
				return a.printJSON(nonNil(nets))
			}
			if len(nets) == 0 {
				fmt.Fprintln(a.out, a.ui.dim.Render("no networks in range (try --rescan, or `bnm wifi on`)"))
				return nil
			}
			a.renderWifiTable(nets, device == "")
			return nil
		},
	}
	cmd.Flags().BoolVar(&rescan, "rescan", false, "request a scan and wait for fresh results")
	cmd.Flags().StringVar(&device, "device", "", "only this Wi-Fi device")
	return cmd
}

// rescan asks for a scan and waits (bounded) for the wifi change that follows.
func (a *app) rescan(ctx context.Context, c *client.Client, device string) error {
	wctx, cancel := context.WithTimeout(ctx, scanWait)
	defer cancel()
	stream, err := c.Events(wctx)
	if err != nil {
		return err
	}
	if err := c.ScanWifi(ctx, device); err != nil {
		return err
	}
	for {
		select {
		case <-wctx.Done():
			return nil // show what we have
		case item, ok := <-stream:
			if !ok {
				return nil
			}
			if item.Change != nil && item.Change.Kind == core.ChangeWifi {
				return nil
			}
		}
	}
}

func (a *app) renderWifiTable(nets []core.WifiNetwork, showDevice bool) {
	u := a.ui
	headers := []string{"", "SSID", "SIGNAL", "", "BAND", "SECURITY", ""}
	if showDevice {
		headers = append(headers, "DEVICE")
	}
	t := u.table(headers...).alignRight(3)
	multiDev := false
	if showDevice {
		first := ""
		for _, n := range nets {
			if first == "" {
				first = n.Device
			} else if n.Device != first {
				multiDev = true
			}
		}
	}
	for _, n := range nets {
		mark := " "
		note := ""
		switch {
		case n.Active:
			mark = u.dot("green")
			note = u.green.Render("connected")
		case n.Known:
			mark = u.dim.Render("✓")
			note = u.dim.Render("saved")
		}
		ssid := n.SSID
		if n.Hidden {
			ssid += u.dim.Render(" (hidden)")
		}
		band := fmt.Sprintf("%s GHz/%d", n.Band, n.Channel)
		if n.Band == "" {
			band = "-"
		}
		row := []string{mark, truncate(ssid, 32), u.bars(n.Strength), fmt.Sprintf("%d%%", n.Strength), band, securityLabel(n.Security), note}
		if showDevice {
			if multiDev {
				row = append(row, n.Device)
			} else {
				row = append(row, "")
			}
		}
		t.add(row...)
	}
	if showDevice && !multiDev {
		t.headers = t.headers[:len(t.headers)-1]
	}
	t.render(a.out)
}

func (a *app) wifiConnectCmd() *cobra.Command {
	var password, device string
	var ask, hidden bool
	cmd := &cobra.Command{
		Use:   "connect <ssid>",
		Short: "Join a Wi-Fi network (creates the profile when needed)",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ssid := args[0]
			c, err := a.client()
			if err != nil {
				return err
			}
			if password != "" && ask {
				return usagef("--password and --ask are mutually exclusive")
			}
			// Work out whether a password is needed before we hit NM, so the
			// prompt appears only when it makes sense.
			var seen *core.WifiNetwork
			if nets, err := c.Wifi(ctx, device); err == nil {
				for i := range nets {
					if nets[i].SSID == ssid {
						seen = &nets[i]
						break
					}
				}
			}
			needsSecret := seen != nil && !seen.Known && (seen.Security == core.SecWPAPSK || seen.Security == core.SecSAE || seen.Security == core.SecWEP)
			if password == "" && (ask || needsSecret) {
				if !ask && !a.stdinIsTerminal() {
					return core.Errorf(core.KindInvalid, "pass --password or --ask", "%q needs a password", ssid)
				}
				password, err = readPassword(a.in, a.errw, fmt.Sprintf("Password for %s: ", ssid))
				if err != nil {
					return err
				}
			}
			req := core.ConnectWifiRequest{SSID: ssid, Password: password, Hidden: hidden, Device: device}
			if err := c.ConnectWifi(ctx, req); err != nil {
				return err
			}
			a.done("%s Connected to %s", a.ui.dot("green"), ssid)
			return nil
		},
	}
	cmd.Flags().StringVar(&password, "password", "", "pre-shared key (prefer --ask; it stays out of the shell history)")
	cmd.Flags().BoolVar(&ask, "ask", false, "prompt for the password without echo")
	cmd.Flags().BoolVar(&hidden, "hidden", false, "the network does not broadcast its SSID")
	cmd.Flags().StringVar(&device, "device", "", "Wi-Fi device to use (default: the first)")
	return cmd
}

func (a *app) stdinIsTerminal() bool {
	if f, ok := a.in.(*os.File); ok {
		return isTerminal(f)
	}
	return false
}

func (a *app) wifiDisconnectCmd() *cobra.Command {
	var device string
	cmd := &cobra.Command{
		Use:   "disconnect",
		Short: "Disconnect the Wi-Fi device",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.DisconnectWifi(cmd.Context(), device); err != nil {
				return err
			}
			a.done("%s Wi-Fi disconnected", a.ui.dot(""))
			return nil
		},
	}
	cmd.Flags().StringVar(&device, "device", "", "Wi-Fi device (default: the first)")
	return cmd
}

func (a *app) wifiForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <ssid|uuid>",
		Short: "Delete a saved Wi-Fi profile",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := a.resolveProfile(ctx, args[0], func(p core.Profile) bool { return p.Type == core.ProfileWifi }, "Wi-Fi profile")
			if err != nil {
				return err
			}
			c, _ := a.client()
			if err := c.ForgetWifi(ctx, p.UUID); err != nil {
				return err
			}
			a.done("Forgot %s", p.Name)
			return nil
		},
	}
}

func (a *app) wifiOnOffCmd(name string, on bool) *cobra.Command {
	short := "Turn Wi-Fi on"
	if !on {
		short = "Turn Wi-Fi off"
	}
	return &cobra.Command{
		Use:   name,
		Short: short,
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.SetWifiEnabled(cmd.Context(), on); err != nil {
				return err
			}
			a.done("Wi-Fi %s", name)
			return nil
		},
	}
}

func (a *app) wifiSavedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "saved",
		Short: "List saved Wi-Fi profiles",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			profiles, err := c.Profiles(cmd.Context())
			if err != nil {
				return err
			}
			var wifi []core.Profile
			for _, p := range profiles {
				if p.Type == core.ProfileWifi {
					wifi = append(wifi, p)
				}
			}
			sort.SliceStable(wifi, func(i, j int) bool {
				if wifi[i].Active != wifi[j].Active {
					return wifi[i].Active
				}
				return wifi[i].Timestamp.After(wifi[j].Timestamp)
			})
			if a.jsonOut {
				return a.printJSON(nonNil(wifi))
			}
			if len(wifi) == 0 {
				fmt.Fprintln(a.out, a.ui.dim.Render("no saved Wi-Fi profiles"))
				return nil
			}
			u := a.ui
			t := u.table("", "SSID", "SECURITY", "AUTOCONNECT", "LAST USED", "UUID")
			for _, p := range wifi {
				mark := " "
				if p.Active {
					mark = u.dot("green")
				}
				ssid := p.SSID
				if ssid == "" {
					ssid = p.Name
				}
				if p.Name != ssid {
					ssid += u.dim.Render(" (" + p.Name + ")")
				}
				t.add(mark, truncate(ssid, 32), securityLabel(p.Security), yesNo(p.Autoconnect), ago(p.Timestamp), u.dim.Render(shortUUID(p.UUID)))
			}
			t.render(a.out)
			return nil
		},
	}
}

// shortUUID keeps the first block of a UUID; enough to resolve unambiguously.
func shortUUID(u string) string {
	if i := strings.IndexByte(u, '-'); i > 0 {
		return u[:i]
	}
	return u
}

// --- wired ------------------------------------------------------------------------------------------

func (a *app) wiredCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wired",
		Short: "Wired (ethernet) devices and profiles",
	}
	cmd.AddCommand(a.wiredListCmd(), a.wiredUpCmd(), a.wiredDownCmd())
	return cmd
}

func (a *app) wiredListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List ethernet devices and wired profiles",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			devs, err := c.Devices(ctx)
			if err != nil {
				return err
			}
			profiles, err := c.Profiles(ctx)
			if err != nil {
				return err
			}
			var eth []core.Device
			for _, d := range devs {
				if d.Kind == core.DeviceEthernet {
					eth = append(eth, d)
				}
			}
			var wired []core.Profile
			for _, p := range profiles {
				if p.Type == core.ProfileEthernet {
					wired = append(wired, p)
				}
			}
			if a.jsonOut {
				return a.printJSON(map[string]any{"devices": nonNil(eth), "profiles": nonNil(wired)})
			}
			u := a.ui
			if len(eth) == 0 {
				fmt.Fprintln(a.out, u.dim.Render("no ethernet devices"))
			} else {
				t := u.table("", "DEVICE", "STATE", "SPEED", "IPV4", "PROFILE")
				for _, d := range eth {
					speed := "-"
					if d.Speed > 0 {
						speed = fmt.Sprintf("%d Mb/s", d.Speed)
					}
					t.add(u.deviceDot(d.State), d.Name, string(d.State), speed, joinOrDash(d.IPv4), orDash(d.ActiveName))
				}
				t.render(a.out)
			}
			if len(wired) > 0 {
				fmt.Fprintln(a.out)
				t := u.table("", "PROFILE", "INTERFACE", "IPV4", "AUTOCONNECT", "UUID")
				for _, p := range wired {
					mark := " "
					if p.Active {
						mark = u.dot("green")
					}
					t.add(mark, p.Name, orDash(p.InterfaceName), string(p.IPv4.Method), yesNo(p.Autoconnect), u.dim.Render(shortUUID(p.UUID)))
				}
				t.render(a.out)
			}
			return nil
		},
	}
}

func (a *app) wiredUpCmd() *cobra.Command {
	var device string
	cmd := &cobra.Command{
		Use:   "up [profile]",
		Short: "Activate a wired profile (the only one, when not named)",
		Args:  a.rangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			isWired := func(p core.Profile) bool { return p.Type == core.ProfileEthernet }
			var p core.Profile
			if len(args) == 1 {
				p, err = a.resolveProfile(ctx, args[0], isWired, "wired profile")
				if err != nil {
					return err
				}
			} else {
				profiles, err := c.Profiles(ctx)
				if err != nil {
					return err
				}
				var wired []core.Profile
				for _, q := range profiles {
					if isWired(q) {
						wired = append(wired, q)
					}
				}
				switch len(wired) {
				case 0:
					return core.Errorf(core.KindNotFound, "plug in a cable; NetworkManager creates one automatically", "no wired profile")
				case 1:
					p = wired[0]
				default:
					names := make([]string, 0, len(wired))
					for _, q := range wired {
						names = append(names, q.Name)
					}
					return core.Errorf(core.KindInvalid, "name one: "+strings.Join(names, ", "), "%d wired profiles", len(wired))
				}
			}
			if err := c.ActivateProfile(ctx, p.UUID, device); err != nil {
				return err
			}
			a.done("%s %s up", a.ui.dot("green"), p.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&device, "device", "", "ethernet device to use (default: NM chooses)")
	return cmd
}

func (a *app) wiredDownCmd() *cobra.Command {
	var device string
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Disconnect the ethernet device",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			if device == "" {
				devs, err := c.Devices(ctx)
				if err != nil {
					return err
				}
				for _, d := range devs {
					if d.Kind == core.DeviceEthernet && (d.State == core.DeviceConnected || d.State == core.DeviceConnecting) {
						device = d.Name
						break
					}
				}
				if device == "" {
					return core.Errorf(core.KindConflict, "", "no ethernet device is connected")
				}
			}
			// The disconnect route takes any device name; only its default is Wi-Fi.
			if err := c.DisconnectWifi(ctx, device); err != nil {
				return err
			}
			a.done("%s %s down", a.ui.dot(""), device)
			return nil
		},
	}
	cmd.Flags().StringVar(&device, "device", "", "ethernet device (default: the connected one)")
	return cmd
}
