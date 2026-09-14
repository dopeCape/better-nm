package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/core"
	"github.com/dopeCape/better-nm/internal/fake"
)

// secretRoutes serves the four secret-agent routes from the fake broker in
// front of the real API server, until internal/api grows them.
func secretRoutes(b *fake.SecretBroker, rest http.Handler) http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, err error) { reply(w, api.StatusFor(err), api.ErrorBody(err)) }
	mux.HandleFunc("GET "+api.Prefix+"/secrets", func(w http.ResponseWriter, r *http.Request) {
		reqs, _ := b.Pending(r.Context())
		if reqs == nil {
			reqs = []core.SecretRequest{}
		}
		reply(w, http.StatusOK, reqs)
	})
	mux.HandleFunc("GET "+api.Prefix+"/secrets/{id}", func(w http.ResponseWriter, r *http.Request) {
		reqs, _ := b.Pending(r.Context())
		for _, req := range reqs {
			if req.ID == r.PathValue("id") {
				reply(w, http.StatusOK, req)
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
		reply(w, http.StatusOK, api.OKResponse{OK: true})
	})
	mux.HandleFunc("POST "+api.Prefix+"/secrets/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := b.Cancel(r.Context(), r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		reply(w, http.StatusOK, api.OKResponse{OK: true})
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

// raise puts req on the broker once the app follows the stream, and waits
// for the prompt for it (or its place in the queue).
func (r *rig) raise(req core.SecretRequest) <-chan core.SecretAnswer {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for r.d.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	ch := r.secrets.Raise(req)
	r.waitFor("request "+req.ID+" to reach the app", func() bool { return r.a.secretKnown[req.ID] })
	return ch
}

func (r *rig) prompt() *secretPrompt {
	var p *secretPrompt
	r.ui(func() { p = r.a.secret })
	return p
}

func (r *rig) waitClosed(id string) {
	r.t.Helper()
	r.waitFor("prompt "+id+" to close", func() bool { return r.a.secret == nil || r.a.secret.req.ID != id })
}

func TestSecretDialogSubmitsAnswer(t *testing.T) {
	r := newRig(t)
	ch := r.raise(wifiSecret("s1", "HomeNet", true))
	p := r.prompt()
	if p == nil {
		t.Fatal("secret-needed should open the prompt")
	}
	r.ui(func() {
		if r.a.win.Canvas().Overlays().Top() == nil {
			t.Error("the dialog should be an overlay on the window")
		}
		if len(p.entries) != 1 || !p.entries[0].Password {
			t.Errorf("entries = %d (password=%v)", len(p.entries), len(p.entries) > 0 && p.entries[0].Password)
		}
		if p.wrong == nil || p.wrong.Text != "The saved password was rejected" {
			t.Error("a RequestNew prompt says the saved password was rejected")
		}
		if !p.save.Checked {
			t.Error("Save password defaults on")
		}
		if !strings.HasPrefix(p.countdown.Text, "Expires in ") {
			t.Errorf("countdown = %q", p.countdown.Text)
		}
		if got := secretTitle(p.req); got != "Password needed for HomeNet" {
			t.Errorf("title = %q", got)
		}
		// an empty password does not submit
		test.Tap(p.submit)
	})
	r.idle()
	if pend, _ := r.secrets.Pending(context.Background()); len(pend) != 1 {
		t.Fatalf("an empty submit must not answer: pending = %d", len(pend))
	}
	r.ui(func() {
		p.entries[0].SetText("hunter22")
		test.Tap(p.submit)
	})
	select {
	case a, ok := <-ch:
		if !ok || a.Secrets["psk"] != "hunter22" || !a.Save {
			t.Fatalf("answer = %+v ok=%v", a, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer reached the broker")
	}
	r.waitClosed("s1")
	r.idle()
	r.ui(func() {
		if r.a.win.Canvas().Overlays().Top() != nil {
			t.Error("dialog should be gone after the answer")
		}
		if r.a.secretPending() != 0 || r.a.secretKnown["s1"] {
			t.Errorf("pending = %d known = %v", r.a.secretPending(), r.a.secretKnown)
		}
	})
}

func TestSecretDialogCancelSendsCancel(t *testing.T) {
	r := newRig(t)
	ch := r.raise(wifiSecret("s2", "Office", false))
	p := r.prompt()
	if p == nil {
		t.Fatal("no prompt")
	}
	r.ui(func() {
		if p.wrong != nil {
			t.Error("a first-time request must not claim the password was rejected")
		}
		test.Tap(p.cancel)
	})
	select {
	case a, ok := <-ch:
		if ok {
			t.Fatalf("cancel must not deliver an answer: %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("broker was not cancelled")
	}
	r.waitClosed("s2")
	r.idle()
	if pend, _ := r.secrets.Pending(context.Background()); len(pend) != 0 {
		t.Errorf("broker still pending: %+v", pend)
	}
}

func TestSecretResolvedElsewhereClosesDialog(t *testing.T) {
	r := newRig(t)
	r.raise(wifiSecret("s3", "HomeNet", true))
	if r.prompt() == nil {
		t.Fatal("no prompt")
	}
	// the TUI answered it
	if err := r.secrets.Answer(context.Background(), "s3", core.SecretAnswer{Secrets: map[string]string{"psk": "x"}}); err != nil {
		t.Fatal(err)
	}
	r.waitClosed("s3")
	r.idle()
	r.ui(func() {
		if r.a.win.Canvas().Overlays().Top() != nil {
			t.Error("dialog should close on secret-resolved")
		}
	})
	// a timeout closes it the same way
	r.raise(wifiSecret("s4", "HomeNet", true))
	r.secrets.Expire("s4")
	r.waitClosed("s4")
}

func TestSecretDialogsQueue(t *testing.T) {
	r := newRig(t)
	ch1 := r.raise(wifiSecret("q1", "HomeNet", true))
	ch2 := r.raise(vpnSecret("q2"))
	p1 := r.prompt()
	if p1 == nil || p1.req.ID != "q1" {
		t.Fatalf("first prompt = %+v", p1)
	}
	r.ui(func() {
		if r.a.secretPending() != 2 || len(r.a.secretQueue) != 1 {
			t.Errorf("pending = %d queue = %d", r.a.secretPending(), len(r.a.secretQueue))
		}
		p1.entries[0].SetText("pw1")
		p1.save.SetChecked(false)
		test.Tap(p1.submit)
	})
	if a := <-ch1; a.Secrets["psk"] != "pw1" || a.Save {
		t.Fatalf("first answer = %+v", a)
	}
	r.waitFor("second prompt", func() bool { return r.a.secret != nil && r.a.secret.req.ID == "q2" })
	p2 := r.prompt()
	r.ui(func() {
		if got := secretTitle(p2.req); got != "office-ovpn · Enter your one-time code" {
			t.Errorf("vpn title = %q", got)
		}
		if len(p2.entries) != 2 || p2.entries[0].Password || !p2.entries[1].Password {
			t.Errorf("vpn entries = %d", len(p2.entries))
		}
		if p2.wrong != nil {
			t.Error("no rejected line for a first request")
		}
		p2.entries[0].SetText("alice")
		p2.entries[1].SetText("123456")
		p2.entries[1].OnSubmitted("123456") // enter in the last entry submits
	})
	select {
	case a, ok := <-ch2:
		if !ok || a.Secrets["username"] != "alice" || a.Secrets["challenge-response"] != "123456" || !a.Save {
			t.Fatalf("second answer = %+v ok=%v", a, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer for the second request")
	}
	r.waitClosed("q2")
	r.idle()
	r.ui(func() {
		if r.a.secretPending() != 0 {
			t.Errorf("pending = %d", r.a.secretPending())
		}
	})
	// a request raised while nobody listened is picked up by loadPendingSecrets
	ch3 := r.secrets.Raise(wifiSecret("q3", "Office", false))
	r.waitFor("q3 via event", func() bool { return r.a.secret != nil && r.a.secret.req.ID == "q3" })
	r.a.loadPendingSecrets()
	r.idle()
	r.ui(func() {
		if r.a.secretPending() != 1 {
			t.Errorf("PendingSecrets must not duplicate a shown request: pending = %d", r.a.secretPending())
		}
		test.Tap(r.a.secret.cancel)
	})
	<-ch3
}

func TestSecretAnswerErrorKeepsDialog(t *testing.T) {
	r := newRig(t)
	r.raise(wifiSecret("e1", "HomeNet", false))
	p := r.prompt()
	// gone on the daemon side before the answer lands
	if err := r.secrets.Cancel(context.Background(), "e1"); err != nil {
		t.Fatal(err)
	}
	r.waitClosed("e1") // the resolved event closes it...
	r.ui(func() {
		if r.a.secret != nil {
			t.Fatal("prompt should be closed by the resolved event")
		}
		// ...and a late submit from the old dialog is ignored
		p.entries[0].SetText("pw")
		test.Tap(p.submit)
	})
	r.idle()
}

func TestTrayMenuShowsPendingSecret(t *testing.T) {
	r := newRig(t)
	tr := &tray{a: r.a}
	m := tr.menu(trayState{connection: fake.HomeSSID, wifiOn: true, wifiHW: true, verdict: "ok", secret: "Password needed for HomeNet"})
	if len(m.Items) < 2 || m.Items[1].Label != "Password needed for HomeNet" || m.Items[1].Action == nil || m.Items[1].Disabled {
		t.Errorf("menu = %+v", m.Items)
	}
	m = tr.menu(trayState{connection: fake.HomeSSID, verdict: "ok"})
	for _, it := range m.Items {
		if strings.HasPrefix(it.Label, "Password needed") {
			t.Error("no entry without a pending request")
		}
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := secretCountdown(now.Add(90*time.Second), now); got != "Expires in 1m30s" {
		t.Errorf("countdown = %q", got)
	}
	if got := secretCountdown(now.Add(-time.Second), now); got != "Expired" {
		t.Errorf("countdown = %q", got)
	}
}
