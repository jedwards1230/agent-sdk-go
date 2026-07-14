package exec_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/exec"
	"github.com/jedwards1230/agent-sdk-go/internal/goldenio"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/provider/faux"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// goldenID and goldenClock pin the deterministic seams so the emitted stream is
// byte-identical across runs.
const goldenID = "0192a1b2-c3d4-7e5f-8a90-000000000001"

func goldenClock() time.Time { return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) }

func newFauxSession(t *testing.T, script faux.Script) *session.Session {
	t.Helper()
	return session.New(
		faux.New(script),
		session.WithIDGen(func() string { return goldenID }),
		session.WithClock(goldenClock),
	)
}

// TestRunGolden proves Run streams the standard event contract as JSONL and
// reports the run summary from the terminal turn.finished.
func TestRunGolden(t *testing.T) {
	ctx := context.Background()
	sess := newFauxSession(t, faux.Default())
	defer sess.Close()

	var buf bytes.Buffer
	res, err := exec.Run(ctx, sess, "hello", exec.Options{Out: &buf})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	goldenio.Assert(t, filepath.Join("testdata", "run.golden.jsonl"), buf.Bytes())

	if got, want := res.SessionID, goldenID; got != want {
		t.Errorf("SessionID = %q, want %q", got, want)
	}
	if got, want := res.Final, "Hello! How can I help you today?"; got != want {
		t.Errorf("Final = %q, want %q", got, want)
	}
	if got, want := res.StopReason, "end_turn"; got != want {
		t.Errorf("StopReason = %q, want %q", got, want)
	}
	if res.Events <= 0 {
		t.Errorf("Events = %d, want > 0", res.Events)
	}
}

// jsonTurn scripts a single text-only turn whose final content is exactly text.
func jsonTurn(text string) faux.Script {
	return faux.Script{Turns: []faux.Turn{{
		Text:       []string{text},
		StopReason: provider.StopEndTurn,
	}}}
}

// TestRunOutputSchemaAccept covers the happy path: a final JSON object that
// satisfies the schema returns a nil error.
func TestRunOutputSchemaAccept(t *testing.T) {
	const final = `{"answer":"42","done":true}`
	schemaDoc := []byte(`{
		"type": "object",
		"properties": {
			"answer": {"type": "string"},
			"done":   {"type": "boolean"}
		},
		"required": ["answer", "done"]
	}`)

	sess := newFauxSession(t, jsonTurn(final))
	defer sess.Close()

	res, err := exec.Run(context.Background(), sess, "go", exec.Options{Out: io.Discard, OutputSchema: schemaDoc})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if res.Final != final {
		t.Errorf("Final = %q, want %q", res.Final, final)
	}
}

// TestRunOutputSchemaReject covers the mismatch paths: a schema violation and a
// non-JSON final result each surface a *SchemaError with Result populated.
func TestRunOutputSchemaReject(t *testing.T) {
	tests := []struct {
		name     string
		final    string
		schema   string
		wantPath string
		wantMsg  string
	}{
		{
			name:     "missing required property",
			final:    `{"answer":"42"}`,
			schema:   `{"type":"object","properties":{"answer":{"type":"string"},"done":{"type":"boolean"}},"required":["answer","done"]}`,
			wantPath: "/done",
			wantMsg:  "missing required property",
		},
		{
			name:     "wrong type",
			final:    `{"answer":42}`,
			schema:   `{"type":"object","properties":{"answer":{"type":"string"}}}`,
			wantPath: "/answer",
			wantMsg:  "expected string, got integer",
		},
		{
			name:     "not valid JSON",
			final:    `not json at all`,
			schema:   `{"type":"object"}`,
			wantPath: "",
			wantMsg:  "final result is not valid JSON",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := newFauxSession(t, jsonTurn(tt.final))
			defer sess.Close()

			res, err := exec.Run(context.Background(), sess, "go", exec.Options{Out: io.Discard, OutputSchema: []byte(tt.schema)})
			var se *exec.SchemaError
			if !errors.As(err, &se) {
				t.Fatalf("error = %v, want *SchemaError", err)
			}
			if se.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", se.Path, tt.wantPath)
			}
			if se.Msg != tt.wantMsg {
				t.Errorf("Msg = %q, want %q", se.Msg, tt.wantMsg)
			}
			// Result is still populated on a schema mismatch.
			if res.Final != tt.final {
				t.Errorf("Final = %q, want %q", res.Final, tt.final)
			}
			if res.StopReason != "end_turn" {
				t.Errorf("StopReason = %q, want end_turn", res.StopReason)
			}
		})
	}
}

// TestRunCompileSchemaError asserts a malformed schema fails before any run.
func TestRunCompileSchemaError(t *testing.T) {
	sess := newFauxSession(t, faux.Default())
	defer sess.Close()

	_, err := exec.Run(context.Background(), sess, "hello", exec.Options{
		Out:          io.Discard,
		OutputSchema: []byte(`{"type": "widget"}`),
	})
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
	var se *exec.SchemaError
	if errors.As(err, &se) {
		t.Errorf("compile error should not be a *SchemaError, got %v", err)
	}
}

// TestRunTerminatesAtTurnFinished sanity-checks that the drainer stops at the
// terminal event and the event count matches the golden stream length.
func TestRunTerminatesAtTurnFinished(t *testing.T) {
	sess := newFauxSession(t, faux.Default())
	defer sess.Close()

	var buf bytes.Buffer
	res, err := exec.Run(context.Background(), sess, "hello", exec.Options{Out: &buf})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// session.created + turn.started + (reasoning started/2 deltas/finished) +
	// (text started/3 deltas/finished) + turn.finished = 12.
	if got, want := res.Events, 12; got != want {
		t.Errorf("Events = %d, want %d", got, want)
	}
	if last := lastKind(t, buf.Bytes()); last != event.KindTurnFinished {
		t.Errorf("last event kind = %q, want %q", last, event.KindTurnFinished)
	}
}

// lastKind decodes the "type" of the last JSONL line.
func lastKind(t *testing.T, jsonl []byte) string {
	t.Helper()
	lines := bytes.Split(bytes.TrimRight(jsonl, "\n"), []byte("\n"))
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(lines[len(lines)-1], &env); err != nil {
		t.Fatalf("decode last line: %v", err)
	}
	return env.Type
}
