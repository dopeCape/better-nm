package diag

import (
	"context"
	"fmt"
	"sort"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/dopeCape/better-nm/internal/core"
)

// Routes lists every route in every table for both families.
func Routes(ctx context.Context) ([]core.Route, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	links, err := linkList()
	if err != nil {
		return nil, fmt.Errorf("diag: list links: %w", err)
	}
	names := make(map[int]string, len(links))
	for _, l := range links {
		names[l.Attrs().Index] = l.Attrs().Name
	}
	// Table RT_TABLE_UNSPEC with RT_FILTER_TABLE means "all tables".
	filter := &netlink.Route{Table: unix.RT_TABLE_UNSPEC}
	var out []core.Route
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		routes, err := routeListFiltered(family, filter, netlink.RT_FILTER_TABLE)
		if err != nil {
			return nil, fmt.Errorf("diag: list routes (family %d): %w", family, err)
		}
		for _, r := range routes {
			out = append(out, toRoute(r, family, names))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Family < out[j].Family
	})
	return out, nil
}

func toRoute(r netlink.Route, family int, names map[int]string) core.Route {
	cr := core.Route{
		Dest:   "default",
		Device: names[r.LinkIndex],
		Metric: r.Priority,
		Proto:  r.Protocol.String(),
		Scope:  r.Scope.String(),
		Table:  r.Table,
		Family: 4,
	}
	if family == netlink.FAMILY_V6 || r.Family == netlink.FAMILY_V6 {
		cr.Family = 6
	}
	if !isDefault(r.Dst) {
		cr.Dest = r.Dst.String()
	}
	if r.Gw != nil {
		cr.Gateway = r.Gw.String()
	}
	if cr.Device == "" && len(r.MultiPath) > 0 {
		cr.Device = names[r.MultiPath[0].LinkIndex]
		if cr.Gateway == "" && r.MultiPath[0].Gw != nil {
			cr.Gateway = r.MultiPath[0].Gw.String()
		}
	}
	return cr
}
