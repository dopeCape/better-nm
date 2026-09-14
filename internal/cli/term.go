package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// isTerminal reports whether f is an interactive terminal.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// lineReader reads prompt answers from stdin. One buffered reader lives for
// the whole invocation so consecutive prompts (several secret fields, a
// retry) each get their own line from a pipe; on a terminal passwords are
// read without echo straight from the file descriptor.
type lineReader struct {
	raw io.Reader
	buf *bufio.Reader
}

func newLineReader(in io.Reader) *lineReader {
	return &lineReader{raw: in, buf: bufio.NewReader(in)}
}

// terminal returns the underlying file when stdin is an interactive terminal.
func (l *lineReader) terminal() (*os.File, bool) {
	f, ok := l.raw.(*os.File)
	if ok && isTerminal(f) {
		return f, true
	}
	return nil, false
}

// line reads one line (without its newline). EOF with nothing read is an error.
func (l *lineReader) line() (string, error) {
	line, err := l.buf.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readPassword prompts on errw and reads a line without echo when stdin is a
// terminal; otherwise it reads a plain line (so pipes and tests work).
func readPassword(in *lineReader, errw io.Writer, prompt string) (string, error) {
	fmt.Fprint(errw, prompt)
	if f, ok := in.terminal(); ok {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(errw)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return string(b), nil
	}
	s, err := in.line()
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return s, nil
}

// readLine prompts on errw and reads one echoed line.
func readLine(in *lineReader, errw io.Writer, prompt string) (string, error) {
	fmt.Fprint(errw, prompt)
	s, err := in.line()
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	return s, nil
}
