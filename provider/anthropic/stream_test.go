package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// sse joins raw SSE frames into a single stream body. Each frame is an already
// formatted "event: …\ndata: …" block; a blank line separates them.
func sse(frames ...string) string {
	return strings.Join(frames, "\n\n") + "\n\n"
}

// frame formats one SSE event with an event: name and a data: JSON line.
func frame(event, data string) string {
	return "event: " + event + "\ndata: " + data
}

// sseHandler returns a handler that writes body as an event stream, flushing so
// the client sees frames incrementally.
func sseHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// testProvider starts an httptest server with handler and returns a provider
// pointed at it plus a static credential of the given kind.
func testProvider(t *testing.T, kind provider.CredKind, handler http.Handler) *Provider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cred := provider.StaticCredentialSource{Cred: provider.Credential{Kind: kind, Token: token(kind)}}
	return New("claude-sonnet-5", cred, WithBaseURL(srv.URL))
}

func token(kind provider.CredKind) string {
	if kind == provider.CredOAuth {
		return "sk-ant-oat-test-abc123"
	}
	return "sk-ant-api-test-abc123"
}

// drain collects every event from a handle until io.EOF.
func drain(t *testing.T, h provider.StreamHandle) []provider.StreamEvent {
	t.Helper()
	var out []provider.StreamEvent
	for {
		ev, err := h.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, ev)
	}
}

// A minimal well-formed text turn.
var textTurn = sse(
	frame("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1}}}`),
	frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
	frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
	frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}`),
	frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
	frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`),
	frame("message_stop", `{"type":"message_stop"}`),
)

func TestStreamText(t *testing.T) {
	p := testProvider(t, provider.CredAPIKey, sseHandler(textTurn))
	h, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{provider.UserText("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = h.Close() }()

	got := drain(t, h)
	var text strings.Builder
	var fin *provider.StreamEvent
	for i := range got {
		switch got[i].Type {
		case provider.StreamTextDelta:
			text.WriteString(got[i].Text)
		case provider.StreamFinished:
			fin = &got[i]
		}
	}
	if text.String() != "Hello, world" {
		t.Errorf("text = %q, want %q", text.String(), "Hello, world")
	}
	if fin == nil {
		t.Fatal("no finished event")
	}
	if fin.StopReason != provider.StopEndTurn {
		t.Errorf("stop = %q, want end_turn", fin.StopReason)
	}
	// input from message_start, output overwritten by message_delta.
	if !fin.Usage.Equal(usageWithRaw(12, 9, 0, 0)) {
		t.Errorf("usage = %+v", fin.Usage)
	}
}

// usageWithRaw builds the expected Usage including the Raw audit map the stream
// records.
func usageWithRaw(in, out, cr, cw int) provider.Usage {
	u := provider.Usage{InputTokens: in, OutputTokens: out, CacheReadTokens: cr, CacheWriteTokens: cw, Raw: map[string]int{}}
	if in > 0 {
		u.Raw["input_tokens"] = in
	}
	if out > 0 {
		u.Raw["output_tokens"] = out
	}
	if cr > 0 {
		u.Raw["cache_read_input_tokens"] = cr
	}
	if cw > 0 {
		u.Raw["cache_creation_input_tokens"] = cw
	}
	return u
}

func TestStreamReasoning(t *testing.T) {
	body := sse(
		frame("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":5}}}`),
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think. "}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"abc"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`),
		frame("message_stop", `{"type":"message_stop"}`),
	)
	p := testProvider(t, provider.CredAPIKey, sseHandler(body))
	h, _ := p.Stream(context.Background(), provider.Request{})
	defer func() { _ = h.Close() }()

	got := drain(t, h)
	var reasoning string
	for _, e := range got {
		if e.Type == provider.StreamReasoningDelta {
			reasoning += e.Text
		}
	}
	if reasoning != "Let me think. " {
		t.Errorf("reasoning = %q", reasoning)
	}
	// signature_delta must not surface as reasoning TEXT, but must be carried
	// as opaque block metadata on an (empty-text) reasoning delta.
	var sig string
	for _, e := range got {
		if e.Type == provider.StreamReasoningDelta && e.Text == "abc" {
			t.Error("signature_delta leaked as reasoning text")
		}
		if v, ok := e.Meta[metaSignatureKey]; ok {
			if e.Type != provider.StreamReasoningDelta || e.Text != "" {
				t.Errorf("signature meta on wrong event: %+v", e)
			}
			sig = v
		}
	}
	if sig != "abc" {
		t.Errorf("signature meta = %q, want abc", sig)
	}
}

func TestStreamToolCallInterleaved(t *testing.T) {
	body := sse(
		frame("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":20}}}`),
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Running it."}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_42","name":"bash","input":{}}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"ls -la\"}"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":1}`),
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":15}}`),
		frame("message_stop", `{"type":"message_stop"}`),
	)
	p := testProvider(t, provider.CredAPIKey, sseHandler(body))
	h, _ := p.Stream(context.Background(), provider.Request{})
	defer func() { _ = h.Close() }()

	got := drain(t, h)

	var start, end *provider.StreamEvent
	var deltaJSON string
	for i := range got {
		switch got[i].Type {
		case provider.StreamToolCallStart:
			start = &got[i]
		case provider.StreamToolCallDelta:
			deltaJSON += got[i].Tool.Delta
		case provider.StreamToolCallEnd:
			end = &got[i]
		}
	}
	if start == nil || start.Tool.ID != "toolu_42" || start.Tool.Name != "bash" {
		t.Fatalf("tool start = %+v", start)
	}
	if end == nil {
		t.Fatal("no tool end event")
	}
	if string(end.Tool.Input) != `{"cmd":"ls -la"}` {
		t.Errorf("assembled input = %s, want full JSON", end.Tool.Input)
	}
	if deltaJSON != `{"cmd":"ls -la"}` {
		t.Errorf("delta fragments = %s", deltaJSON)
	}
	// stop reason should be tool_use.
	if got[len(got)-1].StopReason != provider.StopToolUse {
		t.Errorf("stop = %q, want tool_use", got[len(got)-1].StopReason)
	}
}

func TestStreamCacheTokens(t *testing.T) {
	body := sse(
		frame("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":80,"cache_creation_input_tokens":20,"output_tokens":1}}}`),
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`),
		frame("message_stop", `{"type":"message_stop"}`),
	)
	p := testProvider(t, provider.CredAPIKey, sseHandler(body))
	h, _ := p.Stream(context.Background(), provider.Request{})
	defer func() { _ = h.Close() }()

	got := drain(t, h)
	fin := got[len(got)-1]
	if fin.Type != provider.StreamFinished {
		t.Fatalf("last event = %+v, want finished", fin)
	}
	if fin.Usage.CacheReadTokens != 80 || fin.Usage.CacheWriteTokens != 20 {
		t.Errorf("cache usage = read %d write %d, want 80/20", fin.Usage.CacheReadTokens, fin.Usage.CacheWriteTokens)
	}
	if fin.Usage.InputTokens != 100 || fin.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", fin.Usage)
	}
	// Raw retains the verbatim provider field names.
	if fin.Usage.Raw["cache_read_input_tokens"] != 80 || fin.Usage.Raw["cache_creation_input_tokens"] != 20 {
		t.Errorf("raw = %+v", fin.Usage.Raw)
	}
}

func TestStreamMalformedFrameSkipped(t *testing.T) {
	body := sse(
		frame("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":3}}}`),
		frame("garbage", `{not valid json at all`),
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"survived"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`),
		frame("message_stop", `{"type":"message_stop"}`),
	)
	p := testProvider(t, provider.CredAPIKey, sseHandler(body))
	h, _ := p.Stream(context.Background(), provider.Request{})
	defer func() { _ = h.Close() }()

	got := drain(t, h)
	var text string
	for _, e := range got {
		if e.Type == provider.StreamTextDelta {
			text += e.Text
		}
	}
	if text != "survived" {
		t.Errorf("text = %q, malformed frame should be skipped not fatal", text)
	}
}

func TestStreamErrorFrame(t *testing.T) {
	body := sse(
		frame("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":3}}}`),
		frame("error", `{"type":"error","error":{"type":"overloaded_error","message":"server overloaded"}}`),
	)
	p := testProvider(t, provider.CredAPIKey, sseHandler(body))
	h, _ := p.Stream(context.Background(), provider.Request{})
	defer func() { _ = h.Close() }()

	var gotErr error
	for {
		_, err := h.Next()
		if err != nil {
			gotErr = err
			break
		}
	}
	var apiErr *Error
	if !errors.As(gotErr, &apiErr) {
		t.Fatalf("err = %v, want *Error", gotErr)
	}
	if apiErr.Type != "overloaded_error" || !strings.Contains(apiErr.Message, "overloaded") {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestStreamContextCancelMidStream(t *testing.T) {
	// The handler streams the first delta, then blocks so the client can cancel.
	release := make(chan struct{})
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, frame("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":7}}}`)+"\n\n")
		_, _ = io.WriteString(w, frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)+"\n\n")
		_, _ = io.WriteString(w, frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`)+"\n\n")
		if f != nil {
			f.Flush()
		}
		<-release // hold the connection open until the test is done
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()
	defer close(release)

	cred := provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk-ant-api-test"}}
	p := New("claude-sonnet-5", cred, WithBaseURL(srv.URL))

	ctx, cancel := context.WithCancel(context.Background())
	h, err := p.Stream(ctx, provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = h.Close() }()

	// Read the first real text delta.
	var sawText bool
	for !sawText {
		ev, err := h.Next()
		if err != nil {
			t.Fatalf("Next before cancel: %v", err)
		}
		if ev.Type == provider.StreamTextDelta {
			sawText = true
		}
	}

	cancel()

	// The next event must be a clean cancelled finished, then EOF.
	ev, err := h.Next()
	if err != nil {
		t.Fatalf("Next after cancel: %v", err)
	}
	if ev.Type != provider.StreamFinished || ev.StopReason != provider.StopCancelled {
		t.Fatalf("post-cancel event = %+v, want finished/cancelled", ev)
	}
	if ev.Usage.InputTokens != 7 {
		t.Errorf("cancelled usage = %+v, want partial input 7 retained", ev.Usage)
	}
	if _, err := h.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("after finished err = %v, want EOF", err)
	}
}

func TestStreamCloseIdempotent(t *testing.T) {
	p := testProvider(t, provider.CredAPIKey, sseHandler(textTurn))
	h, _ := p.Stream(context.Background(), provider.Request{})
	if err := h.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestStreamHTTPError(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}
	p := testProvider(t, provider.CredAPIKey, http.HandlerFunc(handler))
	_, err := p.Stream(context.Background(), provider.Request{})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests || apiErr.Type != "rate_limit_error" {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestCredentialResolveError(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{}) // empty token → error
	_, err := p.Stream(context.Background(), provider.Request{})
	if err == nil {
		t.Fatal("expected credential resolution error")
	}
}
