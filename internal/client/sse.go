package client

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// sseEvent is one parsed Server-Sent Event.
type sseEvent struct {
	Name string
	Data []byte
}

// readSSE parses a text/event-stream body, calling fn per event. Comment lines
// (heartbeats) are skipped. It returns nil at EOF or when fn returns false.
func readSSE(r io.Reader, fn func(sseEvent) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var name string
	var data bytes.Buffer
	flush := func() bool {
		if name == "" && data.Len() == 0 {
			return true
		}
		ev := sseEvent{Name: name, Data: bytes.TrimSuffix(data.Bytes(), []byte("\n"))}
		ev.Data = append([]byte(nil), ev.Data...)
		name = ""
		data.Reset()
		return fn(ev)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if !flush() {
				return nil
			}
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				name = value
			case "data":
				data.WriteString(value)
				data.WriteByte('\n')
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	flush()
	return nil
}
