package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
)

// Secret prompts: when the daemon (as NetworkManager's secret agent) needs a
// password, an OTP or a VPN login it emits secret-needed; the TUI fetches the
// request and opens a modal over whatever tab is showing. Requests queue and
// show one after the other; one answered from another surface (the desktop
// app) closes here on secret-resolved. The inline Wi-Fi join prompt is
// separate: it sends the password with the connect request, and this modal
// handles the retry when NM rejects it.

// secretsClient is what the prompt needs from the daemon client.
type secretsClient interface {
	PendingSecrets(ctx context.Context) ([]core.SecretRequest, error)
	Secret(ctx context.Context, id string) (core.SecretRequest, error)
	AnswerSecret(ctx context.Context, id string, a core.SecretAnswer) error
	CancelSecret(ctx context.Context, id string) error
}

var _ secretsClient = (*client.Client)(nil)

type (
	pendingSecretsMsg struct {
		reqs []core.SecretRequest
		err  error
	}
	secretMsg struct {
		id  string
		req core.SecretRequest
		err error
	}
	secretAnsweredMsg struct {
		id  string
		err error
	}
	secretCancelledMsg struct {
		id  string
		err error
	}
	// secretTickMsg drives the countdown; gen ties it to one open prompt.
	secretTickMsg struct{ gen int }
)

func (l loader) pendingSecrets() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		reqs, err := l.c.PendingSecrets(ctx)
		return pendingSecretsMsg{reqs, err}
	}
}

func (l loader) secret(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		req, err := l.c.Secret(ctx, id)
		return secretMsg{id, req, err}
	}
}

func (l loader) answerSecret(id string, a core.SecretAnswer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		return secretAnsweredMsg{id, l.c.AnswerSecret(ctx, id, a)}
	}
}

func (l loader) cancelSecret(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := l.with()
		defer cancel()
		return secretCancelledMsg{id, l.c.CancelSecret(ctx, id)}
	}
}

// secretsState is the queue of requests and the prompt for the first one.
type secretsState struct {
	cur     *secretPrompt
	queue   []core.SecretRequest
	known   map[string]bool // ids shown or queued; resolved ids are dropped
	tickGen int
	tick    time.Duration // countdown period; tests shorten it
}

// secretPrompt is the open modal.
type secretPrompt struct {
	req     core.SecretRequest
	fields  []textinput.Model
	focus   int // index into fields; len(fields) is the save toggle
	save    bool
	sending bool
	err     error
}

func (s *secretsState) init() {
	s.known = map[string]bool{}
	s.tick = time.Second
}

// pending counts the requests waiting on this user (status bar).
func (s *secretsState) pending() int {
	n := len(s.queue)
	if s.cur != nil {
		n++
	}
	return n
}

func (s *secretsState) open() bool { return s.cur != nil }

// update handles the data messages; keys go through key.
func (s *secretsState) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case pendingSecretsMsg:
		if msg.err != nil {
			return nil
		}
		var cmds []tea.Cmd
		for _, r := range msg.reqs {
			cmds = append(cmds, s.enqueue(m, r))
		}
		return tea.Batch(cmds...)
	case secretMsg:
		if msg.err != nil {
			// resolved before we could fetch it, or the daemon is old
			return nil
		}
		return s.enqueue(m, msg.req)
	case secretAnsweredMsg:
		if s.cur == nil || s.cur.req.ID != msg.id {
			return nil
		}
		s.cur.sending = false
		if msg.err != nil {
			s.cur.err = msg.err
			return nil
		}
		s.close()
		return s.next(m)
	case secretCancelledMsg:
		if msg.err != nil && !errors.Is(msg.err, core.ErrNotFound) {
			m.setFlash("cancel: "+errText(msg.err), true)
			return m.flashTimer()
		}
		return nil
	case secretTickMsg:
		if s.cur == nil || msg.gen != s.tickGen {
			return nil
		}
		return s.tickCmd()
	}
	return nil
}

// enqueue shows req now or after the ones ahead of it.
func (s *secretsState) enqueue(m *Model, req core.SecretRequest) tea.Cmd {
	if req.ID == "" || s.known[req.ID] {
		return nil
	}
	s.known[req.ID] = true
	if s.cur != nil {
		s.queue = append(s.queue, req)
		return nil
	}
	return s.show(m, req)
}

// resolved is the secret-resolved event: the request ended (answered here or
// elsewhere, cancelled, timed out) so it leaves the queue or the screen.
func (s *secretsState) resolved(m *Model, id string, outcome core.SecretOutcome) tea.Cmd {
	delete(s.known, id)
	for i, r := range s.queue {
		if r.ID == id {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			break
		}
	}
	if s.cur == nil || s.cur.req.ID != id {
		return nil
	}
	name := s.cur.req.ConnectionName
	s.close()
	var cmds []tea.Cmd
	if outcome == core.SecretTimeout {
		m.setFlash("password request for "+name+" timed out", true)
		cmds = append(cmds, m.flashTimer())
	}
	cmds = append(cmds, s.next(m))
	return tea.Batch(cmds...)
}

func (s *secretsState) show(m *Model, req core.SecretRequest) tea.Cmd {
	p := &secretPrompt{req: req, save: true}
	for _, f := range req.Fields {
		label := f.Label
		if label == "" {
			label = f.Key
		}
		in := newInput(label+": ", "", 256)
		if f.Secret {
			in.EchoMode = textinput.EchoPassword
			in.EchoCharacter = '•'
		}
		p.fields = append(p.fields, in)
	}
	s.cur = p
	s.tickGen++
	cmds := []tea.Cmd{s.tickCmd()}
	if len(p.fields) > 0 {
		cmds = append(cmds, p.fields[0].Focus())
	} else {
		p.focus = 0
	}
	return tea.Batch(cmds...)
}

func (s *secretsState) close() {
	if s.cur != nil {
		delete(s.known, s.cur.req.ID)
	}
	s.cur = nil
	s.tickGen++
}

// next opens the first queued request, if any.
func (s *secretsState) next(m *Model) tea.Cmd {
	if s.cur != nil || len(s.queue) == 0 {
		return nil
	}
	req := s.queue[0]
	s.queue = s.queue[1:]
	return s.show(m, req)
}

func (s *secretsState) tickCmd() tea.Cmd {
	gen := s.tickGen
	return tea.Tick(s.tick, func(time.Time) tea.Msg { return secretTickMsg{gen} })
}

// key handles every key while the prompt is open.
func (s *secretsState) key(m *Model, k tea.KeyMsg) tea.Cmd {
	p := s.cur
	if p == nil {
		return nil
	}
	if p.sending {
		return nil
	}
	n := len(p.fields)
	switch k.String() {
	case "esc":
		id := p.req.ID
		s.close()
		return tea.Batch(m.l.cancelSecret(id), s.next(m))
	case "tab", "down":
		return p.setFocus((p.focus + 1) % (n + 1))
	case "shift+tab", "up":
		return p.setFocus((p.focus + n) % (n + 1))
	case " ":
		if p.focus == n {
			p.save = !p.save
			return nil
		}
	case "enter":
		if p.focus < n-1 {
			return p.setFocus(p.focus + 1)
		}
		return s.submit(m)
	}
	if p.focus < n {
		var cmd tea.Cmd
		p.fields[p.focus], cmd = p.fields[p.focus].Update(k)
		return cmd
	}
	return nil
}

func (p *secretPrompt) setFocus(i int) tea.Cmd {
	if p.focus < len(p.fields) {
		p.fields[p.focus].Blur()
	}
	p.focus = i
	if i < len(p.fields) {
		return p.fields[i].Focus()
	}
	return nil
}

// submit sends the answer once every field has a value.
func (s *secretsState) submit(m *Model) tea.Cmd {
	p := s.cur
	a := core.SecretAnswer{Secrets: map[string]string{}, Save: p.save}
	for i, f := range p.req.Fields {
		v := p.fields[i].Value()
		if v == "" {
			return p.setFocus(i)
		}
		a.Secrets[f.Key] = v
	}
	p.sending = true
	p.err = nil
	return m.l.answerSecret(p.req.ID, a)
}

func (s *secretsState) hints() string {
	if s.cur == nil {
		return ""
	}
	if s.cur.focus == len(s.cur.fields) {
		return keyHints("space", "toggle save", "enter", "submit", "tab", "next", "esc", "cancel")
	}
	return keyHints("enter", "submit", "tab", "next field", "esc", "cancel")
}

// title is the modal's first line.
func (p *secretPrompt) title() string {
	r := p.req
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

// view renders the modal box for a body w cells wide.
func (s *secretsState) view(m *Model, w int) string {
	p := s.cur
	// inner is the text width; the box adds its padding and border.
	inner := min(w-8, 62)
	if inner < 20 {
		inner = max(10, w-6)
	}
	var lines []string
	lines = append(lines, truncate(stTitle.Render(p.title()), inner))
	if p.req.VPN && p.req.Message == "" && p.req.VPNKind != "" {
		lines = append(lines, truncate(stDim.Render(p.req.VPNKind+" VPN"), inner))
	}
	if p.req.RequestNew {
		lines = append(lines, truncate(stBad.Render("The saved password was rejected"), inner))
	}
	lines = append(lines, "")
	for i := range p.fields {
		f := &p.fields[i]
		f.Width = max(4, inner-lipgloss.Width(f.Prompt)-2)
		lines = append(lines, truncate(f.View(), inner))
	}
	box := "[ ]"
	if p.save {
		box = "[x]"
	}
	save := box + " Save password"
	if p.focus == len(p.fields) {
		save = stSelected.Render(save)
	} else {
		save = stText.Render(save)
	}
	lines = append(lines, save)
	if p.err != nil {
		lines = append(lines, truncate(stBad.Render("✗ "+errText(p.err)), inner))
	}
	lines = append(lines, "")
	left := stDim.Render(countdown(p.req.ExpiresAt, m.now()))
	if p.sending {
		left = stAccent.Render("sending…")
	}
	right := ""
	if n := len(s.queue); n > 0 {
		right = stDim.Render(fmt.Sprintf("%d more waiting", n))
	}
	gap := inner - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		lines = append(lines, truncate(left, inner))
	} else {
		lines = append(lines, left+strings.Repeat(" ", gap)+right)
	}
	return stBox.Width(inner + 2).Render(strings.Join(lines, "\n"))
}

// countdown formats the time left before NM gives up on the request.
func countdown(expires, now time.Time) string {
	if expires.IsZero() {
		return ""
	}
	d := expires.Sub(now).Round(time.Second)
	if d <= 0 {
		return "expired"
	}
	if d >= time.Minute {
		return fmt.Sprintf("expires in %dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	}
	return fmt.Sprintf("expires in %ds", int(d/time.Second))
}

// overlayBox draws box centred over body (both ANSI-styled), keeping every
// line exactly w cells: the covered cells are replaced, the rest of the
// line keeps its styling because the cut preserves escape sequences.
func overlayBox(body, box string, w, h int) string {
	bl := strings.Split(clampBox(body, w, h), "\n")
	xl := strings.Split(box, "\n")
	bw := 0
	for _, l := range xl {
		bw = max(bw, lipgloss.Width(l))
	}
	if bw >= w {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
	}
	x0 := (w - bw) / 2
	y0 := max(0, (h-len(xl))/2)
	for i, l := range xl {
		y := y0 + i
		if y >= h {
			break
		}
		left := ansi.Truncate(bl[y], x0, "")
		right := ansi.TruncateLeft(bl[y], x0+bw, "")
		bl[y] = left + "\x1b[0m" + fit(l, bw) + right
	}
	return strings.Join(bl, "\n")
}
