package desktop

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
)

// Secret prompts: the daemon, as NetworkManager's secret agent, emits
// secret-needed when it needs a password, an OTP or a VPN login. The app
// fetches the request and opens a modal dialog (showing the window first if
// it is hidden to the tray); requests queue and show one after the other, and
// secret-resolved closes a prompt that was answered elsewhere, cancelled or
// timed out. All prompt state lives on the UI thread; the countdown ticks
// from its own goroutine through onUI.

// secretsClient is what the prompt needs from the daemon client.
type secretsClient interface {
	PendingSecrets(ctx context.Context) ([]core.SecretRequest, error)
	Secret(ctx context.Context, id string) (core.SecretRequest, error)
	AnswerSecret(ctx context.Context, id string, a core.SecretAnswer) error
	CancelSecret(ctx context.Context, id string) error
}

var _ secretsClient = (*client.Client)(nil)

// secretPrompt is the open dialog for one request.
type secretPrompt struct {
	req       core.SecretRequest
	entries   []*widget.Entry // one per req.Fields
	wrong     *widget.Label   // "rejected" line, nil unless RequestNew
	save      *widget.Check
	countdown *widget.Label
	errLbl    *errorLabel
	submit    *widget.Button
	cancel    *widget.Button
	dlg       dialog.Dialog
	stop      chan struct{}
	sending   bool
}

// secretTitle is the dialog title: the connection, or for a VPN the plugin's
// own message when it sent one.
func secretTitle(r core.SecretRequest) string {
	name := r.ConnectionName
	if name == "" {
		name = r.SSID
	}
	if name == "" {
		name = "a connection"
	}
	if r.VPN && r.Message != "" {
		return name + " · " + r.Message
	}
	return "Password needed for " + name
}

// secretCountdown formats the time left before NM gives up on the request.
func secretCountdown(expires, now time.Time) string {
	if expires.IsZero() {
		return ""
	}
	d := expires.Sub(now).Round(time.Second)
	if d <= 0 {
		return "Expired"
	}
	if d >= time.Minute {
		return fmt.Sprintf("Expires in %dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	}
	return fmt.Sprintf("Expires in %ds", int(d/time.Second))
}

// loadPendingSecrets picks up requests raised while the app was not listening.
func (a *App) loadPendingSecrets() {
	a.bg(func(ctx context.Context) {
		reqs, err := a.c.PendingSecrets(ctx)
		if err != nil {
			return
		}
		a.onUI(func() {
			for _, r := range reqs {
				a.enqueueSecret(r)
			}
		})
	})
}

// fetchSecret reads one request after its secret-needed event.
func (a *App) fetchSecret(id string) {
	a.bg(func(ctx context.Context) {
		req, err := a.c.Secret(ctx, id)
		if err != nil {
			return // resolved before we got to it
		}
		a.onUI(func() { a.enqueueSecret(req) })
	})
}

// enqueueSecret shows req now or after the ones ahead of it. UI thread.
func (a *App) enqueueSecret(req core.SecretRequest) {
	if req.ID == "" || a.secretKnown[req.ID] {
		return
	}
	a.secretKnown[req.ID] = true
	if a.secret != nil {
		a.secretQueue = append(a.secretQueue, req)
		a.updateSecretTray()
		return
	}
	a.showSecret(req)
}

// secretResolved is the secret-resolved event. UI thread.
func (a *App) secretResolved(id string, outcome core.SecretOutcome) {
	delete(a.secretKnown, id)
	for i, r := range a.secretQueue {
		if r.ID == id {
			a.secretQueue = append(a.secretQueue[:i], a.secretQueue[i+1:]...)
			break
		}
	}
	if a.secret != nil && a.secret.req.ID == id {
		a.closeSecret()
		a.nextSecret()
	}
	a.updateSecretTray()
}

// showSecret builds and opens the dialog for req. UI thread.
func (a *App) showSecret(req core.SecretRequest) {
	p := &secretPrompt{req: req, stop: make(chan struct{})}
	form := container.NewVBox()
	if req.VPN && req.Message == "" && req.VPNKind != "" {
		form.Add(caption(req.VPNKind + " VPN"))
	}
	if req.RequestNew {
		p.wrong = widget.NewLabel("The saved password was rejected")
		p.wrong.Importance = widget.DangerImportance
		form.Add(p.wrong)
	}
	for i, f := range req.Fields {
		var e *widget.Entry
		if f.Secret {
			e = widget.NewPasswordEntry()
		} else {
			e = widget.NewEntry()
		}
		label := f.Label
		if label == "" {
			label = f.Key
		}
		e.SetPlaceHolder(label)
		idx := i
		e.OnSubmitted = func(string) {
			if idx < len(p.entries)-1 {
				a.win.Canvas().Focus(p.entries[idx+1])
				return
			}
			a.submitSecret(p)
		}
		p.entries = append(p.entries, e)
		form.Add(widget.NewLabel(label))
		form.Add(e)
	}
	p.save = widget.NewCheck("Save password", nil)
	p.save.SetChecked(true)
	form.Add(p.save)
	p.errLbl = newErrorLabel()
	form.Add(p.errLbl)
	p.countdown = caption(secretCountdown(req.ExpiresAt, time.Now()))
	p.submit = widget.NewButton("Submit", func() { a.submitSecret(p) })
	p.submit.Importance = widget.HighImportance
	p.cancel = widget.NewButton("Cancel", func() { a.cancelSecret(p) })
	buttons := container.NewHBox(p.countdown, spacer(), p.cancel, p.submit)
	content := container.NewVBox(form, inset(buttons, 12, 0, 0, 0))
	p.dlg = dialog.NewCustomWithoutButtons(secretTitle(req), fixedWidth(content, 400), a.win)
	a.secret = p
	a.win.Show()
	a.win.RequestFocus()
	p.dlg.Show()
	if len(p.entries) > 0 {
		a.win.Canvas().Focus(p.entries[0])
	}
	a.updateSecretTray()
	go a.tickSecret(p)
}

// tickSecret refreshes the countdown once a second until the prompt closes.
func (a *App) tickSecret(p *secretPrompt) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
		a.onUI(func() {
			if a.secret == p {
				p.countdown.SetText(secretCountdown(p.req.ExpiresAt, time.Now()))
			}
		})
	}
}

// submitSecret sends the answer; the dialog stays up until the daemon takes it.
func (a *App) submitSecret(p *secretPrompt) {
	if a.secret != p || p.sending {
		return
	}
	ans := core.SecretAnswer{Secrets: map[string]string{}, Save: p.save.Checked}
	for i, f := range p.req.Fields {
		v := p.entries[i].Text
		if v == "" {
			a.win.Canvas().Focus(p.entries[i])
			return
		}
		ans.Secrets[f.Key] = v
	}
	p.sending = true
	p.errLbl.set(nil)
	p.submit.Disable()
	p.cancel.Disable()
	id := p.req.ID
	a.bg(func(ctx context.Context) {
		err := a.c.AnswerSecret(ctx, id, ans)
		a.onUI(func() {
			if a.secret != p {
				return
			}
			p.sending = false
			if err != nil {
				p.errLbl.set(err)
				p.submit.Enable()
				p.cancel.Enable()
				return
			}
			a.closeSecret()
			a.nextSecret()
		})
	})
}

// cancelSecret closes the prompt and tells the daemon nobody will answer.
func (a *App) cancelSecret(p *secretPrompt) {
	if a.secret != p {
		return
	}
	id := p.req.ID
	a.closeSecret()
	a.nextSecret()
	a.bg(func(ctx context.Context) {
		if err := a.c.CancelSecret(ctx, id); err != nil {
			a.log.Debug("cancel secret", "id", id, "err", err)
		}
	})
}

// closeSecret hides the open dialog. UI thread.
func (a *App) closeSecret() {
	p := a.secret
	if p == nil {
		return
	}
	close(p.stop)
	p.dlg.Hide()
	delete(a.secretKnown, p.req.ID)
	a.secret = nil
	a.updateSecretTray()
}

// nextSecret opens the first queued request, if any. UI thread.
func (a *App) nextSecret() {
	if a.secret != nil || len(a.secretQueue) == 0 {
		return
	}
	req := a.secretQueue[0]
	a.secretQueue = a.secretQueue[1:]
	a.showSecret(req)
}

// secretPending counts the requests waiting on this user. UI thread.
func (a *App) secretPending() int {
	n := len(a.secretQueue)
	if a.secret != nil {
		n++
	}
	return n
}

// updateSecretTray keeps the tray's "Password needed for …" entry in step.
func (a *App) updateSecretTray() {
	if a.tray == nil {
		return
	}
	label := ""
	if a.secret != nil {
		label = secretTitle(a.secret.req)
		if n := len(a.secretQueue); n > 0 {
			label += fmt.Sprintf(" (+%d)", n)
		}
	}
	a.tray.setSecret(label)
}

// onSecretEvent routes the secret-agent events off the stream goroutine.
func (a *App) onSecretEvent(e core.Event) {
	id := e.Data["request_id"]
	if id == "" {
		return
	}
	switch e.Type {
	case core.EventSecretNeeded:
		a.fetchSecret(id)
	case core.EventSecretResolved:
		outcome := core.SecretOutcome(e.Data["outcome"])
		a.bg(func(context.Context) {
			a.onUI(func() { a.secretResolved(id, outcome) })
		})
	}
}

// focusSecret brings the window and the open prompt to the front (tray entry).
func (a *App) focusSecret() {
	a.win.Show()
	a.win.RequestFocus()
	if a.secret != nil && len(a.secret.entries) > 0 {
		a.win.Canvas().Focus(a.secret.entries[0])
	}
}
