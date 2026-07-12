package anthropic

import (
	"bufio"
	"bytes"
	"io"
)

// sseScanner reads a text/event-stream and yields the JSON payload of each
// event: the concatenation of its data: lines. The event: name line is not
// needed — every Messages API frame carries a "type" field in its data — so it
// is skipped. next returns io.EOF at the end of the stream.
type sseScanner struct {
	sc   *bufio.Scanner
	data bytes.Buffer
}

// maxSSELine bounds a single SSE line. Anthropic frames are small, but a large
// tool-use input arrives across many input_json_delta frames, so per-line this
// ceiling is comfortable.
const maxSSELine = 1 << 20

func newSSEScanner(r io.Reader) *sseScanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)
	return &sseScanner{sc: sc}
}

// next returns the next event's data payload. It accumulates data: lines until a
// blank line terminates the event, then returns the buffered bytes. Frames with
// no data: line (e.g. a lone comment) are skipped.
func (s *sseScanner) next() ([]byte, error) {
	s.data.Reset()
	for s.sc.Scan() {
		line := s.sc.Bytes()

		if len(line) == 0 {
			// End of an event. Emit if we accumulated any data.
			if s.data.Len() > 0 {
				return s.payload(), nil
			}
			continue
		}
		if line[0] == ':' {
			// Comment / heartbeat line.
			continue
		}
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			// Per the SSE spec, multiple data: lines in one event join with a
			// newline. Anthropic sends one line per frame, but a proxy could
			// re-wrap a long payload, and gluing fragments would corrupt JSON.
			if s.data.Len() > 0 {
				s.data.WriteByte('\n')
			}
			s.data.Write(bytes.TrimPrefix(rest, []byte(" ")))
		}
		// event:, id:, retry: and any other field lines are ignored.
	}

	if err := s.sc.Err(); err != nil {
		return nil, err
	}
	// Stream ended. Flush a trailing event that was not blank-line terminated.
	if s.data.Len() > 0 {
		return s.payload(), nil
	}
	return nil, io.EOF
}

// payload returns a copy of the accumulated data buffer so the caller may retain
// it after the next Reset.
func (s *sseScanner) payload() []byte {
	out := make([]byte, s.data.Len())
	copy(out, s.data.Bytes())
	return out
}
