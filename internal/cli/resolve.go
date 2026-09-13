package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dopeCape/better-nm/internal/core"
)

// candidate is one thing a <name|uuid> argument may mean.
type candidate struct {
	ID   string // UUID (profiles) or VPN ID
	Name string
	Kind string // "wifi profile", "wired profile", "VPN (WireGuard)", ...
	SSID string
}

func (c candidate) String() string {
	if c.SSID != "" && c.SSID != c.Name {
		return fmt.Sprintf("%s  %s (%s, ssid %q)", c.ID, c.Name, c.Kind, c.SSID)
	}
	return fmt.Sprintf("%s  %s (%s)", c.ID, c.Name, c.Kind)
}

// ambiguousError lists the candidates a name matched.
type ambiguousError struct {
	arg        string
	candidates []candidate
}

func (e *ambiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q is ambiguous; it matches:", e.arg)
	for _, c := range e.candidates {
		b.WriteString("\n  " + c.String())
	}
	return b.String()
}

func (e *ambiguousError) Hint() string { return "use the UUID (or a longer prefix of it)" }

// resolve picks exactly one candidate for arg: an exact ID, then an exact
// name/SSID, then a case-insensitive name/SSID, then an ID prefix, then a
// name/SSID prefix. More than one hit at the winning tier is an error listing them.
func resolve(arg string, pool []candidate, what string) (candidate, error) {
	if arg == "" {
		return candidate{}, core.Errorf(core.KindInvalid, "", "a %s name or UUID is required", what)
	}
	for _, c := range pool {
		if c.ID == arg {
			return c, nil
		}
	}
	tiers := []func(candidate) bool{
		func(c candidate) bool { return c.Name == arg || c.SSID == arg },
		func(c candidate) bool {
			return strings.EqualFold(c.Name, arg) || strings.EqualFold(c.SSID, arg)
		},
		func(c candidate) bool {
			return len(arg) >= 3 && strings.HasPrefix(strings.ToLower(c.ID), strings.ToLower(arg))
		},
		func(c candidate) bool {
			l := strings.ToLower(arg)
			return len(arg) >= 2 && (strings.HasPrefix(strings.ToLower(c.Name), l) || strings.HasPrefix(strings.ToLower(c.SSID), l))
		},
	}
	for _, match := range tiers {
		var hits []candidate
		for _, c := range pool {
			if match(c) {
				hits = append(hits, c)
			}
		}
		if len(hits) == 1 {
			return hits[0], nil
		}
		if len(hits) > 1 {
			sort.Slice(hits, func(i, j int) bool { return hits[i].Name < hits[j].Name })
			return candidate{}, &ambiguousError{arg: arg, candidates: hits}
		}
	}
	names := make([]string, 0, len(pool))
	for _, c := range pool {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	hint := ""
	if len(names) > 0 {
		hint = "known: " + strings.Join(names, ", ")
	}
	return candidate{}, core.Errorf(core.KindNotFound, hint, "no %s matches %q", what, arg)
}

func profileKind(p core.Profile) string {
	switch p.Type {
	case core.ProfileWifi:
		return "wifi profile"
	case core.ProfileEthernet:
		return "wired profile"
	case core.ProfileWireGuard:
		return "WireGuard profile"
	case core.ProfileVPN:
		return core.VPNKindForServiceType(p.VPNServiceType) + " profile"
	case core.ProfileBridge:
		return "bridge profile"
	}
	return p.RawType + " profile"
}

func profileCandidates(ps []core.Profile, keep func(core.Profile) bool) []candidate {
	var out []candidate
	for _, p := range ps {
		if keep != nil && !keep(p) {
			continue
		}
		out = append(out, candidate{ID: p.UUID, Name: p.Name, Kind: profileKind(p), SSID: p.SSID})
	}
	return out
}

func vpnCandidates(vs []core.VPN) []candidate {
	var out []candidate
	for _, v := range vs {
		out = append(out, candidate{ID: v.ID, Name: v.Name, Kind: "VPN, " + v.Kind})
	}
	return out
}

// resolveProfile finds one profile (optionally filtered by keep) by name, SSID or UUID.
func (a *app) resolveProfile(ctx context.Context, arg string, keep func(core.Profile) bool, what string) (core.Profile, error) {
	c, err := a.client()
	if err != nil {
		return core.Profile{}, err
	}
	profiles, err := c.Profiles(ctx)
	if err != nil {
		return core.Profile{}, err
	}
	hit, err := resolve(arg, profileCandidates(profiles, keep), what)
	if err != nil {
		return core.Profile{}, err
	}
	for _, p := range profiles {
		if p.UUID == hit.ID {
			return p, nil
		}
	}
	return core.Profile{}, core.Errorf(core.KindNotFound, "", "profile %s vanished", hit.ID)
}

// resolveVPN finds one VPN by name, ID or UUID prefix. "ts" is an alias for tailscale.
func (a *app) resolveVPN(ctx context.Context, arg string) (core.VPN, error) {
	c, err := a.client()
	if err != nil {
		return core.VPN{}, err
	}
	vpns, err := c.VPNs(ctx)
	if err != nil {
		return core.VPN{}, err
	}
	if strings.EqualFold(arg, "ts") {
		arg = "tailscale"
	}
	hit, err := resolve(arg, vpnCandidates(vpns), "VPN")
	if err != nil {
		return core.VPN{}, err
	}
	for _, v := range vpns {
		if v.ID == hit.ID {
			return v, nil
		}
	}
	return core.VPN{}, core.Errorf(core.KindNotFound, "", "VPN %s vanished", hit.ID)
}
