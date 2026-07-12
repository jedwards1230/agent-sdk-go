package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// cassette is a recorded sequence of request/response interactions. Each
// interaction asserts the request body contains expected substrings (a
// JSON-aware match against the projected wire body) and replays a structured
// list of SSE frames as the response.
type cassette struct {
	Interactions []interaction `json:"interactions"`
}

type interaction struct {
	Request struct {
		BodyContains []string `json:"body_contains"`
	} `json:"request"`
	Frames []struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
	} `json:"frames"`
}

// sseBody re-serializes an interaction's structured frames into an SSE body.
func (in interaction) sseBody() string {
	var b strings.Builder
	for _, f := range in.Frames {
		b.WriteString("event: ")
		b.WriteString(f.Event)
		b.WriteString("\ndata: ")
		b.Write(f.Data)
		b.WriteString("\n\n")
	}
	return b.String()
}

// cassettePlayer serves a cassette's interactions in order, asserting each
// request matches before replaying its frames.
type cassettePlayer struct {
	t   *testing.T
	c   cassette
	mu  sync.Mutex
	idx int
}

func (p *cassettePlayer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.idx >= len(p.c.Interactions) {
		p.t.Errorf("cassette: unexpected request #%d (only %d recorded)", p.idx+1, len(p.c.Interactions))
		http.Error(w, "no more interactions", http.StatusInternalServerError)
		return
	}
	in := p.c.Interactions[p.idx]
	p.idx++

	data, _ := io.ReadAll(r.Body)
	for _, want := range in.Request.BodyContains {
		if !strings.Contains(string(data), want) {
			p.t.Errorf("interaction %d: body missing %q\nbody: %s", p.idx, want, data)
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, in.sseBody())
}

func loadCassette(t *testing.T, path string) cassette {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	var c cassette
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("parse cassette: %v", err)
	}
	return c
}

// TestCassetteThinkingToolsSignatureReplay drives the extended-thinking +
// tool-use flow end-to-end: turn 1 streams a signed thinking block and a tool
// call; the test captures the signature from the reasoning event's Meta (as the
// loop would), assembles the assistant turn, and sends turn 2. The cassette's
// turn-2 request matcher asserts the thinking block AND its signature reach the
// wire — the correctness requirement for thinking+tools multi-turn.
func TestCassetteThinkingToolsSignatureReplay(t *testing.T) {
	c := loadCassette(t, "testdata/thinking_tools_conversation.json")
	player := &cassettePlayer{t: t, c: c}
	srv := httptest.NewServer(player)
	defer srv.Close()

	cred := provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk-ant-api-test"}}
	p := New("claude-sonnet-5", cred, WithBaseURL(srv.URL))
	tools := []provider.ToolSpec{{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`)}}

	// --- Turn 1: prompt (thinking on) → signed reasoning + tool_use ---
	history := []provider.Message{provider.UserText("Refactor the parser entrypoint.")}
	h1, err := p.Stream(context.Background(), provider.Request{
		Messages: history, Tools: tools,
		Params: provider.Params{Thinking: provider.Thinking{Enabled: true, BudgetTokens: 2048}},
	})
	if err != nil {
		t.Fatalf("turn 1 Stream: %v", err)
	}

	// Reconstruct the assembled reasoning block the loop would build: accumulate
	// reasoning text and merge Meta across the reasoning deltas.
	var reasoningText, signature, toolID, toolInput string
	for _, e := range drain(t, h1) {
		switch e.Type {
		case provider.StreamReasoningDelta:
			reasoningText += e.Text
			if s, ok := e.Meta[metaSignatureKey]; ok {
				signature = s
			}
		case provider.StreamToolCallStart:
			toolID = e.Tool.ID
		case provider.StreamToolCallEnd:
			toolInput = string(e.Tool.Input)
		}
	}
	_ = h1.Close()

	if signature != "sig_synthetic_thinking_001" {
		t.Fatalf("captured signature = %q, want the streamed signature", signature)
	}
	if reasoningText != "I should inspect the parser first. Let me grep for the entrypoint." {
		t.Errorf("reasoning text = %q", reasoningText)
	}

	// --- Turn 2: replay signed thinking + tool_use, add tool_result ---
	history = append(history,
		provider.Message{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			{Type: provider.BlockReasoning, Text: reasoningText, Meta: map[string]string{metaSignatureKey: signature}},
			provider.ToolUseBlock(toolID, "bash", json.RawMessage(toolInput)),
		}},
		provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{
			provider.ToolResultBlock(toolID, "src/parser.go:42: func Parse(", false),
		}},
	)
	h2, err := p.Stream(context.Background(), provider.Request{Messages: history, Tools: tools})
	if err != nil {
		t.Fatalf("turn 2 Stream: %v", err)
	}
	// The cassette matcher (body_contains signature + type:thinking + tool_result)
	// fails the test via player.t if the signature did not reach the wire.
	var summary string
	var fin provider.StreamEvent
	for _, e := range drain(t, h2) {
		switch e.Type {
		case provider.StreamTextDelta:
			summary += e.Text
		case provider.StreamFinished:
			fin = e
		}
	}
	_ = h2.Close()

	if !strings.Contains(summary, "src/parser.go:42") {
		t.Errorf("turn2 summary = %q", summary)
	}
	if fin.StopReason != provider.StopEndTurn {
		t.Errorf("turn2 stop = %q, want end_turn", fin.StopReason)
	}
	if player.idx != 2 {
		t.Errorf("played %d interactions, want 2", player.idx)
	}
}

// TestCassetteToolUseConversation replays a realistic two-turn tool-use
// conversation end-to-end through the provider: the model reasons and calls a
// tool (turn 1), then the tool result is fed back and the model summarizes
// (turn 2). It exercises reasoning, tool-call assembly, cache-token usage, and
// the outgoing tool_result projection in one flow.
func TestCassetteToolUseConversation(t *testing.T) {
	c := loadCassette(t, "testdata/tool_use_conversation.json")
	player := &cassettePlayer{t: t, c: c}
	srv := httptest.NewServer(player)
	defer srv.Close()

	cred := provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk-ant-api-test"}}
	p := New("claude-sonnet-5", cred, WithBaseURL(srv.URL))

	tools := []provider.ToolSpec{{
		Name:        "bash",
		Description: "run a shell command",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
	}}

	// --- Turn 1: prompt → reasoning + text + tool_use ---
	history := []provider.Message{provider.UserText("List the files in the current directory.")}
	h1, err := p.Stream(context.Background(), provider.Request{
		Messages: history, Tools: tools,
		Params: provider.Params{Thinking: provider.Thinking{Enabled: true, BudgetTokens: 2048}},
	})
	if err != nil {
		t.Fatalf("turn 1 Stream: %v", err)
	}
	turn1 := drain(t, h1)
	_ = h1.Close()

	var reasoning, text, toolID, toolInput string
	var fin1 provider.StreamEvent
	for _, e := range turn1 {
		switch e.Type {
		case provider.StreamReasoningDelta:
			reasoning += e.Text
		case provider.StreamTextDelta:
			text += e.Text
		case provider.StreamToolCallStart:
			toolID = e.Tool.ID
		case provider.StreamToolCallEnd:
			toolInput = string(e.Tool.Input)
		case provider.StreamFinished:
			fin1 = e
		}
	}
	if !strings.Contains(reasoning, "directory listing") {
		t.Errorf("turn1 reasoning = %q", reasoning)
	}
	if text != "Let me list them." {
		t.Errorf("turn1 text = %q", text)
	}
	if toolID != "toolu_synthetic_ls" || toolInput != `{"command": "ls -la"}` {
		t.Errorf("turn1 tool = %s / %s", toolID, toolInput)
	}
	if fin1.StopReason != provider.StopToolUse {
		t.Errorf("turn1 stop = %q, want tool_use", fin1.StopReason)
	}
	if fin1.Usage.InputTokens != 512 || fin1.Usage.CacheReadTokens != 400 || fin1.Usage.OutputTokens != 48 {
		t.Errorf("turn1 usage = %+v", fin1.Usage)
	}
	// Cost is priced from the shared registry.
	if cost, ok := provider.CostOf("claude-sonnet-5", fin1.Usage); !ok || cost.USD <= 0 {
		t.Errorf("turn1 cost = %+v ok=%v", cost, ok)
	}

	// --- Turn 2: append assistant tool_use + user tool_result → summary ---
	history = append(history,
		provider.Message{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			provider.ToolUseBlock(toolID, "bash", json.RawMessage(toolInput)),
		}},
		provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{
			provider.ToolResultBlock(toolID, "total 8\nREADME.md\nsrc", false),
		}},
	)
	h2, err := p.Stream(context.Background(), provider.Request{Messages: history, Tools: tools})
	if err != nil {
		t.Fatalf("turn 2 Stream: %v", err)
	}
	turn2 := drain(t, h2)
	_ = h2.Close()

	var summary string
	var fin2 provider.StreamEvent
	for _, e := range turn2 {
		switch e.Type {
		case provider.StreamTextDelta:
			summary += e.Text
		case provider.StreamFinished:
			fin2 = e
		}
	}
	if !strings.Contains(summary, "two entries") {
		t.Errorf("turn2 summary = %q", summary)
	}
	if fin2.StopReason != provider.StopEndTurn {
		t.Errorf("turn2 stop = %q, want end_turn", fin2.StopReason)
	}
	if player.idx != 2 {
		t.Errorf("played %d interactions, want 2", player.idx)
	}
}
