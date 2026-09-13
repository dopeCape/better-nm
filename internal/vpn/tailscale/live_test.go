//go:build live

package tailscale

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dopeCape/better-nm/internal/core"
)

// Read-only against this machine's tailscaled. Never calls a write endpoint
// other than the adapter's empty-mask probe (a no-op edit that only tells us
// whether operator mode is on).
func TestLiveStatusAndPrefs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := New("")
	st, err := c.Status(ctx)
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("tailscaled not running: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{
		StateNoState: true, StateInUseOtherUser: true, StateNeedsLogin: true, StateNeedsMachineAuth: true,
		StateStopped: true, StateStarting: true, StateRunning: true,
	}
	if !known[st.BackendState] {
		t.Errorf("BackendState %q is not a known ipn.State", st.BackendState)
	}
	if st.Version == "" {
		t.Error("empty Version")
	}
	t.Logf("socket=%s version=%s state=%s self=%v peers=%d suffix=%s health=%v",
		c.Socket(), st.Version, st.BackendState, st.Self != nil, len(st.Peer), st.MagicDNSSuffix, st.Health)

	pr, err := c.Prefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pr.ControlURL == "" {
		t.Error("empty ControlURL")
	}
	t.Logf("prefs: control=%s wantRunning=%v corpDNS=%v exitNode=%q operator=%q",
		pr.ControlURL, pr.WantRunning, pr.CorpDNS, pr.ExitNodeID, pr.OperatorUser)

	a := NewAdapter(c, nil)
	vpns, err := a.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := vpns[0]
	if v.State != MapState(st.BackendState) {
		t.Errorf("adapter state %s != MapState(%s)", v.State, st.BackendState)
	}
	if v.Tailscale == nil || v.Tailscale.BackendState != st.BackendState {
		t.Errorf("info = %+v", v.Tailscale)
	}
	t.Logf("vpn: state=%s writable=%v detail=%q peers=%d", v.State, v.Writable, v.Detail, len(v.Tailscale.Peers))
	if v.State == core.VPNUnavailable {
		t.Error("adapter reports unavailable while Status worked")
	}
}

func TestLiveWatchInitial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := New("")
	var first *Notify
	err := c.WatchIPNBus(ctx, DefaultWatchMask, func(n Notify) bool {
		first = &n
		return false
	})
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("tailscaled not running: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.State == nil || first.Prefs == nil {
		t.Fatalf("initial notify = %+v", first)
	}
	t.Logf("initial notify: state=%s prefs.WantRunning=%v health=%v", first.StateName(), first.Prefs.WantRunning, first.Health.Messages())
}
