package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
)

// Secret prompts. bnmd is NetworkManager's secret agent: when NM needs a
// password the profile does not hold (a wrong password being retried, an OTP,
// an unsaved VPN password) the daemon publishes a secret-needed event and
// keeps the request open until a surface answers it. `bnm secrets` lists and
// answers those from any shell; `wifi connect` and `vpn up` follow the event
// stream while their request runs and answer the prompts meant for them on
// the terminal.

// stdinInteractive says whether prompts can be shown; tests override it.
var stdinInteractive = func(a *app) bool { return a.stdinIsTerminal() }

func (a *app) secretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Password prompts NetworkManager is waiting on: list, answer, cancel",
		Long: `NetworkManager asks bnmd (its secret agent) when an activation needs a password
the profile does not store: a rejected Wi-Fi password, a one-time code, an
unsaved VPN password. "bnm secrets" lists the open prompts, "bnm secrets answer"
types the answer in, "bnm secrets cancel" gives up (the activation then fails).`,
		Args: a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.secretsList(cmd.Context())
		},
	}
	cmd.AddCommand(a.secretsListCmd(), a.secretsAnswerCmd(), a.secretsCancelCmd())
	return cmd
}

func (a *app) secretsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List open password prompts",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.secretsList(cmd.Context())
		},
	}
}

func (a *app) secretsList(ctx context.Context) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	reqs, err := c.PendingSecrets(ctx)
	if err != nil {
		return err
	}
	sort.SliceStable(reqs, func(i, j int) bool { return reqs[i].CreatedAt.Before(reqs[j].CreatedAt) })
	if a.jsonOut {
		return a.printJSON(nonNil(reqs))
	}
	if len(reqs) == 0 {
		fmt.Fprintln(a.out, a.ui.dim.Render("no password prompts are waiting"))
		return nil
	}
	u := a.ui
	t := u.table("ID", "FOR", "KIND", "ASKS", "NOTE")
	now := time.Now()
	for _, r := range reqs {
		t.add(r.ID, truncate(secretSubject(r), 28), secretKind(r), secretAsks(r), u.dim.Render(secretNote(r, now)))
	}
	t.render(a.out)
	fmt.Fprintln(a.out, u.dim.Render("answer with: bnm secrets answer <id>"))
	return nil
}

// secretSubject names what the prompt is for.
func secretSubject(r core.SecretRequest) string {
	switch {
	case r.SSID != "":
		return r.SSID
	case r.ConnectionName != "":
		return r.ConnectionName
	}
	return r.ConnectionUUID
}

func secretKind(r core.SecretRequest) string {
	switch {
	case r.VPN && r.VPNKind != "":
		return r.VPNKind
	case r.VPN:
		return "VPN"
	case r.SSID != "" || r.SettingName == "802-11-wireless-security" || r.SettingName == "802-1x":
		return "Wi-Fi"
	}
	return r.SettingName
}

func secretAsks(r core.SecretRequest) string {
	labels := make([]string, 0, len(r.Fields))
	for _, f := range r.Fields {
		labels = append(labels, f.Label)
	}
	return strings.Join(labels, ", ")
}

func secretNote(r core.SecretRequest, now time.Time) string {
	var parts []string
	if r.RequestNew {
		parts = append(parts, "previous answer rejected")
	}
	if r.Message != "" {
		parts = append(parts, strings.ReplaceAll(r.Message, "\n", " "))
	}
	if !r.ExpiresAt.IsZero() {
		if left := r.ExpiresAt.Sub(now); left > 0 {
			parts = append(parts, "expires in "+shortDuration(left.Round(time.Second)))
		} else {
			parts = append(parts, "expired")
		}
	}
	return strings.Join(parts, " · ")
}

func (a *app) secretsAnswerCmd() *cobra.Command {
	var save, noSave bool
	cmd := &cobra.Command{
		Use:   "answer [id]",
		Short: "Type the answer to a prompt (the only open one when no id is given)",
		Args:  a.rangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			req, err := a.resolveSecret(ctx, c, id)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.errw, "%s\n", secretHeadline(req))
			secrets, cancelled, err := a.promptSecretFields(req)
			if err != nil {
				return err
			}
			if cancelled {
				if err := c.CancelSecret(ctx, req.ID); err != nil {
					return err
				}
				a.done("Cancelled the prompt for %s", secretSubject(req))
				return nil
			}
			if err := c.AnswerSecret(ctx, req.ID, core.SecretAnswer{Secrets: secrets, Save: save && !noSave}); err != nil {
				return err
			}
			a.done("%s Answered the prompt for %s", a.ui.dot("green"), secretSubject(req))
			return nil
		},
	}
	cmd.Flags().BoolVar(&save, "save", true, "store the answer in the profile so the next activation does not ask")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "use the answer for this activation only")
	return cmd
}

func (a *app) secretsCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <id>",
		Short: "Give up on a prompt (the activation fails)",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			req, err := a.resolveSecret(ctx, c, args[0])
			if err != nil {
				return err
			}
			if err := c.CancelSecret(ctx, req.ID); err != nil {
				return err
			}
			a.done("Cancelled the prompt for %s", secretSubject(req))
			return nil
		},
	}
}

// resolveSecret picks the request an id (or id prefix) names; with no id the
// only open request.
func (a *app) resolveSecret(ctx context.Context, c *client.Client, id string) (core.SecretRequest, error) {
	reqs, err := c.PendingSecrets(ctx)
	if err != nil {
		return core.SecretRequest{}, err
	}
	if id == "" {
		switch len(reqs) {
		case 0:
			return core.SecretRequest{}, core.Errorf(core.KindNotFound, "", "no password prompt is waiting")
		case 1:
			return reqs[0], nil
		}
		ids := make([]string, 0, len(reqs))
		for _, r := range reqs {
			ids = append(ids, r.ID+" ("+secretSubject(r)+")")
		}
		return core.SecretRequest{}, core.Errorf(core.KindInvalid, "name one: "+strings.Join(ids, ", "), "%d prompts are waiting", len(reqs))
	}
	var hits []core.SecretRequest
	for _, r := range reqs {
		if r.ID == id {
			return r, nil
		}
		if len(id) >= 3 && strings.HasPrefix(r.ID, id) {
			hits = append(hits, r)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		return core.SecretRequest{}, core.Errorf(core.KindInvalid, "use a longer prefix", "%q matches %d prompts", id, len(hits))
	}
	return core.SecretRequest{}, core.Errorf(core.KindNotFound, "run `bnm secrets`", "no open prompt %q (it may have been answered or expired)", id)
}

// secretHeadline is the line printed above a prompt.
func secretHeadline(r core.SecretRequest) string {
	subject := secretSubject(r)
	var b strings.Builder
	switch {
	case r.RequestNew && !r.VPN:
		fmt.Fprintf(&b, "Wrong password for %s; try again.", subject)
	case r.RequestNew:
		fmt.Fprintf(&b, "%s rejected the previous answer; try again.", subject)
	case r.VPN && r.VPNKind != "":
		fmt.Fprintf(&b, "%s (%s) needs %s.", subject, r.VPNKind, strings.ToLower(secretAsks(r)))
	default:
		fmt.Fprintf(&b, "%s needs %s.", subject, strings.ToLower(secretAsks(r)))
	}
	if r.Message != "" {
		b.WriteString("\n" + r.Message)
	}
	return b.String()
}

// promptSecretFields asks for every field on stdin (masked for secrets when
// stdin is a terminal). An empty secret means "give up": cancelled is true.
func (a *app) promptSecretFields(r core.SecretRequest) (map[string]string, bool, error) {
	subject := secretSubject(r)
	secrets := map[string]string{}
	for _, f := range r.Fields {
		prompt := fmt.Sprintf("%s for %s: ", f.Label, subject)
		var v string
		var err error
		if f.Secret {
			v, err = readPassword(a.lines(), a.errw, prompt)
		} else {
			v, err = readLine(a.lines(), a.errw, prompt)
		}
		if err != nil {
			return nil, false, err
		}
		if v == "" && f.Secret {
			return nil, true, nil
		}
		secrets[f.Key] = v
	}
	return secrets, false, nil
}

// promptOpts drives runWithPrompts.
type promptOpts struct {
	// match says whether a request is about the thing being connected.
	match func(core.SecretRequest) bool
	// preset answers (from flags) are used once for a first, non-retry request.
	preset map[string]string
	// presetUsed starts true when the preset already went into the profile,
	// so a retry means it was wrong rather than "not yet given".
	presetUsed bool
}

// runWithPrompts runs do while following the event stream: a secret-needed
// event whose request matches is answered from the preset once, then from the
// terminal; without a terminal the request is left open, the `bnm secrets`
// hint is printed and the command fails.
func (a *app) runWithPrompts(ctx context.Context, c *client.Client, o promptOpts, do func(context.Context) error) error {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.Events(rctx)
	if err != nil {
		// No stream, no prompts: run the request plainly.
		return do(ctx)
	}
	errc := make(chan error, 1)
	go func() { errc <- do(rctx) }()
	seen := map[string]bool{}
	for {
		select {
		case err := <-errc:
			return err
		case item, ok := <-stream:
			if !ok {
				return <-errc
			}
			if item.Event == nil || item.Event.Type != core.EventSecretNeeded {
				continue
			}
			req, err := c.Secret(ctx, item.Event.Data["request_id"])
			if err != nil || !o.match(req) {
				continue
			}
			retry := req.RequestNew || seen[req.SettingName]
			seen[req.SettingName] = true
			err, gaveUp := a.answerPrompt(ctx, c, req, &o, retry)
			if err == nil {
				continue
			}
			if gaveUp {
				// The prompt was cancelled: let NM fail the activation (and
				// the daemon clean up a brand-new profile) before returning.
				select {
				case <-errc:
					return err
				case <-time.After(15 * time.Second):
				}
			}
			cancel()
			<-errc
			return err
		}
	}
}

// answerPrompt answers one matching request: preset first, then the terminal.
// gaveUp reports that the request was cancelled here (the activation is
// about to fail) rather than left open or failed some other way.
func (a *app) answerPrompt(ctx context.Context, c *client.Client, req core.SecretRequest, o *promptOpts, retry bool) (err error, gaveUp bool) {
	subject := secretSubject(req)
	if !retry && !o.presetUsed && len(o.preset) > 0 {
		secrets := map[string]string{}
		for _, f := range req.Fields {
			if v, ok := o.preset[f.Key]; ok && v != "" {
				secrets[f.Key] = v
			}
		}
		if len(secrets) > 0 {
			o.presetUsed = true
			return c.AnswerSecret(ctx, req.ID, core.SecretAnswer{Secrets: secrets, Save: true}), false
		}
	}
	if !stdinInteractive(a) {
		if retry && (o.presetUsed || len(o.preset) > 0) {
			// The password given by flag was rejected; nothing else can fix it.
			_ = c.CancelSecret(ctx, req.ID)
			return core.Errorf(core.KindInvalid, "check the password and try again", "wrong password for %s", subject), true
		}
		return core.Errorf(core.KindInvalid, "run: bnm secrets answer "+req.ID,
			"%s needs %s and there is no terminal to ask on", subject, strings.ToLower(secretAsks(req))), false
	}
	if retry && !req.RequestNew {
		req.RequestNew = true
	}
	fmt.Fprintln(a.errw, secretHeadline(req))
	secrets, cancelled, err := a.promptSecretFields(req)
	if err != nil {
		return err, false
	}
	if cancelled {
		if err := c.CancelSecret(ctx, req.ID); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err, false
		}
		return core.Errorf(core.KindInvalid, "", "no password given for %s", subject), true
	}
	return c.AnswerSecret(ctx, req.ID, core.SecretAnswer{Secrets: secrets, Save: true}), false
}
