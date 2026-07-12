// Package goldenio is a test helper for golden-file event-stream assertions. It
// runs a session to completion, serializes every emitted event to JSONL, and
// compares against a golden file — or rewrites it under -update.
package goldenio

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// update, when set via `go test -update`, rewrites golden files instead of
// comparing against them. Only test binaries that import this package define
// the flag, so `go test ./...` stays clean for packages without golden tests.
var update = flag.Bool("update", false, "rewrite golden files instead of comparing")

// collectTimeout bounds how long Collect waits for a session to finish a turn.
const collectTimeout = 2 * time.Second

// Collect subscribes to every event of sess, runs one prompt, and returns the
// event stream serialized as JSONL (one JSON object per line, in seq order),
// terminating at turn.finished.
func Collect(ctx context.Context, sess *session.Session, prompt string) ([]byte, error) {
	sub := sess.Subscribe(event.FilterAll)
	defer sub.Close()

	if err := sess.Prompt(ctx, prompt); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				return buf.Bytes(), nil
			}
			if err := enc.Encode(ev); err != nil {
				return nil, fmt.Errorf("goldenio: encode %s: %w", ev.Kind(), err)
			}
			if ev.Kind() == event.KindTurnFinished {
				return buf.Bytes(), nil
			}
		case <-time.After(collectTimeout):
			return nil, fmt.Errorf("goldenio: timed out after %s waiting for turn.finished", collectTimeout)
		}
	}
}

// Assert compares got against the golden file at path, failing t on mismatch.
// With -update it rewrites the golden file and does not compare.
func Assert(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("goldenio: update %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("goldenio: read golden %s: %v (run `go test ./compose/... -update` to create it)", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
