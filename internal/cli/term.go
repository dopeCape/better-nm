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

// readPassword prompts on errw and reads a line from in without echo when in
// is a terminal; otherwise it reads a plain line (so pipes and tests work).
func readPassword(in io.Reader, errw io.Writer, prompt string) (string, error) {
	fmt.Fprint(errw, prompt)
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(errw)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
