package tui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/fake"
)

// secretRoutes serves the four secret-agent routes from the fake broker in
// front of the real API server, until internal/api grows them.
func secretRoutes(b *fake.SecretBroker, rest http.Handler) http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(api.StatusFor(err))
		_ = json.NewEncoder(w).Encode(api.ErrorBody(err))
	}
	mux.HandleFunc("GET "+api.Prefix+"/secrets", func(w http.ResponseWriter, r *http.Request) {
		reqs, _ := b.Pending(r.Context())
		if reqs == nil {
			reqs = []core.SecretRequest{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reqs)
	})
	mux.HandleFunc("GET "+api.Prefix+"/secrets/{id}", func(w http.ResponseWriter, r *http.Request) {
		reqs, _ := b.Pending(r.Context())
		for _, req := range reqs {
			if req.ID == r.PathValue("id") {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(req)
				return
			}
		}
		fail(w, core.Errorf(core.KindNotFound, "", "secret request %s is not pending", r.PathValue("id")))
	})
	mux.HandleFunc("POST "+api.Prefix+"/secrets/{id}", func(w http.ResponseWriter, r *http.Request) {
		var a core.SecretAnswer
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			fail(w, core.Errorf(core.KindInvalid, "", "bad body: %v", err))
			return
		}
		if err := b.Answer(r.Context(), r.PathValue("id"), a); err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.OKResponse{OK: true})
	})
	mux.HandleFunc("POST "+api.Prefix+"/secrets/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := b.Cancel(r.Context(), r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.OKResponse{OK: true})
	})
	mux.Handle("/", rest)
	return mux
}

func wifiSecret(id, ssid string, wrong bool) core.SecretRequest {
	return core.SecretRequest{
		ID: id, ConnectionUUID: "uuid-" + id, ConnectionName: ssid, SSID: ssid,
		SettingName: "802-11-wireless-security",
		Fields:      []core.SecretField{{Key: "psk", Label: "Wi-Fi password", Secret: true}},
		RequestNew:  wrong, UserRequested: true,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(90 * time.Second),
	}
}

func vpnSecret(id string) core.SecretRequest {
	return core.SecretRequest{
		ID: id, ConnectionUUID: "uuid-" + id, ConnectionName: "office-ovpn", VPN: true, VPNKind: "OpenVPN",
		SettingName: "vpn", Message: "Enter your one-time code",
		Fields: []core.SecretField{
			{Key: "username", Label: "Username"},
			{Key: "challenge-response", Label: "One-time code", Secret: true},
		},
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(2 * time.Minute),
	}
}

// raise puts req on the broker and pumps the stream until the TUI has
// fetched it.
func (h *harness) raise(req core.SecretRequest) <-chan core.SecretAnswer {
	h.t.Helper()
	ch := h.r.secrets.Raise(req)
	ok := h.pump(5*time.Second, func(it client.StreamItem) bool {
		return it.Event != nil && it.Event.Type == core.EventSecretNeeded && it.Event.Data["request_id"] == req.ID
	})
	if !ok {
		h.t.Fatalf("secret-needed for %s never arrived", req.ID)
	}
	return ch
}

// pumpResolved waits for the secret-resolved event of id.
func (h *harness) pumpResolved(id string) {
	h.t.Helper()
	ok := h.pump(5*time.Second, func(it client.StreamItem) bool {
		return it.Event != nil && it.Event.Type == core.EventSecretResolved && it.Event.Data["request_id"] == id
	})
	if !ok {
		h.t.Fatalf("secret-resolved for %s never arrived", id)
	}
}

func TestSecretPromptAnswersWithSave(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	h.key("3") // any tab: the prompt is modal over it
	ch := h.raise(wifiSecret("s1", "HomeNet", true))
	if !h.m.secrets.open() {
		t.Fatal("secret-needed should open the prompt")
	}
	v := h.view()
	mustContain(t, v, "Password needed for HomeNet", "The saved password was rejected", "Wi-Fi password:", "[x] Save password", "expires in", "secrets 1", "enter submit")
	mustContain(t, v, "Tailscale") // the VPN tab is still underneath
	for i, l := range lines(v) {
		if w := lipgloss.Width(l); w != 100 {
			t.Errorf("line %d is %d cells wide with the overlay: %q", i, w, l)
		}
	}
	// tab keys do not switch tabs while the prompt is up
	h.key("tab")
	if h.m.tab != tabVPN {
		t.Error("tab must move focus inside the prompt, not switch tabs")
	}
	mustContain(t, h.view(), "space toggle save")
	h.key("shift+tab")
	h.typeText("hunter22")
	mustNotContain(t, h.view(), "hunter22") // masked
	h.key("enter")
	select {
	case a, ok := <-ch:
		if !ok || a.Secrets["psk"] != "hunter22" || !a.Save {
			t.Fatalf("answer = %+v ok=%v", a, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer reached the broker")
	}
	if h.m.secrets.open() {
		t.Fatal("prompt should close once the answer is accepted")
	}
	h.pumpResolved("s1")
	v = h.view()
	mustNotContain(t, v, "Password needed", "secrets 1")
	if h.m.secrets.pending() != 0 {
		t.Errorf("pending = %d", h.m.secrets.pending())
	}
	if pend, _ := r.secrets.Pending(t.Context()); len(pend) != 0 {
		t.Errorf("broker still pending: %+v", pend)
	}
}

func TestSecretPromptSaveToggleAndVPNFields(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	ch := h.raise(vpnSecret("v1"))
	v := h.view()
	mustContain(t, v, "office-ovpn · Enter your one-time code", "Username:", "One-time code:")
	mustNotContain(t, v, "rejected")
	h.typeText("alice")
	h.key("enter") // enter on a middle field moves on
	if h.m.secrets.open() && h.m.secrets.cur.focus != 1 {
		t.Fatalf("focus = %d", h.m.secrets.cur.focus)
	}
	h.key("enter") // empty code: stays put
	if !h.m.secrets.open() {
		t.Fatal("an empty secret must not submit")
	}
	h.typeText("123456")
	mustNotContain(t, h.view(), "123456")
	mustContain(t, h.view(), "alice") // usernames are not masked
	h.key("tab", " ")                 // onto the save toggle, turn it off
	mustContain(t, h.view(), "[ ] Save password")
	h.key("enter")
	select {
	case a, ok := <-ch:
		if !ok || a.Secrets["username"] != "alice" || a.Secrets["challenge-response"] != "123456" || a.Save {
			t.Fatalf("answer = %+v ok=%v", a, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer reached the broker")
	}
	h.pumpResolved("v1")
	if h.m.secrets.open() {
		t.Fatal("prompt should close")
	}
}

func TestSecretPromptEscCancels(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	ch := h.raise(wifiSecret("s2", "Office", false))
	mustContain(t, h.view(), "Password needed for Office")
	mustNotContain(t, h.view(), "rejected")
	h.key("esc")
	if h.m.secrets.open() {
		t.Fatal("esc closes the prompt")
	}
	select {
	case a, ok := <-ch:
		if ok {
			t.Fatalf("cancel must not deliver an answer: %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("broker was not cancelled")
	}
	h.pumpResolved("s2")
	mustNotContain(t, h.view(), "secrets 1", "Password needed")
	// q works again once the prompt is gone
	h.key("q")
	if !h.quit {
		t.Error("q should quit after the prompt closes")
	}
}

func TestSecretResolvedElsewhereClosesPrompt(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	h.raise(wifiSecret("s3", "HomeNet", true))
	mustContain(t, h.view(), "Password needed for HomeNet")
	// the desktop app answers it
	if err := r.secrets.Answer(t.Context(), "s3", core.SecretAnswer{Secrets: map[string]string{"psk": "x"}}); err != nil {
		t.Fatal(err)
	}
	h.pumpResolved("s3")
	if h.m.secrets.open() {
		t.Fatal("secret-resolved for the shown request must close the prompt")
	}
	mustNotContain(t, h.view(), "Password needed", "secrets")
	// a timeout says so in the footer
	h.raise(wifiSecret("s4", "HomeNet", true))
	r.secrets.Expire("s4")
	h.pumpResolved("s4")
	if h.m.secrets.open() {
		t.Fatal("timeout must close the prompt")
	}
	mustContain(t, h.view(), "password request for HomeNet timed out")
}

func TestSecretPromptsQueue(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	ch1 := h.raise(wifiSecret("q1", "HomeNet", true))
	ch2 := h.raise(vpnSecret("q2"))
	v := h.view()
	mustContain(t, v, "Password needed for HomeNet", "1 more waiting", "secrets 2")
	mustNotContain(t, v, "office-ovpn ·")
	if h.m.secrets.pending() != 2 {
		t.Fatalf("pending = %d", h.m.secrets.pending())
	}
	h.typeText("pw1")
	h.key("enter")
	if a := <-ch1; a.Secrets["psk"] != "pw1" {
		t.Fatalf("first answer = %+v", a)
	}
	if !h.m.secrets.open() || h.m.secrets.cur.req.ID != "q2" {
		t.Fatal("the second request should show once the first is answered")
	}
	h.pumpResolved("q1")
	v = h.view()
	mustContain(t, v, "office-ovpn", "secrets 1")
	mustNotContain(t, v, "more waiting", "HomeNet ·")
	h.key("esc")
	if _, ok := <-ch2; ok {
		t.Fatal("esc must cancel the second request")
	}
	h.pumpResolved("q2")
	if h.m.secrets.pending() != 0 || h.m.secrets.open() {
		t.Errorf("pending = %d open = %v", h.m.secrets.pending(), h.m.secrets.open())
	}
	// a request raised while the TUI was away is picked up on (re)connect
	ch3 := r.secrets.Raise(wifiSecret("q3", "Office", false))
	h.run(h.m.l.pendingSecrets())
	if !h.m.secrets.open() || h.m.secrets.cur.req.ID != "q3" {
		t.Fatal("PendingSecrets on connect should open the waiting request")
	}
	// and the late secret-needed event for it is not a duplicate
	h.pump(5*time.Second, func(it client.StreamItem) bool {
		return it.Event != nil && it.Event.Type == core.EventSecretNeeded && it.Event.Data["request_id"] == "q3"
	})
	if h.m.secrets.pending() != 1 {
		t.Errorf("pending = %d after the duplicate event", h.m.secrets.pending())
	}
	h.typeText("pw3")
	h.key("enter")
	<-ch3
}

func TestSecretPromptAnswerErrorStaysOpen(t *testing.T) {
	r := newRig(t)
	h := newHarness(t, r, 100, 30)
	h.raise(wifiSecret("e1", "HomeNet", false))
	// the request vanished on the daemon side before the answer landed
	if err := r.secrets.Cancel(t.Context(), "e1"); err != nil {
		t.Fatal(err)
	}
	h.typeText("pw")
	h.key("enter")
	if !h.m.secrets.open() || h.m.secrets.cur.err == nil {
		t.Fatal("a failed answer keeps the prompt open with the error")
	}
	mustContain(t, h.view(), "not pending")
	h.pumpResolved("e1")
	if h.m.secrets.open() {
		t.Fatal("the resolved event closes it")
	}
}

func TestCountdownAndOverlayHelpers(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		in   time.Time
		want string
	}{
		{time.Time{}, ""},
		{now.Add(-time.Second), "expired"},
		{now.Add(42 * time.Second), "expires in 42s"},
		{now.Add(90 * time.Second), "expires in 1m30s"},
	}
	for _, tt := range tests {
		if got := countdown(tt.in, now); got != tt.want {
			t.Errorf("countdown(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
	body := strings.Repeat(strings.Repeat("a", 20)+"\n", 5)
	box := "XXXX\nXXXX"
	out := overlayBox(strings.TrimRight(body, "\n"), box, 20, 5)
	ls := strings.Split(out, "\n")
	if len(ls) != 5 {
		t.Fatalf("lines = %d", len(ls))
	}
	for i, l := range ls {
		if w := lipgloss.Width(l); w != 20 {
			t.Errorf("line %d width %d", i, w)
		}
	}
	if !strings.Contains(ls[1], "aaaaaaaa\x1b[0mXXXXaaaaaaaa") || !strings.Contains(ls[2], "XXXX") || strings.Contains(ls[0], "X") {
		t.Errorf("overlay placement:\n%s", out)
	}
}
