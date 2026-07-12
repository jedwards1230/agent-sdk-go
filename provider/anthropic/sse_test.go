package anthropic

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// scanAll drains an sseScanner into a slice of payload strings.
func scanAll(t *testing.T, r io.Reader) []string {
	t.Helper()
	s := newSSEScanner(r)
	var out []string
	for {
		p, err := s.next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, string(p))
	}
}

func TestSSEBasicFrames(t *testing.T) {
	body := "event: a\ndata: {\"x\":1}\n\nevent: b\ndata: {\"y\":2}\n\n"
	got := scanAll(t, strings.NewReader(body))
	want := []string{`{"x":1}`, `{"y":2}`}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("frames = %#v, want %#v", got, want)
	}
}

func TestSSETrailingFrameNoBlankLine(t *testing.T) {
	// A final event not terminated by a blank line must still flush.
	body := "event: a\ndata: {\"x\":1}\n\nevent: b\ndata: {\"y\":2}"
	got := scanAll(t, strings.NewReader(body))
	if len(got) != 2 || got[1] != `{"y":2}` {
		t.Errorf("frames = %#v, want trailing frame flushed", got)
	}
}

func TestSSEMultiLineDataJoinedWithNewline(t *testing.T) {
	// Two data: lines in one event join with a newline (SSE spec).
	body := "data: {\"a\":1,\ndata: \"b\":2}\n\n"
	got := scanAll(t, strings.NewReader(body))
	if len(got) != 1 || got[0] != "{\"a\":1,\n\"b\":2}" {
		t.Errorf("payload = %q, want newline-joined", got)
	}
}

func TestSSECommentAndFieldLinesIgnored(t *testing.T) {
	// A leading ':' comment (heartbeat) and non-data field lines are skipped.
	body := ": ping\nid: 7\nevent: a\ndata: {\"x\":1}\nretry: 100\n\n"
	got := scanAll(t, strings.NewReader(body))
	if len(got) != 1 || got[0] != `{"x":1}` {
		t.Errorf("frames = %#v, want only the data payload", got)
	}
}

func TestSSEDataWithoutLeadingSpace(t *testing.T) {
	// The single optional leading space after "data:" is stripped; further
	// spaces are preserved.
	body := "data:{\"x\":1}\n\ndata:  spaced\n\n"
	got := scanAll(t, strings.NewReader(body))
	if len(got) != 2 || got[0] != `{"x":1}` || got[1] != " spaced" {
		t.Errorf("frames = %#v", got)
	}
}

func TestSSELargeLineWithinCap(t *testing.T) {
	// A single data line near (but under) the cap is handled — exercises the
	// enlarged scanner buffer for big tool-use inputs.
	big := strings.Repeat("x", 500<<10)
	body := "data: {\"blob\":\"" + big + "\"}\n\n"
	got := scanAll(t, strings.NewReader(body))
	if len(got) != 1 || !strings.Contains(got[0], big) {
		t.Errorf("large frame not returned intact (len %d)", len(got))
	}
}

func TestSSEPayloadIsCopied(t *testing.T) {
	// Successive next() calls must not alias a shared buffer.
	s := newSSEScanner(strings.NewReader("data: first\n\ndata: second\n\n"))
	p1, err := s.next()
	if err != nil {
		t.Fatalf("next 1: %v", err)
	}
	first := string(p1)
	if _, err := s.next(); err != nil {
		t.Fatalf("next 2: %v", err)
	}
	if string(p1) != first {
		t.Errorf("first payload mutated after second scan: %q != %q", p1, first)
	}
}
