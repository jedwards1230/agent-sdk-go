package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// --- routing ---

func TestRouteAPIKey(t *testing.T) {
	p := New("gpt-5", nil)
	base, h, err := p.route(provider.Credential{Kind: provider.CredAPIKey, Token: "sk-abc"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if base != apiBaseURL {
		t.Errorf("base = %q, want %q", base, apiBaseURL)
	}
	if h["Authorization"] != "Bearer sk-abc" {
		t.Errorf("authorization = %q", h["Authorization"])
	}
	if _, present := h[headerAccountID]; present {
		t.Error("api-key route must not set the account-id header")
	}
}

// TestRouteOAuthSwitchesBaseURL asserts the account-present path: oauth switches
// off the api host and sets the ChatGPT-Account-ID header from cred.Account.
func TestRouteOAuthSwitchesBaseURL(t *testing.T) {
	p := New("gpt-5", nil)
	base, h, err := p.route(provider.Credential{Kind: provider.CredOAuth, Token: "tok-xyz", Account: "acct-42"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if base != oauthBaseURL {
		t.Errorf("oauth base = %q, want %q (must switch off the api host)", base, oauthBaseURL)
	}
	if h["Authorization"] != "Bearer tok-xyz" {
		t.Errorf("authorization = %q, want Bearer tok-xyz", h["Authorization"])
	}
	if h[headerAccountID] != "acct-42" {
		t.Errorf("%s = %q, want acct-42", headerAccountID, h[headerAccountID])
	}
}

// TestRouteOAuthNoAccount asserts an oauth credential with no account routes
// correctly: oauth host, bearer intact, and NO account-id header (omitted, not
// blank, not an error).
func TestRouteOAuthNoAccount(t *testing.T) {
	p := New("gpt-5", nil)
	base, h, err := p.route(provider.Credential{Kind: provider.CredOAuth, Token: "sk-oauth-plain"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if base != oauthBaseURL {
		t.Errorf("oauth base = %q, want %q", base, oauthBaseURL)
	}
	if h["Authorization"] != "Bearer sk-oauth-plain" {
		t.Errorf("authorization = %q, want the token untouched", h["Authorization"])
	}
	if _, present := h[headerAccountID]; present {
		t.Errorf("account-id header should be omitted when account is empty, got %q", h[headerAccountID])
	}
}

func TestRouteOAuthEmptyTokenErrors(t *testing.T) {
	p := New("gpt-5", nil)
	if _, _, err := p.route(provider.Credential{Kind: provider.CredOAuth}); err == nil {
		t.Error("empty oauth token should error")
	}
}

func TestRouteEmptyAPIKeyErrors(t *testing.T) {
	p := New("gpt-5", nil)
	if _, _, err := p.route(provider.Credential{Kind: provider.CredAPIKey}); err == nil {
		t.Error("empty api key should error")
	}
}

func TestBaseURLOverridePreservesHeaders(t *testing.T) {
	p := New("gpt-5", nil, WithBaseURL("http://127.0.0.1:1/v"))
	base, h, err := p.route(provider.Credential{Kind: provider.CredOAuth, Token: "tok", Account: "acct"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if base != "http://127.0.0.1:1/v" {
		t.Errorf("override base = %q", base)
	}
	if h[headerAccountID] != "acct" {
		t.Error("override must preserve oauth header logic")
	}
}

func TestInfo(t *testing.T) {
	if got := New("gpt-5", nil).Info(); got.Provider != "openai" || got.ContextWindow == 0 {
		t.Errorf("Info(gpt-5) = %+v", got)
	}
	if got := New("mystery-model", nil).Info(); got.Provider != "openai" || got.ID != "mystery-model" {
		t.Errorf("Info(unregistered) = %+v", got)
	}
}

// --- SSE streaming harness ---

// capture records what the scripted server received.
type capture struct {
	req  *http.Request
	body string
}

// sseServer returns an httptest.Server that writes the given raw SSE payload
// (flushing after the write) and records the request it received.
func sseServer(t *testing.T, payload string) (*httptest.Server, *capture) {
	t.Helper()
	rec := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.body = string(b)
		rec.req = r
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// event formats one SSE frame with an event: line and a data: line.
func event(typ, dataJSON string) string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", typ, dataJSON)
}

// drain reads a handle to completion, returning the events.
func drain(t *testing.T, h provider.StreamHandle) []provider.StreamEvent {
	t.Helper()
	var out []provider.StreamEvent
	for {
		ev, err := h.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, ev)
	}
}

// streamOnce runs one Stream call against a scripted server and returns the
// drained events plus the recorded request/body.
func streamOnce(t *testing.T, payload string, cred provider.Credential, req provider.Request) ([]provider.StreamEvent, *capture) {
	t.Helper()
	srv, rec := sseServer(t, payload)
	p := New("gpt-5", provider.StaticCredentialSource{Cred: cred}, WithBaseURL(srv.URL))
	h, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = h.Close() }()
	return drain(t, h), rec
}

func TestStreamText(t *testing.T) {
	payload := event("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hello"}`) +
		event("response.output_text.delta", `{"type":"response.output_text.delta","delta":", world"}`) +
		event("response.completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}}`)

	evs, _ := streamOnce(t, payload, provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}, provider.Request{})

	var text string
	var fin *provider.StreamEvent
	for i, e := range evs {
		switch e.Type {
		case provider.StreamTextDelta:
			text += e.Text
		case provider.StreamFinished:
			fin = &evs[i]
		}
	}
	if text != "Hello, world" {
		t.Errorf("text = %q", text)
	}
	if fin == nil {
		t.Fatal("no finished event")
	}
	if fin.StopReason != provider.StopEndTurn {
		t.Errorf("stop = %q, want end_turn", fin.StopReason)
	}
	if !fin.Usage.Equal(provider.Usage{InputTokens: 10, OutputTokens: 3, Raw: map[string]int{
		"input_tokens": 10, "output_tokens": 3, "total_tokens": 13, "cached_tokens": 0, "reasoning_tokens": 0,
	}}) {
		t.Errorf("usage = %+v", fin.Usage)
	}
}

// TestStreamCachedInputUsage asserts cached tokens are split into CacheReadTokens
// and subtracted from InputTokens (the uncached remainder).
func TestStreamCachedInputUsage(t *testing.T) {
	payload := event("response.completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":80},"output_tokens":5,"total_tokens":105}}}`)
	evs, _ := streamOnce(t, payload, provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}, provider.Request{})
	fin := evs[len(evs)-1]
	if fin.Usage.InputTokens != 20 || fin.Usage.CacheReadTokens != 80 {
		t.Errorf("usage = %+v, want input 20 / cacheRead 80", fin.Usage)
	}
	if fin.Usage.Raw["input_tokens"] != 100 {
		t.Errorf("raw input_tokens = %d, want 100 (original retained)", fin.Usage.Raw["input_tokens"])
	}
}

func TestStreamReasoning(t *testing.T) {
	payload := event("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"Thinking"}`) +
		event("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":" hard"}`) +
		event("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Answer"}`) +
		event("response.completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":2,"output_tokens_details":{"reasoning_tokens":8}}}}`)

	evs, _ := streamOnce(t, payload, provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}, provider.Request{})

	var reasoning string
	var fin provider.StreamEvent
	for _, e := range evs {
		if e.Type == provider.StreamReasoningDelta {
			reasoning += e.Text
		}
		if e.Type == provider.StreamFinished {
			fin = e
		}
	}
	if reasoning != "Thinking hard" {
		t.Errorf("reasoning = %q", reasoning)
	}
	if fin.Usage.Raw["reasoning_tokens"] != 8 {
		t.Errorf("reasoning_tokens raw = %d, want 8", fin.Usage.Raw["reasoning_tokens"])
	}
}

// TestStreamReasoningItemIDMeta asserts reasoning deltas are tagged with their
// item id under Meta["openai.item_id"] — both when the id rides on the delta and
// when it must be tracked from the reasoning item's output_item.added.
func TestStreamReasoningItemIDMeta(t *testing.T) {
	payload := event("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_42","summary":[]}}`) +
		// This delta omits item_id — the id must come from the added event.
		event("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"a"}`) +
		// This one carries item_id explicitly.
		event("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"rs_42","delta":"b"}`) +
		event("response.completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)

	evs, _ := streamOnce(t, payload, provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}, provider.Request{})

	var seen int
	for _, e := range evs {
		if e.Type != provider.StreamReasoningDelta {
			continue
		}
		seen++
		if e.Meta[metaItemID] != "rs_42" {
			t.Errorf("reasoning delta %q Meta[%s] = %q, want rs_42", e.Text, metaItemID, e.Meta[metaItemID])
		}
	}
	if seen != 2 {
		t.Fatalf("saw %d reasoning deltas, want 2", seen)
	}
}

// TestStreamToolCall covers the interleaved tool-call lifecycle: added ->
// arguments deltas -> item done, and the tool-use stop reason.
func TestStreamToolCall(t *testing.T) {
	payload := event("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"get_weather","arguments":""}}`) +
		event("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"city\":"}`) +
		event("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"paris\"}"}`) +
		event("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"get_weather","arguments":"{\"city\":\"paris\"}"}}`) +
		event("response.completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":20,"output_tokens":9}}}`)

	evs, _ := streamOnce(t, payload, provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}, provider.Request{})

	var start, end *provider.StreamEvent
	var deltas string
	var fin provider.StreamEvent
	for i, e := range evs {
		switch e.Type {
		case provider.StreamToolCallStart:
			start = &evs[i]
		case provider.StreamToolCallDelta:
			deltas += e.Tool.Delta
		case provider.StreamToolCallEnd:
			end = &evs[i]
		case provider.StreamFinished:
			fin = e
		}
	}
	if start == nil || start.Tool.ID != "call_abc" || start.Tool.Name != "get_weather" {
		t.Fatalf("tool start = %+v", start)
	}
	if deltas != `{"city":"paris"}` {
		t.Errorf("assembled deltas = %q", deltas)
	}
	if end == nil || end.Tool.ID != "call_abc" || string(end.Tool.Input) != `{"city":"paris"}` {
		t.Fatalf("tool end = %+v", end)
	}
	if fin.StopReason != provider.StopToolUse {
		t.Errorf("stop = %q, want tool_use", fin.StopReason)
	}
}

// TestStreamToolCallByItemID asserts argument deltas correlate to their call by
// item_id even when the delta omits output_index (so the default 0 would miss
// the tool that was added at a non-zero index).
func TestStreamToolCallByItemID(t *testing.T) {
	payload := event("response.output_item.added", `{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"fc_9","call_id":"call_z","name":"run","arguments":""}}`) +
		event("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_9","delta":"{\"x\":1}"}`) +
		event("response.output_item.done", `{"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"fc_9","call_id":"call_z","name":"run","arguments":"{\"x\":1}"}}`) +
		event("response.completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)

	evs, _ := streamOnce(t, payload, provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}, provider.Request{})
	var deltaID, deltaText string
	for _, e := range evs {
		if e.Type == provider.StreamToolCallDelta {
			deltaID, deltaText = e.Tool.ID, e.Tool.Delta
		}
	}
	if deltaID != "call_z" {
		t.Errorf("delta correlated to id %q, want call_z (via item_id)", deltaID)
	}
	if deltaText != `{"x":1}` {
		t.Errorf("delta text = %q", deltaText)
	}
}

// TestStreamUncorrelatedArgumentDeltaDropped asserts an argument delta that
// matches no known tool (e.g. it arrives before the tool's added event, or with
// an unknown item_id) is dropped rather than emitted with an empty ID. The
// authoritative arguments still arrive on the must-deliver ToolCallEnd.
func TestStreamUncorrelatedArgumentDeltaDropped(t *testing.T) {
	payload := event("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"ghost","output_index":7,"delta":"{\"a\":1}"}`) +
		event("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_ok","name":"go","arguments":""}}`) +
		event("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"b\":2}"}`) +
		event("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_ok","name":"go","arguments":"{\"b\":2}"}}`) +
		event("response.completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)

	evs, _ := streamOnce(t, payload, provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}, provider.Request{})

	var deltas []provider.ToolCall
	var end *provider.ToolCall
	for i := range evs {
		switch evs[i].Type {
		case provider.StreamToolCallDelta:
			deltas = append(deltas, *evs[i].Tool)
		case provider.StreamToolCallEnd:
			end = evs[i].Tool
		}
	}
	// Only the correlated delta survives; the ghost one is dropped.
	if len(deltas) != 1 {
		t.Fatalf("got %d tool deltas, want 1 (the uncorrelated one must be dropped): %+v", len(deltas), deltas)
	}
	if deltas[0].ID != "call_ok" || deltas[0].Delta != `{"b":2}` {
		t.Errorf("surviving delta = %+v", deltas[0])
	}
	// No delta is ever emitted with an empty ID.
	for _, d := range deltas {
		if d.ID == "" {
			t.Errorf("delta emitted with empty ID: %+v", d)
		}
	}
	if end == nil || string(end.Input) != `{"b":2}` {
		t.Errorf("tool end (authoritative args) = %+v", end)
	}
}

func TestStreamIncompleteMaxTokens(t *testing.T) {
	payload := event("response.output_text.delta", `{"type":"response.output_text.delta","delta":"partial"}`) +
		event("response.incomplete", `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":1,"output_tokens":128}}}`)

	evs, _ := streamOnce(t, payload, provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}, provider.Request{})
	fin := evs[len(evs)-1]
	if fin.Type != provider.StreamFinished || fin.StopReason != provider.StopMaxTokens {
		t.Errorf("final = %+v, want finished/max_tokens", fin)
	}
}

func TestStreamFailedSurfacesError(t *testing.T) {
	payload := event("response.failed", `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}`)
	srv, _ := sseServer(t, payload)
	p := New("gpt-5", provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}}, WithBaseURL(srv.URL))
	h, err := p.Stream(context.Background(), provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = h.Close() }()

	var streamErr error
	for {
		_, err := h.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			streamErr = err
			break
		}
	}
	var se *StreamError
	if !errors.As(streamErr, &se) || se.Code != "server_error" {
		t.Fatalf("expected *StreamError with code server_error, got %v", streamErr)
	}
}

func TestStreamTopLevelError(t *testing.T) {
	payload := event("error", `{"type":"error","code":"rate_limit","message":"slow down"}`)
	srv, _ := sseServer(t, payload)
	p := New("gpt-5", provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}}, WithBaseURL(srv.URL))
	h, _ := p.Stream(context.Background(), provider.Request{})
	defer func() { _ = h.Close() }()
	_, err := h.Next()
	if err == nil || !strings.Contains(err.Error(), "slow down") {
		t.Errorf("expected top-level error, got %v", err)
	}
}

// TestStreamToleratesMalformedFrames asserts keep-alive comments and unparseable
// data lines do not abort a turn.
func TestStreamToleratesMalformedFrames(t *testing.T) {
	payload := ": keep-alive ping\n\n" +
		"data: not-json-at-all\n\n" +
		event("response.output_text.delta", `{"type":"response.output_text.delta","delta":"ok"}`) +
		event("response.completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)

	evs, _ := streamOnce(t, payload, provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}, provider.Request{})
	var text string
	for _, e := range evs {
		if e.Type == provider.StreamTextDelta {
			text += e.Text
		}
	}
	if text != "ok" {
		t.Errorf("text = %q, want ok (malformed frames should be skipped)", text)
	}
}

// TestOAuthRoutingHeaders asserts a real Stream call over the oauth credential
// sends the ChatGPT-Account-ID header and an OAuth bearer, hitting /responses,
// with store:false in the body.
func TestOAuthRoutingHeaders(t *testing.T) {
	payload := event("response.completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)
	_, rec := streamOnce(t, payload,
		provider.Credential{Kind: provider.CredOAuth, Token: "tok-secret", Account: "acct-99"},
		provider.Request{Messages: []provider.Message{provider.UserText("hi")}})

	if rec.req == nil {
		t.Fatal("no request captured")
	}
	if rec.req.URL.Path != "/responses" {
		t.Errorf("path = %q, want /responses", rec.req.URL.Path)
	}
	if got := rec.req.Header.Get("Authorization"); got != "Bearer tok-secret" {
		t.Errorf("authorization = %q", got)
	}
	if got := rec.req.Header.Get(headerAccountID); got != "acct-99" {
		t.Errorf("%s = %q, want acct-99", headerAccountID, got)
	}
	if rec.req.Header.Get("Accept") != "text/event-stream" {
		t.Errorf("accept = %q", rec.req.Header.Get("Accept"))
	}
	if !strings.Contains(rec.body, `"store":false`) {
		t.Errorf("body should set store:false, got %s", rec.body)
	}
}

func TestAPIKeyRoutingHeaders(t *testing.T) {
	payload := event("response.completed", `{"type":"response.completed","response":{"status":"completed"}}`)
	_, rec := streamOnce(t, payload,
		provider.Credential{Kind: provider.CredAPIKey, Token: "sk-live"},
		provider.Request{})
	if rec.req.Header.Get("Authorization") != "Bearer sk-live" {
		t.Errorf("authorization = %q", rec.req.Header.Get("Authorization"))
	}
	if rec.req.Header.Get(headerAccountID) != "" {
		t.Error("api-key path must not send the account-id header")
	}
}

// TestStreamContextCancel asserts a mid-stream cancellation closes cleanly and
// surfaces a StopCancelled terminal event.
func TestStreamContextCancel(t *testing.T) {
	// Server sends one delta then blocks until the client disconnects.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte(event("response.output_text.delta", `{"type":"response.output_text.delta","delta":"partial"}`)))
		fl.Flush()
		<-r.Context().Done() // hold the connection open until the client cancels
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	p := New("gpt-5", provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}}, WithBaseURL(srv.URL))
	h, err := p.Stream(ctx, provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = h.Close() }()

	first, err := h.Next()
	if err != nil || first.Type != provider.StreamTextDelta {
		t.Fatalf("first event = %+v, err %v", first, err)
	}
	cancel()

	// After cancellation the next event is a terminal StopCancelled, then EOF.
	ev, err := h.Next()
	if err != nil {
		t.Fatalf("post-cancel Next err = %v", err)
	}
	if ev.Type != provider.StreamFinished || ev.StopReason != provider.StopCancelled {
		t.Fatalf("post-cancel event = %+v, want finished/cancelled", ev)
	}
	if _, err := h.Next(); err != io.EOF {
		t.Errorf("after terminal, want EOF, got %v", err)
	}
}

func TestStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()
	p := New("gpt-5", provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}}, WithBaseURL(srv.URL))
	_, err := p.Stream(context.Background(), provider.Request{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected *APIError 429, got %v", err)
	}
}

// --- SSE decoder edge cases ---

func TestSSEDecoderMultilineAndCRLF(t *testing.T) {
	// Two data lines in one event coalesce with a newline; CRLF terminators are
	// stripped.
	raw := "data: line1\r\ndata: line2\r\n\r\ndata: second\n\n"
	d := newSSEDecoder(strings.NewReader(raw))

	f1, err := d.next()
	if err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	if string(f1.data) != "line1\nline2" {
		t.Errorf("frame 1 = %q, want line1\\nline2", f1.data)
	}
	f2, err := d.next()
	if err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	if string(f2.data) != "second" {
		t.Errorf("frame 2 = %q", f2.data)
	}
	if _, err := d.next(); err != io.EOF {
		t.Errorf("want EOF, got %v", err)
	}
}

func TestSSEDecoderLargeLine(t *testing.T) {
	big := strings.Repeat("x", 200_000)
	raw := "data: " + big + "\n\n"
	d := newSSEDecoder(strings.NewReader(raw))
	f, err := d.next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(f.data) != len(big) {
		t.Errorf("large data len = %d, want %d", len(f.data), len(big))
	}
}
