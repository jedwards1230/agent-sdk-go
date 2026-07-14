package lsp_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/lsp"
)

// capturingHandler is a minimal slog.Handler that records every record's level
// and message under a mutex, so a test can assert (with a bounded poll, since
// the client's read loop logs from a background goroutine) that a specific
// diagnostic was surfaced.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

// seen reports whether any recorded message contains substr at the given level.
func (h *capturingHandler) seen(level slog.Level, substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level == level && strings.Contains(r.Message, substr) {
			return true
		}
	}
	return false
}

// waitSeen polls seen until it is true or testTimeout elapses.
func (h *capturingHandler) waitSeen(level slog.Level, substr string) bool {
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if h.seen(level, substr) {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return h.seen(level, substr)
}

// TestClient_WithLogger_SurfacesSwallowedReadLoopDiagnostics proves the
// optional *slog.Logger seam surfaces the read loop's three otherwise-invisible
// paths — a malformed frame dropped, a malformed publishDiagnostics dropped,
// and the read loop exiting on a transport death no in-flight call observed.
// These are the only diagnostics in the client that neither reach a caller nor
// ride the event stream, which is exactly why they earn a logger seam.
func TestClient_WithLogger_SurfacesSwallowedReadLoopDiagnostics(t *testing.T) {
	client, server := newPipedTransports()
	handler := &capturingHandler{}
	c := lsp.NewClient(client, newRecordingPublisher(), "sess", lsp.WithLogger(slog.New(handler)))
	defer func() { _ = c.Close() }()

	// A frame whose body is not valid JSON: dropped by the read loop's unmarshal.
	if err := testWriteFrame(server, []byte("this is not json")); err != nil {
		t.Fatalf("write malformed frame: %v", err)
	}
	// A well-formed JSON-RPC publishDiagnostics whose params is a string, not the
	// expected object: dropped by handleDiagnostics's unmarshal.
	badDiag := []byte(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":"not-an-object"}`)
	if err := testWriteFrame(server, badDiag); err != nil {
		t.Fatalf("write malformed diagnostics: %v", err)
	}
	// Kill the transport with no Close on the client side and no call in flight:
	// the read loop's exit is otherwise silent.
	if err := server.Close(); err != nil {
		t.Fatalf("close server transport: %v", err)
	}

	if !handler.waitSeen(slog.LevelWarn, "malformed frame") {
		t.Error("expected a warn log for the dropped malformed frame; none seen")
	}
	if !handler.waitSeen(slog.LevelWarn, "malformed publishDiagnostics") {
		t.Error("expected a warn log for the dropped malformed diagnostics; none seen")
	}
	if !handler.waitSeen(slog.LevelWarn, "transport error") {
		t.Error("expected a warn log for the read loop's transport-death exit; none seen")
	}
}

// TestClient_WithLogger_DeliberateCloseIsNotNoise proves a deliberate Close
// logs the read loop's exit at debug level (not warn), so intentional shutdown
// does not surface as a transport error.
func TestClient_WithLogger_DeliberateCloseIsNotNoise(t *testing.T) {
	client, _ := newPipedTransports()
	handler := &capturingHandler{}
	c := lsp.NewClient(client, newRecordingPublisher(), "sess", lsp.WithLogger(slog.New(handler)))

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !handler.waitSeen(slog.LevelDebug, "stopped on close") {
		t.Error("expected a debug log for the deliberate-close read-loop exit; none seen")
	}
	if handler.seen(slog.LevelWarn, "transport error") {
		t.Error("deliberate Close must not log a transport error")
	}
}

// TestClient_NilLogger_SafeOnMalformedInput proves the default (no WithLogger)
// and an explicit WithLogger(nil) are nil-safe: the same malformed inputs and
// transport death that log above must not panic when no logger is configured.
// Close blocks on the read loop's exit, so reaching its return proves the read
// loop ran through every (discard-logged) site without panicking.
func TestClient_NilLogger_SafeOnMalformedInput(t *testing.T) {
	for _, name := range []string{"default", "explicit-nil"} {
		t.Run(name, func(t *testing.T) {
			client, server := newPipedTransports()
			var c *lsp.Client
			if name == "explicit-nil" {
				c = lsp.NewClient(client, newRecordingPublisher(), "sess", lsp.WithLogger(nil))
			} else {
				c = lsp.NewClient(client, newRecordingPublisher(), "sess")
			}

			if err := testWriteFrame(server, []byte("this is not json")); err != nil {
				t.Fatalf("write malformed frame: %v", err)
			}
			if err := server.Close(); err != nil {
				t.Fatalf("close server transport: %v", err)
			}
			// Close waits on the read loop's exit: returning without a panic is
			// the nil-safety proof.
			if err := c.Close(); err != nil {
				t.Fatalf("Close after transport death: %v", err)
			}
		})
	}
}
