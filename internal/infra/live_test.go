//go:build live

package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestLive prints this machine's links and infra networks (read-only).
func TestLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	links, err := Links(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		class, owner := Classify(l.Name, l.Kind, HasSysfsDevice(l.Name))
		t.Logf("link %-16s idx=%-3d kind=%-9s class=%-8s owner=%-9s master=%-16s up=%-5v mtu=%-5d netnsid=%-2d hw=%s addrs=%v",
			l.Name, l.IfIndex, l.Kind, class, owner, l.Master, l.Up, l.MTU, l.PeerNetNS, l.HwAddr, l.Addresses)
	}

	nets, err := Networks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nets {
		b, _ := json.MarshalIndent(n, "", "  ")
		t.Logf("network %s:\n%s", n.Bridge.Name, b)
	}
}
