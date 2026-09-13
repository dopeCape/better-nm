package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dopeCape/better-nm/internal/core"
)

func (a *app) diagCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diag",
		Short: "Diagnostics: LAN hosts, ports, routes, DNS, public IP, bridges",
	}
	cmd.AddCommand(a.diagDevicesCmd(), a.diagPortsCmd(), a.diagRoutesCmd(), a.diagDNSCmd(), a.diagPublicIPCmd(), a.diagInfraCmd())
	return cmd
}

func (a *app) diagDevicesCmd() *cobra.Command {
	var device string
	var noSweep bool
	cmd := &cobra.Command{
		Use:     "devices",
		Aliases: []string{"lan"},
		Short:   "Neighbours on the current subnet (ARP/NDP plus an active sweep)",
		Args:    a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if device == "" {
				// The daemon wants a concrete interface; use the primary one.
				st, err := c.Status(cmd.Context())
				if err != nil {
					return err
				}
				if st.Primary == nil || len(st.Primary.Devices) == 0 {
					return core.Errorf(core.KindConflict, "pass --device", "not connected; no subnet to scan")
				}
				device = st.Primary.Devices[0]
			}
			hosts, err := c.LANHosts(cmd.Context(), device, !noSweep)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(nonNil(hosts))
			}
			if len(hosts) == 0 {
				fmt.Fprintln(a.out, a.ui.dim.Render("no neighbours seen"))
				return nil
			}
			sort.SliceStable(hosts, func(i, j int) bool {
				if hosts[i].Gateway != hosts[j].Gateway {
					return hosts[i].Gateway
				}
				return ipLess(hosts[i].IP, hosts[j].IP)
			})
			u := a.ui
			t := u.table("", "IP", "MAC", "HOSTNAME", "STATE", "DEVICE").shrinkCol(3)
			for _, h := range hosts {
				mark := " "
				name := h.Hostname
				switch {
				case h.Gateway:
					mark = u.cyan.Render("▲")
					if name == "" {
						name = "gateway"
					} else {
						name += u.dim.Render(" (gateway)")
					}
				case h.Self:
					mark = u.green.Render("●")
					if name == "" {
						name = "this host"
					} else {
						name += u.dim.Render(" (this host)")
					}
				}
				state := h.State
				switch h.State {
				case "reachable":
					state = u.green.Render(h.State)
				case "failed", "incomplete":
					state = u.red.Render(h.State)
				default:
					state = u.dim.Render(h.State)
				}
				t.add(mark, h.IP, orDash(h.MAC), orDash(name), state, h.Device)
			}
			t.render(a.out)
			return nil
		},
	}
	cmd.Flags().StringVar(&device, "device", "", "only this interface's subnet")
	cmd.Flags().BoolVar(&noSweep, "no-sweep", false, "only what the neighbour table already knows; do not probe")
	return cmd
}

// ipLess orders dotted IPv4 numerically and everything else lexically.
func ipLess(x, y string) bool {
	xs, ys := strings.Split(x, "."), strings.Split(y, ".")
	if len(xs) == 4 && len(ys) == 4 {
		for i := 0; i < 4; i++ {
			var xi, yi int
			fmt.Sscanf(xs[i], "%d", &xi)
			fmt.Sscanf(ys[i], "%d", &yi)
			if xi != yi {
				return xi < yi
			}
		}
		return false
	}
	return x < y
}

func (a *app) diagPortsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ports",
		Short: "Listening TCP and bound UDP sockets on this host",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ports, err := c.ListeningPorts(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(nonNil(ports))
			}
			if len(ports) == 0 {
				fmt.Fprintln(a.out, a.ui.dim.Render("nothing listening"))
				return nil
			}
			sort.SliceStable(ports, func(i, j int) bool {
				if ports[i].Port != ports[j].Port {
					return ports[i].Port < ports[j].Port
				}
				return ports[i].Proto < ports[j].Proto
			})
			u := a.ui
			t := u.table("PROTO", "ADDRESS", "PORT", "PROCESS", "PID", "USER").alignRight(2, 4)
			for _, p := range ports {
				proc := p.Process
				if proc == "" {
					proc = u.dim.Render("(not visible)")
				}
				pid := "-"
				if p.PID > 0 {
					pid = fmt.Sprint(p.PID)
				}
				addr := p.Addr
				if addr == "0.0.0.0" || addr == "::" {
					addr = u.yellow.Render("*")
				}
				t.add(p.Proto, addr, fmt.Sprint(p.Port), proc, pid, p.User)
			}
			t.render(a.out)
			fmt.Fprintln(a.out, u.dim.Render("* = every interface"))
			return nil
		},
	}
}

func (a *app) diagRoutesCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "routes",
		Short: "Kernel routing table",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			routes, err := c.Routes(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(nonNil(routes))
			}
			u := a.ui
			t := u.table("DEST", "VIA", "DEVICE", "METRIC", "PROTO", "TABLE").alignRight(3)
			hidden := 0
			for _, r := range routes {
				if !all && r.Table != 0 && r.Table != 254 {
					hidden++
					continue
				}
				dest := r.Dest
				if dest == "default" {
					dest = u.bold.Render("default")
				}
				table := "main"
				if r.Table != 0 && r.Table != 254 {
					table = fmt.Sprint(r.Table)
				}
				t.add(dest, orDash(r.Gateway), r.Device, fmt.Sprint(r.Metric), orDash(r.Proto), table)
			}
			t.render(a.out)
			if hidden > 0 {
				fmt.Fprintln(a.out, u.dim.Render(fmt.Sprintf("%d routes in other tables hidden (--all)", hidden)))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include policy routing tables (VPNs)")
	return cmd
}

func (a *app) diagDNSCmd() *cobra.Command {
	var server, qtype string
	cmd := &cobra.Command{
		Use:   "dns <name>",
		Short: "Resolve a name and time it",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ans, err := c.DNSLookup(cmd.Context(), args[0], server, qtype)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(ans)
			}
			u := a.ui
			via := "system resolver"
			if ans.Server != "" {
				via = ans.Server
			}
			if ans.Error != "" {
				fmt.Fprintf(a.out, "%s %s %s via %s: %s\n", u.dot("red"), ans.Name, ans.Type, via, u.red.Render(ans.Error))
				return core.Errorf(core.KindInternal, "", "lookup failed")
			}
			fmt.Fprintf(a.out, "%s %s %s via %s in %s\n", u.dot("green"), u.bold.Render(ans.Name), ans.Type, via, ans.Duration.Round(100000).String())
			for _, r := range ans.Answers {
				fmt.Fprintf(a.out, "  %s\n", r)
			}
			if len(ans.Answers) == 0 {
				fmt.Fprintln(a.out, u.dim.Render("  (no records)"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "DNS server to ask (default: the system resolver)")
	cmd.Flags().StringVar(&qtype, "type", "A", "record type (A, AAAA, MX, TXT, ...)")
	return cmd
}

func (a *app) diagPublicIPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "public-ip",
		Short: "What the internet sees: address, location, Cloudflare colo",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ip, err := c.PublicIP(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(ip)
			}
			u := a.ui
			fmt.Fprintln(a.out, u.bold.Render(ip.IP))
			k := u.kv()
			k.add("Location", ip.Location)
			k.add("Colo", ip.Colo)
			if ip.Warp != "" && ip.Warp != "off" {
				k.add("WARP", ip.Warp)
			}
			k.add("Via", u.dim.Render(ip.Via))
			k.render(a.out)
			return nil
		},
	}
}

func (a *app) diagInfraCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "infra",
		Short: "Container and VM bridges with their members (inspect only)",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			nets, err := c.Infra(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(nonNil(nets))
			}
			u := a.ui
			if len(nets) == 0 {
				fmt.Fprintln(a.out, u.dim.Render("no container or VM bridges"))
				return nil
			}
			for i, n := range nets {
				if i > 0 {
					fmt.Fprintln(a.out)
				}
				up := u.dot("green")
				if !n.Bridge.Up {
					up = u.dot("")
				}
				owner := n.Owner
				if n.OwnerName != "" {
					owner += " " + u.dim.Render("("+n.OwnerName+")")
				}
				fmt.Fprintf(a.out, "%s %s  %s  %s\n", up, u.bold.Render(n.Bridge.Name), owner, joinOrDash(n.Bridge.Addresses))
				if n.Reachable != "" && n.Reachable != "ok" {
					fmt.Fprintf(a.out, "  %s\n", u.yellow.Render("runtime API: "+n.Reachable))
				}
				for j, m := range n.Members {
					branch := "├─"
					if j == len(n.Members)-1 {
						branch = "└─"
					}
					state := u.green.Render("up")
					if !m.Up {
						state = u.dim.Render("down")
					}
					extra := ""
					if m.PeerNetNS >= 0 && m.Kind == "veth" {
						extra = u.dim.Render(fmt.Sprintf("  netns %d", m.PeerNetNS))
					}
					fmt.Fprintf(a.out, "  %s %s  %s  %s%s\n", u.dim.Render(branch), m.Name, m.Kind, state, extra)
				}
				for _, h := range n.Neighbours {
					fmt.Fprintf(a.out, "     %s %s  %s\n", u.dim.Render("·"), h.IP, u.dim.Render(strings.TrimSpace(h.MAC+" "+h.Hostname)))
				}
			}
			return nil
		},
	}
}
