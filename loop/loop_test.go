package loop_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

const sid = "test-session"

// scripted returns a StreamFn that replays one event slice per call, erroring
// once the script is exhausted.
func scripted(turns ...[]provider.StreamEvent) loop.StreamFn {
	i := 0
	return func(_ context.Context, _ provider.Request) (provider.StreamHandle, error) {
		if i >= len(turns) {
			return nil, errors.New("scripted: exhausted")
		}
		t := turns[i]
		i++
		return provider.SliceStream(t...), nil
	}
}

func textTurn(text string, stop provider.StopReason) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.StreamTextDelta, Text: text},
		{Type: provider.StreamFinished, StopReason: stop, Usage: provider.Usage{InputTokens: 3, OutputTokens: 2}},
	}
}

func toolTurn(id, name, input string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: id, Name: name}},
		{Type: provider.StreamToolCallDelta, Tool: &provider.ToolCall{ID: id, Delta: input}},
		{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}},
		{Type: provider.StreamFinished, StopReason: provider.StopToolUse, Usage: provider.Usage{InputTokens: 4, OutputTokens: 1}},
	}
}

// collectKinds drains every buffered event from a subscription (broker delivery
// is synchronous, so all events are present once Run returns).
func collectKinds(sub *event.Subscription) []string {
	var kinds []string
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				return kinds
			}
			kinds = append(kinds, e.Kind())
		default:
			return kinds
		}
	}
}

func countKind(kinds []string, kind string) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}

// fakeTool is a registry with a single named tool.
type fakeTool struct {
	name   string
	result loop.ToolResult
	err    error
	gotIn  json.RawMessage
	runs   int
}

func (f *fakeTool) Get(name string) (loop.Tool, bool) {
	if name == f.name {
		return f, true
	}
	return nil, false
}
func (f *fakeTool) Specs() []provider.ToolSpec {
	return []provider.ToolSpec{{Name: f.name}}
}
func (f *fakeTool) Run(_ context.Context, input json.RawMessage) (loop.ToolResult, error) {
	f.runs++
	f.gotIn = input
	return f.result, f.err
}

func baseConfig(b *event.Broker, stream loop.StreamFn) loop.Config {
	return loop.Config{Stream: stream, Model: "faux", Broker: b, SessionID: sid}
}

func TestSingleTurnNoTools(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	cfg := baseConfig(b, scripted(textTurn("hello", provider.StopEndTurn)))
	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("hi")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != provider.StopEndTurn || res.Iterations != 1 {
		t.Errorf("res = %+v", res)
	}
	if !res.Usage.Equal(provider.Usage{InputTokens: 3, OutputTokens: 2}) {
		t.Errorf("usage = %+v", res.Usage)
	}
	// initial user + assistant reply.
	if len(res.Messages) != 2 || res.Messages[1].Role != provider.RoleAssistant || res.Messages[1].Text() != "hello" {
		t.Errorf("messages = %+v", res.Messages)
	}

	kinds := collectKinds(sub)
	for _, want := range []string{event.KindTurnStarted, event.KindMessageStarted, event.KindMessageDelta, event.KindMessageFinished, event.KindTurnFinished} {
		if countKind(kinds, want) != 1 {
			t.Errorf("kind %s count = %d, want 1 (all: %v)", want, countKind(kinds, want), kinds)
		}
	}
}

func TestToolCallRound(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "pong"}}
	cfg := baseConfig(b, scripted(
		toolTurn("t1", "echo", `{"msg":"ping"}`),
		textTurn("done", provider.StopEndTurn),
	))
	cfg.Tools = tool

	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Iterations != 2 || res.StopReason != provider.StopEndTurn {
		t.Errorf("res = %+v", res)
	}
	if string(tool.gotIn) != `{"msg":"ping"}` {
		t.Errorf("tool input = %s, want assembled {\"msg\":\"ping\"}", tool.gotIn)
	}
	// cumulative usage across both turns: (4,1) + (3,2).
	if !res.Usage.Equal(provider.Usage{InputTokens: 7, OutputTokens: 3}) {
		t.Errorf("usage = %+v", res.Usage)
	}
	// messages: user, assistant(tool_use), user(tool_result), assistant(text).
	if len(res.Messages) != 4 {
		t.Fatalf("messages len = %d, want 4: %+v", len(res.Messages), res.Messages)
	}
	if res.Messages[2].Role != provider.RoleUser || res.Messages[2].Content[0].Type != provider.BlockToolResult {
		t.Errorf("tool result message = %+v", res.Messages[2])
	}

	kinds := collectKinds(sub)
	if countKind(kinds, event.KindToolCallStarted) != 1 || countKind(kinds, event.KindToolCallFinished) != 1 {
		t.Errorf("tool events: %v", kinds)
	}
	if countKind(kinds, event.KindTurnFinished) != 2 {
		t.Errorf("want 2 turn.finished, got %v", kinds)
	}
}

func TestUnknownToolIsError(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()

	tool := &fakeTool{name: "known"}
	cfg := baseConfig(b, scripted(
		toolTurn("t1", "missing", `{}`),
		textTurn("recovered", provider.StopEndTurn),
	))
	cfg.Tools = tool

	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	trBlock := res.Messages[2].Content[0]
	if !trBlock.IsError {
		t.Errorf("expected error tool result, got %+v", trBlock)
	}
}

func TestCancellation(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the first iteration.

	cfg := baseConfig(b, scripted(textTurn("unused", provider.StopEndTurn)))
	res, err := loop.Run(ctx, cfg, []provider.Message{provider.UserText("hi")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.StopReason != provider.StopCancelled {
		t.Errorf("stop = %q, want cancelled", res.StopReason)
	}
	kinds := collectKinds(sub)
	if countKind(kinds, event.KindSessionError) != 1 {
		t.Errorf("want a session.error, got %v", kinds)
	}
}

func TestStreamError(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	streamErr := errors.New("provider down")
	cfg := baseConfig(b, func(_ context.Context, _ provider.Request) (provider.StreamHandle, error) {
		return nil, streamErr
	})
	_, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("hi")})
	if !errors.Is(err, streamErr) {
		t.Fatalf("err = %v, want provider down", err)
	}
	kinds := collectKinds(sub)
	if countKind(kinds, event.KindSessionError) != 1 || countKind(kinds, event.KindTurnFinished) != 1 {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestHookNeverThrow(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	var transformed bool
	cfg := baseConfig(b, scripted(textTurn("hi", provider.StopEndTurn)))
	cfg.Hooks = loop.Hooks{
		TransformContext: func(_ context.Context, msgs []provider.Message) ([]provider.Message, error) {
			transformed = true
			return nil, errors.New("hook boom")
		},
	}
	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("hi")})
	if err != nil {
		t.Fatalf("hook error must not fail the run: %v", err)
	}
	if !transformed {
		t.Error("transform hook should have run")
	}
	if res.StopReason != provider.StopEndTurn {
		t.Errorf("run should complete: %+v", res)
	}
	// non-fatal session.error emitted for the hook failure.
	if countKind(collectKinds(sub), event.KindSessionError) != 1 {
		t.Error("want a non-fatal session.error for the hook failure")
	}
}

func TestSteeringTransformContext(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()

	var sawSystem bool
	cfg := baseConfig(b, func(_ context.Context, req provider.Request) (provider.StreamHandle, error) {
		for _, m := range req.Messages {
			if m.Role == provider.RoleSystem {
				sawSystem = true
			}
		}
		return provider.SliceStream(textTurn("ok", provider.StopEndTurn)...), nil
	})
	cfg.Hooks = loop.Hooks{
		TransformContext: func(_ context.Context, msgs []provider.Message) ([]provider.Message, error) {
			return append([]provider.Message{{Role: provider.RoleSystem, Content: []provider.ContentBlock{provider.TextBlock("steer")}}}, msgs...), nil
		},
	}
	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("hi")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sawSystem {
		t.Error("transformContext steering was not applied to the request")
	}
}

func TestBeforeToolRewrite(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := baseConfig(b, scripted(
		toolTurn("t1", "echo", `{"a":1}`),
		textTurn("done", provider.StopEndTurn),
	))
	cfg.Tools = tool
	cfg.Hooks = loop.Hooks{
		BeforeTool: func(_ context.Context, call loop.ToolCall) (loop.ToolCall, error) {
			call.Input = json.RawMessage(`{"a":2}`)
			return call, nil
		},
	}
	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(tool.gotIn) != `{"a":2}` {
		t.Errorf("beforeTool rewrite not applied: tool saw %s", tool.gotIn)
	}
}

func TestIterationCap(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	// Provider always requests a tool that produces an (error) result, so the
	// loop never sees end_turn and must stop at the cap.
	stream := func(_ context.Context, _ provider.Request) (provider.StreamHandle, error) {
		return provider.SliceStream(toolTurn("t", "missing", `{}`)...), nil
	}
	cfg := baseConfig(b, stream)
	cfg.Tools = &fakeTool{name: "known"}
	cfg.MaxIters = 3

	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Iterations != 3 {
		t.Errorf("iterations = %d, want 3 (the cap)", res.Iterations)
	}
	// non-fatal session.error announcing the cap.
	if countKind(collectKinds(sub), event.KindSessionError) != 1 {
		t.Error("want a session.error announcing the iteration cap")
	}
}

func TestDuplicateToolEndExecutesOnce(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()

	// A malformed stream emits StreamToolCallEnd twice for the same id.
	dupTurn := []provider.StreamEvent{
		{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "t1", Name: "echo"}},
		{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "t1", Name: "echo", Input: json.RawMessage(`{"a":1}`)}},
		{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "t1", Name: "echo", Input: json.RawMessage(`{"a":1}`)}},
		{Type: provider.StreamFinished, StopReason: provider.StopToolUse},
	}
	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := baseConfig(b, scripted(dupTurn, textTurn("done", provider.StopEndTurn)))
	cfg.Tools = tool

	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tool.runs != 1 {
		t.Errorf("tool executed %d times, want exactly 1 (duplicate End must not double-execute)", tool.runs)
	}
	// The assistant turn must contain exactly one tool_use block.
	var toolUses int
	for _, blk := range res.Messages[1].Content {
		if blk.Type == provider.BlockToolUse {
			toolUses++
		}
	}
	if toolUses != 1 {
		t.Errorf("assistant turn has %d tool_use blocks, want 1", toolUses)
	}
}

func TestMidStreamCancellation(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	ctx, cancel := context.WithCancel(context.Background())
	// The stream yields one text delta and cancels the context as a side effect,
	// so the loop hits its in-call ctx check on the next iteration.
	stream := func(_ context.Context, _ provider.Request) (provider.StreamHandle, error) {
		return &cancelStream{cancel: cancel, ev: provider.StreamEvent{Type: provider.StreamTextDelta, Text: "partial"}}, nil
	}
	res, err := loop.Run(ctx, baseConfig(b, stream), []provider.Message{provider.UserText("hi")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.StopReason != provider.StopCancelled {
		t.Errorf("stop = %q, want cancelled", res.StopReason)
	}
	// Mid-call cancellation emits a BALANCED turn.started + turn.finished pair.
	kinds := collectKinds(sub)
	if countKind(kinds, event.KindTurnStarted) != 1 || countKind(kinds, event.KindTurnFinished) != 1 {
		t.Errorf("want balanced turn.started/turn.finished, got %v", kinds)
	}
}

// cancelStream yields ev once (cancelling ctx as a side effect), then io.EOF.
type cancelStream struct {
	cancel context.CancelFunc
	ev     provider.StreamEvent
	done   bool
}

func (c *cancelStream) Next() (provider.StreamEvent, error) {
	if c.done {
		return provider.StreamEvent{}, errors.New("unreachable: ctx should be cancelled")
	}
	c.done = true
	c.cancel()
	return c.ev, nil
}
func (c *cancelStream) Close() error { return nil }

func TestMissingFinishedFailsClosed(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	// Stream ends (io.EOF) without ever emitting StreamFinished.
	noFinish := []provider.StreamEvent{{Type: provider.StreamTextDelta, Text: "hi"}}
	res, err := loop.Run(context.Background(), baseConfig(b, scripted(noFinish)), []provider.Message{provider.UserText("hi")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != provider.StopError {
		t.Errorf("stop = %q, want error (missing finished must fail closed)", res.StopReason)
	}
	if countKind(collectKinds(sub), event.KindSessionError) != 1 {
		t.Error("want a session.error for the missing finished event")
	}
}

func TestBlockMetaCarriedThroughStream(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()

	// A reasoning block arrives with an opaque provider signature; the loop must
	// carry it onto the assembled ContentBlock so it round-trips + replays.
	turn := []provider.StreamEvent{
		{Type: provider.StreamReasoningDelta, Text: "thinking..."},
		{Type: provider.StreamReasoningDelta, Meta: map[string]string{"anthropic.signature": "sig-abc"}},
		{Type: provider.StreamTextDelta, Text: "answer"},
		{Type: provider.StreamFinished, StopReason: provider.StopEndTurn},
	}
	res, err := loop.Run(context.Background(), baseConfig(b, scripted(turn)), []provider.Message{provider.UserText("hi")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assistant := res.Messages[1]
	if len(assistant.Content) != 2 {
		t.Fatalf("assistant content = %+v", assistant.Content)
	}
	reasoning := assistant.Content[0]
	if reasoning.Type != provider.BlockReasoning || reasoning.Meta["anthropic.signature"] != "sig-abc" {
		t.Errorf("reasoning block did not carry the signature meta: %+v", reasoning)
	}
	// The text block carries no meta.
	if assistant.Content[1].Meta != nil {
		t.Errorf("text block unexpectedly carries meta: %+v", assistant.Content[1])
	}

	// Meta must survive a JSON journal round-trip.
	raw, err := json.Marshal(reasoning)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back provider.ContentBlock
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Meta["anthropic.signature"] != "sig-abc" {
		t.Errorf("meta lost in JSON round-trip: %s", raw)
	}
}

func TestRequiresBrokerAndStream(t *testing.T) {
	if _, err := loop.Run(context.Background(), loop.Config{SessionID: sid}, nil); err == nil {
		t.Error("missing broker should error")
	}
	b := event.NewBroker()
	defer b.Close()
	if _, err := loop.Run(context.Background(), loop.Config{Broker: b, SessionID: sid}, nil); err == nil {
		t.Error("missing provider/stream should error")
	}
}

func TestProviderStreamAdapter(t *testing.T) {
	// A Provider (not a raw StreamFn) drives the loop and prices via the registry.
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	cfg := loop.Config{
		Provider:  &scriptProvider{turns: [][]provider.StreamEvent{textTurn("hi", provider.StopEndTurn)}},
		Model:     "claude-opus-4-8",
		Broker:    b,
		SessionID: sid,
	}
	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("hi")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// turn.finished for a registered model carries a cost payload.
	sawCost := false
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				goto done
			}
			if tf, isTF := e.(event.TurnFinished); isTF && tf.Cost != nil {
				sawCost = true
			}
		default:
			goto done
		}
	}
done:
	if !sawCost {
		t.Error("turn.finished for a priced model should carry a cost")
	}
}

type scriptProvider struct {
	turns [][]provider.StreamEvent
	i     int
}

func (p *scriptProvider) Stream(_ context.Context, _ provider.Request) (provider.StreamHandle, error) {
	if p.i >= len(p.turns) {
		return nil, errors.New("exhausted")
	}
	t := p.turns[p.i]
	p.i++
	return provider.SliceStream(t...), nil
}
func (p scriptProvider) Info() provider.ModelInfo { return provider.ModelInfo{ID: "faux"} }
