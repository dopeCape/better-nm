package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dopeCape/better-nm/internal/core"
)

func (a *app) profileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Saved profiles: list, show, IP settings, autoconnect, delete",
	}
	cmd.AddCommand(a.profileListCmd(), a.profileShowCmd(), a.profileIPCmd(), a.profileAutoconnectCmd(), a.profileDeleteCmd())
	return cmd
}

func (a *app) profileListCmd() *cobra.Command {
	var typ string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every saved profile",
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
			if typ != "" {
				var keep []core.Profile
				for _, p := range profiles {
					if string(p.Type) == typ || p.RawType == typ {
						keep = append(keep, p)
					}
				}
				profiles = keep
			}
			sort.SliceStable(profiles, func(i, j int) bool {
				if profiles[i].Active != profiles[j].Active {
					return profiles[i].Active
				}
				if profiles[i].Type != profiles[j].Type {
					return profiles[i].Type < profiles[j].Type
				}
				return strings.ToLower(profiles[i].Name) < strings.ToLower(profiles[j].Name)
			})
			if a.jsonOut {
				return a.printJSON(nonNil(profiles))
			}
			if len(profiles) == 0 {
				fmt.Fprintln(a.out, a.ui.dim.Render("no profiles"))
				return nil
			}
			u := a.ui
			t := u.table("", "NAME", "TYPE", "INTERFACE", "AUTO", "LAST USED", "UUID")
			for _, p := range profiles {
				mark := " "
				if p.Active {
					mark = u.dot("green")
				}
				typ := string(p.Type)
				if p.Type == core.ProfileVPN {
					typ = core.VPNKindForServiceType(p.VPNServiceType)
				}
				t.add(mark, truncate(p.Name, 30), typ, orDash(p.InterfaceName), yesNo(p.Autoconnect), ago(p.Timestamp), u.dim.Render(shortUUID(p.UUID)))
			}
			t.render(a.out)
			return nil
		},
	}
	cmd.Flags().StringVar(&typ, "type", "", "only this type (wifi, ethernet, wireguard, vpn, bridge)")
	return cmd
}

func (a *app) profileShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name|uuid>",
		Short: "Show one profile in full",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.resolveProfile(cmd.Context(), args[0], nil, "profile")
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(p)
			}
			u := a.ui
			state := u.dim.Render("inactive")
			if p.Active {
				state = u.green.Render("active")
			}
			fmt.Fprintf(a.out, "%s %s\n", u.bold.Render(p.Name), state)
			k := u.kv()
			k.add("UUID", p.UUID)
			typ := string(p.Type)
			if p.RawType != "" && p.RawType != typ {
				typ += u.dim.Render(" (" + p.RawType + ")")
			}
			k.add("Type", typ)
			k.add("Interface", p.InterfaceName)
			k.add("SSID", p.SSID)
			if p.Security != "" {
				k.add("Security", securityLabel(p.Security))
			}
			k.add("Autoconnect", yesNo(p.Autoconnect))
			if !p.Timestamp.IsZero() {
				k.add("Last used", ago(p.Timestamp))
			}
			k.add("IPv4", ipConfigLine(p.IPv4))
			k.add("IPv6", ipConfigLine(p.IPv6))
			if len(p.Permissions) > 0 {
				k.add("Permissions", strings.Join(p.Permissions, ", "))
			}
			k.add("File", p.Filename)
			if p.VPNServiceType != "" {
				k.add("VPN plugin", p.VPNServiceType)
			}
			if p.VPNData != nil {
				keys := make([]string, 0, len(p.VPNData))
				for key := range p.VPNData {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					k.add("  "+key, p.VPNData[key])
				}
			}
			if wg := p.WireGuard; wg != nil {
				k.add("WireGuard", fmt.Sprintf("pubkey %s  port %d  mtu %d", orDash(wg.PublicKey), wg.ListenPort, wg.MTU))
				for i, peer := range wg.Peers {
					k.add(fmt.Sprintf("  peer %d", i+1), fmt.Sprintf("%s  %s  allowed %s", truncate(peer.PublicKey, 16), orDash(peer.Endpoint), strings.Join(peer.AllowedIPs, ",")))
				}
			}
			k.render(a.out)
			return nil
		},
	}
}

func ipConfigLine(c core.IPConfig) string {
	if c.Method == "" {
		return ""
	}
	parts := []string{string(c.Method)}
	if len(c.Addresses) > 0 {
		parts = append(parts, strings.Join(c.Addresses, ","))
	}
	if c.Gateway != "" {
		parts = append(parts, "gw "+c.Gateway)
	}
	if len(c.DNS) > 0 {
		parts = append(parts, "dns "+strings.Join(c.DNS, ","))
	}
	if len(c.DNSSearch) > 0 {
		parts = append(parts, "search "+strings.Join(c.DNSSearch, ","))
	}
	if c.IgnoreAutoDNS {
		parts = append(parts, "ignore-auto-dns")
	}
	if c.NeverDefault {
		parts = append(parts, "never-default")
	}
	return strings.Join(parts, "  ")
}

func (a *app) profileIPCmd() *cobra.Command {
	var (
		v4method, v6method string
		v4addr, v6addr     []string
		v4gw, v6gw         string
		v4dns, v6dns       []string
		v4search, v6search []string
		v4ignore, v6ignore bool
	)
	cmd := &cobra.Command{
		Use:   "ip <name|uuid>",
		Short: "Replace a profile's IPv4 and/or IPv6 settings",
		Long: `Replace the IP layer of a profile. Flags for a family are only applied when
that family's --ipv4-method / --ipv6-method is given; the other family is left
as it is. Manual needs at least one address in CIDR form.

  bnm profile ip Office --ipv4-method manual --address 10.0.0.5/24 --gateway 10.0.0.1 --dns 10.0.0.1
  bnm profile ip Office --ipv4-method auto`,
		Args: a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if v4method == "" && v6method == "" {
				return usagef("give --ipv4-method and/or --ipv6-method")
			}
			build := func(method string, addrs []string, gw string, dns, search []string, ignore bool) (*core.IPConfig, error) {
				if method == "" {
					return nil, nil
				}
				m := core.IPMethod(method)
				switch m {
				case core.IPAuto, core.IPManual, core.IPDisabled, core.IPLinkLocal, core.IPShared, core.IPIgnore:
				default:
					return nil, usagef("unknown method %q (auto, manual, disabled, link-local, shared, ignore)", method)
				}
				if m == core.IPManual && len(addrs) == 0 {
					return nil, usagef("manual needs at least one --address CIDR")
				}
				return &core.IPConfig{Method: m, Addresses: addrs, Gateway: gw, DNS: dns, DNSSearch: search, IgnoreAutoDNS: ignore}, nil
			}
			ipv4, err := build(v4method, v4addr, v4gw, v4dns, v4search, v4ignore)
			if err != nil {
				return err
			}
			ipv6, err := build(v6method, v6addr, v6gw, v6dns, v6search, v6ignore)
			if err != nil {
				return err
			}
			p, err := a.resolveProfile(ctx, args[0], nil, "profile")
			if err != nil {
				return err
			}
			c, _ := a.client()
			if err := c.SetProfileIP(ctx, p.UUID, ipv4, ipv6); err != nil {
				return err
			}
			a.done("Updated IP settings of %s", p.Name)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&v4method, "ipv4-method", "", "auto|manual|disabled|link-local|shared")
	f.StringSliceVar(&v4addr, "address", nil, "IPv4 address in CIDR form (repeatable)")
	f.StringVar(&v4gw, "gateway", "", "IPv4 gateway")
	f.StringSliceVar(&v4dns, "dns", nil, "IPv4 DNS server (repeatable)")
	f.StringSliceVar(&v4search, "dns-search", nil, "IPv4 DNS search domain (repeatable)")
	f.BoolVar(&v4ignore, "ignore-auto-dns", false, "IPv4: ignore DHCP-provided DNS")
	f.StringVar(&v6method, "ipv6-method", "", "auto|manual|disabled|link-local|ignore")
	f.StringSliceVar(&v6addr, "address6", nil, "IPv6 address in CIDR form (repeatable)")
	f.StringVar(&v6gw, "gateway6", "", "IPv6 gateway")
	f.StringSliceVar(&v6dns, "dns6", nil, "IPv6 DNS server (repeatable)")
	f.StringSliceVar(&v6search, "dns-search6", nil, "IPv6 DNS search domain (repeatable)")
	f.BoolVar(&v6ignore, "ignore-auto-dns6", false, "IPv6: ignore RA/DHCPv6-provided DNS")
	return cmd
}

func (a *app) profileAutoconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "autoconnect <name|uuid> on|off",
		Short: "Set whether a profile connects automatically",
		Args:  a.exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			on, err := a.onOff(cmd, args[1])
			if err != nil {
				return err
			}
			p, err := a.resolveProfile(ctx, args[0], nil, "profile")
			if err != nil {
				return err
			}
			c, _ := a.client()
			if err := c.SetAutoconnect(ctx, p.UUID, on); err != nil {
				return err
			}
			a.done("Autoconnect %s for %s", onOffWord(on), p.Name)
			return nil
		},
	}
}

func (a *app) profileDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name|uuid>",
		Short: "Delete a profile",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := a.resolveProfile(ctx, args[0], nil, "profile")
			if err != nil {
				return err
			}
			c, _ := a.client()
			if err := c.DeleteProfile(ctx, p.UUID); err != nil {
				return err
			}
			a.done("Deleted %s", p.Name)
			return nil
		},
	}
}
