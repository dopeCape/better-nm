package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dopeCape/better-nm/internal/client"
	"github.com/dopeCape/better-nm/internal/core"
)

// Live refresh: one stream of StreamItems from the daemon, read one item per
// Cmd so the program loop owns the goroutine; Change hints are coalesced for
// debounceWindow and then only the affected data is reloaded. When the stream
// drops the status bar says "reconnecting…" and the open is retried with
// backoff until ctx ends.

const (
	debounceWindow = 250 * time.Millisecond
	backoffMin     = 500 * time.Millisecond
	backoffMax     = 10 * time.Second
	eventLogSize   = 50
)

type (
	streamOpenMsg   struct{ ch <-chan client.StreamItem }
	streamErrMsg    struct{ err error }
	streamItemMsg   struct{ item client.StreamItem }
	streamClosedMsg struct{}
	streamRetryMsg  struct{}
	debounceMsg     struct{}
)

func (l loader) openStream() tea.Cmd {
	return func() tea.Msg {
		ch, err := l.c.Events(l.ctx)
		if err != nil {
			return streamErrMsg{err}
		}
		return streamOpenMsg{ch}
	}
}

func waitStream(ch <-chan client.StreamItem) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		item, ok := <-ch
		if !ok {
			return streamClosedMsg{}
		}
		return streamItemMsg{item}
	}
}

func retryStream(after time.Duration) tea.Cmd {
	return tea.Tick(after, func(time.Time) tea.Msg { return streamRetryMsg{} })
}

func debounce() tea.Cmd {
	return tea.Tick(debounceWindow, func(time.Time) tea.Msg { return debounceMsg{} })
}

// stream is the model's view of the subscription.
type stream struct {
	ch        <-chan client.StreamItem
	up        bool
	everUp    bool
	backoff   time.Duration
	lastErr   error
	pending   map[core.ChangeKind]struct{}
	debouncin bool
}

func (s *stream) note(kind core.ChangeKind) {
	if s.pending == nil {
		s.pending = map[core.ChangeKind]struct{}{}
	}
	s.pending[kind] = struct{}{}
}

func (s *stream) take() map[core.ChangeKind]struct{} {
	p := s.pending
	s.pending = nil
	return p
}
