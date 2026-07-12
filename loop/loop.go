// Package loop owns the agent loop: it drives a provider through one or more
// model calls, converts the provider's normalized stream into the typed event
// contract, executes tool calls between calls, and stops at an end-of-turn stop
// reason or an iteration cap.
//
// The loop never imports a vendor SDK — the model call is injected as a
// [StreamFn] (or derived from a [provider.Provider]). Hooks
// (BeforeTool / AfterTool / TransformContext / PrepareNextTurn) are the single
// orthogonal seam for permissions, context shaping, and steering; they are
// never-throw: a hook returns (T, error) and a hook error degrades to the
// pre-hook value rather than crashing the loop.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

// defaultMaxIters caps the model-call rounds in one Run when Config.MaxIters is
// not set. It bounds a runaway tool-calling loop.
const defaultMaxIters = 16

// StreamFn is the injectable model call. The loop calls it once per iteration;
// it must return a live [provider.StreamHandle] or an error.
type StreamFn func(ctx context.Context, req provider.Request) (provider.StreamHandle, error)

// ProviderStream adapts a [provider.Provider] to a [StreamFn].
func ProviderStream(p provider.Provider) StreamFn { return p.Stream }

// ToolRegistry is the subset of the tool package the loop consumes. It is
// declared here (consumer-side) so the loop takes no reverse dependency on the
// tool package; the tool package's Registry satisfies this interface.
type ToolRegistry interface {
	// Get returns the tool registered under name, and whether it exists.
	Get(name string) (Tool, bool)
	// Specs returns the tool specifications to advertise to the model.
	Specs() []provider.ToolSpec
}

// Tool is one executable tool.
type Tool interface {
	// Run executes the tool with JSON input and returns its result.
	Run(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

// ToolResult is a tool's outcome.
type ToolResult struct {
	// Content is the tool's textual output, fed back to the model.
	Content string
	// IsError marks a failed execution.
	IsError bool
	// Diagnostics are optional advisory messages (e.g. LSP findings).
	Diagnostics []string
}

// ToolCall is a resolved tool invocation passed to the BeforeTool/AfterTool
// hooks and executed by the loop.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// TurnState is the loop state handed to PrepareNextTurn between iterations.
type TurnState struct {
	// Messages is the conversation after appending the last assistant turn and
	// its tool results; the hook may rewrite it (steering, compaction).
	Messages []provider.Message
	// Iteration is the zero-based index of the iteration just completed.
	Iteration int
	// LastStop is the stop reason of the iteration just completed.
	LastStop provider.StopReason
	// Usage is the cumulative usage across the run so far.
	Usage provider.Usage
}

// Hooks are the loop's never-throw seams. A nil hook is a no-op. A hook that
// returns an error does not crash the loop: the loop emits a non-fatal
// session.error and proceeds with the pre-hook value.
type Hooks struct {
	// BeforeTool may rewrite a tool call before execution.
	BeforeTool func(ctx context.Context, call ToolCall) (ToolCall, error)
	// AfterTool may rewrite a tool's result before it returns to the model.
	AfterTool func(ctx context.Context, call ToolCall, result ToolResult) (ToolResult, error)
	// TransformContext may rewrite the message list before each model call.
	TransformContext func(ctx context.Context, msgs []provider.Message) ([]provider.Message, error)
	// PrepareNextTurn may rewrite loop state between iterations.
	PrepareNextTurn func(ctx context.Context, state TurnState) (TurnState, error)
}

// Config configures a [Run]. Either Provider or Stream must be set; Provider is
// used to derive Stream and Info when Stream is nil.
type Config struct {
	// Provider is the model backend. If set and Stream is nil, Stream is derived
	// from it and its Info() supplies pricing for per-turn cost.
	Provider provider.Provider
	// Stream overrides the model call. When set, ModelID/Pricing come from the
	// registry via Model.
	Stream StreamFn
	// Model is the model identifier passed on each request and used to price
	// usage via the model registry.
	Model string
	// System is the system prompt.
	System string
	// Params carries sampling and reasoning controls.
	Params provider.Params
	// Tools is the tool registry; nil means no tools are offered.
	Tools ToolRegistry
	// Hooks are the loop's seams.
	Hooks Hooks
	// Broker receives the contract events the loop emits. Required.
	Broker *event.Broker
	// SessionID tags every emitted event. Required.
	SessionID string
	// MaxIters caps model-call rounds; <= 0 uses the default.
	MaxIters int
}

// Result is the outcome of a [Run].
type Result struct {
	// Messages is the full conversation, including the final assistant turn.
	Messages []provider.Message
	// Usage is the cumulative usage across every model call in the run.
	Usage provider.Usage
	// StopReason is the stop reason of the final model call.
	StopReason provider.StopReason
	// Iterations is the number of model calls made.
	Iterations int
}

// Run drives the agent loop from an initial message list until the model ends
// its turn (a non-tool-use stop) or the iteration cap is reached, returning the
// settled conversation and cumulative usage. Context cancellation interrupts the
// loop between and during model calls; the run emits a terminal turn.finished
// and returns ctx.Err().
func Run(ctx context.Context, cfg Config, messages []provider.Message) (Result, error) {
	if cfg.Broker == nil {
		return Result{}, errors.New("loop: Config.Broker is required")
	}
	streamFn := cfg.Stream
	if streamFn == nil {
		if cfg.Provider == nil {
			return Result{}, errors.New("loop: Config requires Provider or Stream")
		}
		streamFn = cfg.Provider.Stream
	}
	maxIters := cfg.MaxIters
	if maxIters <= 0 {
		maxIters = defaultMaxIters
	}

	r := &runner{cfg: cfg, stream: streamFn}
	// Copy the caller's slice: Run appends the assistant turn and tool results,
	// and must not mutate the caller's backing array.
	msgs := append([]provider.Message(nil), messages...)
	var (
		cum  provider.Usage
		stop provider.StopReason
		iter int
	)

	for iter = 0; iter < maxIters; iter++ {
		// Cancelled between turns: no model call started this iteration, so emit
		// only a fatal session.error (no unbalanced turn.finished). Cancellation
		// during a call is handled in callModel with balanced turn events.
		if err := ctx.Err(); err != nil {
			r.emitError(err.Error(), true)
			return Result{Messages: msgs, Usage: cum, StopReason: provider.StopCancelled, Iterations: iter}, err
		}

		msgs = r.transformContext(ctx, msgs)

		assistant, calls, usage, s, err := r.callModel(ctx, msgs)
		cum = cum.Add(usage)
		stop = s
		if err != nil {
			// callModel already emitted session.error + turn.finished.
			return Result{Messages: msgs, Usage: cum, StopReason: stop, Iterations: iter + 1}, err
		}
		msgs = append(msgs, assistant)

		if stop != provider.StopToolUse || len(calls) == 0 {
			return Result{Messages: msgs, Usage: cum, StopReason: stop, Iterations: iter + 1}, nil
		}

		results := r.runTools(ctx, calls)
		msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: results})

		msgs = r.prepareNextTurn(ctx, TurnState{Messages: msgs, Iteration: iter, LastStop: stop, Usage: cum})
	}

	// Iteration cap reached with the model still requesting tools.
	r.emitError(fmt.Sprintf("loop: iteration cap (%d) reached", maxIters), false)
	return Result{Messages: msgs, Usage: cum, StopReason: stop, Iterations: iter}, nil
}

// runner carries per-Run state and helpers.
type runner struct {
	cfg    Config
	stream StreamFn
}

func (r *runner) broker() *event.Broker { return r.cfg.Broker }

func (r *runner) emitError(msg string, fatal bool) {
	r.broker().Publish(event.NewSessionError(r.cfg.SessionID, msg, fatal))
}

// callModel makes one model call, converts its stream into contract events, and
// returns the assembled assistant message, the tool calls it requested, the
// turn's usage, and its stop reason. On stream failure it emits session.error +
// turn.finished and returns the error.
func (r *runner) callModel(ctx context.Context, msgs []provider.Message) (provider.Message, []ToolCall, provider.Usage, provider.StopReason, error) {
	sid := r.cfg.SessionID
	r.broker().Publish(event.NewTurnStarted(sid))

	req := provider.Request{
		Model:    r.cfg.Model,
		System:   r.cfg.System,
		Messages: msgs,
		Params:   r.cfg.Params,
	}
	if r.cfg.Tools != nil {
		req.Tools = r.cfg.Tools.Specs()
	}

	stream, err := r.stream(ctx, req)
	if err != nil {
		r.emitError(err.Error(), true)
		r.broker().Publish(event.NewTurnFinished(sid, string(provider.StopError), provider.Usage{}))
		return provider.Message{}, nil, provider.Usage{}, provider.StopError, err
	}
	defer func() { _ = stream.Close() }()

	conv := newConverter(r.broker(), sid)
	for {
		if err := ctx.Err(); err != nil {
			conv.flush()
			r.emitError(err.Error(), true)
			r.broker().Publish(event.NewTurnFinished(sid, string(provider.StopCancelled), conv.usage))
			return provider.Message{}, nil, conv.usage, provider.StopCancelled, err
		}
		se, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			conv.flush()
			r.emitError(err.Error(), true)
			r.broker().Publish(event.NewTurnFinished(sid, string(provider.StopError), conv.usage))
			return provider.Message{}, nil, conv.usage, provider.StopError, err
		}
		conv.handle(se)
	}
	conv.flush()

	// Fail closed if the provider ended the stream without a terminal Finished
	// event: surface a non-fatal error and mark the turn as errored rather than
	// silently returning an empty stop reason.
	stop := conv.stop
	if !conv.finished {
		r.emitError("provider stream ended without a finished event", false)
		stop = provider.StopError
	}

	assistant := provider.Message{Role: provider.RoleAssistant, Content: conv.blocks}
	r.broker().Publish(r.turnFinished(stop, conv.usage))
	return assistant, conv.calls, conv.usage, stop, nil
}

// turnFinished builds a turn.finished event, pricing the usage when the model is
// in the registry.
func (r *runner) turnFinished(stop provider.StopReason, usage provider.Usage) event.TurnFinished {
	if cost, ok := provider.CostOf(r.cfg.Model, usage); ok {
		return event.NewTurnFinishedCost(r.cfg.SessionID, string(stop), usage, &cost)
	}
	return event.NewTurnFinished(r.cfg.SessionID, string(stop), usage)
}

// runTools executes each requested tool call (through the hooks) and returns the
// tool_result content blocks to append as the next user message.
func (r *runner) runTools(ctx context.Context, calls []ToolCall) []provider.ContentBlock {
	blocks := make([]provider.ContentBlock, 0, len(calls))
	for _, call := range calls {
		// Honor cancellation between tool calls: emit a cancelled result for each
		// remaining call rather than invoking it, keeping the message well-formed
		// (every tool_use gets a matching tool_result).
		if err := ctx.Err(); err != nil {
			res := ToolResult{Content: "cancelled: " + err.Error(), IsError: true}
			r.broker().Publish(event.NewToolCallFinished(r.cfg.SessionID, call.ID, res.Content, nil))
			blocks = append(blocks, provider.ToolResultBlock(call.ID, res.Content, true))
			continue
		}
		call = r.beforeTool(ctx, call)

		var res ToolResult
		if r.cfg.Tools == nil {
			res = ToolResult{Content: fmt.Sprintf("no tool registry configured for tool %q", call.Name), IsError: true}
		} else if tool, ok := r.cfg.Tools.Get(call.Name); !ok {
			res = ToolResult{Content: fmt.Sprintf("unknown tool %q", call.Name), IsError: true}
		} else if out, err := tool.Run(ctx, call.Input); err != nil {
			res = ToolResult{Content: err.Error(), IsError: true}
		} else {
			res = out
		}

		res = r.afterTool(ctx, call, res)

		r.broker().Publish(event.NewToolCallFinished(r.cfg.SessionID, call.ID, res.Content, res.Diagnostics))
		blocks = append(blocks, provider.ToolResultBlock(call.ID, res.Content, res.IsError))
	}
	return blocks
}

// --- never-throw hook wrappers ---

func (r *runner) transformContext(ctx context.Context, msgs []provider.Message) []provider.Message {
	if r.cfg.Hooks.TransformContext == nil {
		return msgs
	}
	out, err := r.cfg.Hooks.TransformContext(ctx, msgs)
	if err != nil {
		r.emitError("transformContext hook: "+err.Error(), false)
		return msgs
	}
	return out
}

func (r *runner) beforeTool(ctx context.Context, call ToolCall) ToolCall {
	if r.cfg.Hooks.BeforeTool == nil {
		return call
	}
	out, err := r.cfg.Hooks.BeforeTool(ctx, call)
	if err != nil {
		r.emitError("beforeTool hook: "+err.Error(), false)
		return call
	}
	return out
}

func (r *runner) afterTool(ctx context.Context, call ToolCall, res ToolResult) ToolResult {
	if r.cfg.Hooks.AfterTool == nil {
		return res
	}
	out, err := r.cfg.Hooks.AfterTool(ctx, call, res)
	if err != nil {
		r.emitError("afterTool hook: "+err.Error(), false)
		return res
	}
	return out
}

func (r *runner) prepareNextTurn(ctx context.Context, state TurnState) []provider.Message {
	if r.cfg.Hooks.PrepareNextTurn == nil {
		return state.Messages
	}
	out, err := r.cfg.Hooks.PrepareNextTurn(ctx, state)
	if err != nil {
		r.emitError("prepareNextTurn hook: "+err.Error(), false)
		return state.Messages
	}
	return out.Messages
}

// converter turns a provider stream into contract events while assembling the
// assistant message content, the requested tool calls, and the turn's usage.
type converter struct {
	broker *event.Broker
	sid    string

	// open message (text or reasoning) state.
	open bool
	kind event.MessageKind
	buf  strings.Builder

	// in-flight tool call assembly, keyed by id in call order.
	toolIdx map[string]int
	partial []toolAssembly

	blocks   []provider.ContentBlock
	calls    []ToolCall
	usage    provider.Usage
	stop     provider.StopReason
	finished bool // a StreamFinished event was observed
}

type toolAssembly struct {
	id   string
	name string
	buf  strings.Builder
	seed json.RawMessage
}

func newConverter(b *event.Broker, sid string) *converter {
	return &converter{broker: b, sid: sid, toolIdx: map[string]int{}}
}

func (c *converter) handle(se provider.StreamEvent) {
	switch se.Type {
	case provider.StreamTextDelta:
		c.delta(event.MessageText, se.Text)
	case provider.StreamReasoningDelta:
		c.delta(event.MessageReasoning, se.Text)
	case provider.StreamToolCallStart:
		c.toolStart(se.Tool)
	case provider.StreamToolCallDelta:
		c.toolDelta(se.Tool)
	case provider.StreamToolCallEnd:
		c.toolEnd(se.Tool)
	case provider.StreamFinished:
		c.usage = se.Usage
		c.stop = se.StopReason
		c.finished = true
	}
}

// delta opens a message of the given kind (closing a different open message)
// and emits a message.delta while accumulating the settled content.
func (c *converter) delta(kind event.MessageKind, chunk string) {
	if c.open && c.kind != kind {
		c.closeMessage()
	}
	if !c.open {
		c.kind = kind
		c.open = true
		c.broker.Publish(event.NewMessageStarted(c.sid, kind))
	}
	c.buf.WriteString(chunk)
	c.broker.Publish(event.NewMessageDelta(c.sid, kind, chunk))
}

// closeMessage emits message.finished for the open message and records the
// settled content block.
func (c *converter) closeMessage() {
	if !c.open {
		return
	}
	content := c.buf.String()
	c.broker.Publish(event.NewMessageFinished(c.sid, c.kind, content))
	switch c.kind {
	case event.MessageReasoning:
		c.blocks = append(c.blocks, provider.ReasoningBlock(content))
	default:
		c.blocks = append(c.blocks, provider.TextBlock(content))
	}
	c.buf.Reset()
	c.open = false
}

func (c *converter) toolStart(t *provider.ToolCall) {
	if t == nil {
		return
	}
	c.closeMessage()
	c.toolIdx[t.ID] = len(c.partial)
	c.partial = append(c.partial, toolAssembly{id: t.ID, name: t.Name, seed: t.Input})
	c.broker.Publish(event.NewToolCallStarted(c.sid, t.ID, t.Name, t.Input))
}

func (c *converter) toolDelta(t *provider.ToolCall) {
	if t == nil {
		return
	}
	if i, ok := c.toolIdx[t.ID]; ok {
		c.partial[i].buf.WriteString(t.Delta)
	}
	c.broker.Publish(event.NewToolCallDelta(c.sid, t.ID, t.Delta))
}

func (c *converter) toolEnd(t *provider.ToolCall) {
	if t == nil {
		return
	}
	// Idempotent per id: a duplicate End for an already-finalized call must not
	// append a second tool_use block (which would execute the tool twice).
	if c.callSeen(t.ID) {
		return
	}
	i, ok := c.toolIdx[t.ID]
	if !ok {
		// End without a Start: synthesize an assembly so the call is not lost.
		i = len(c.partial)
		c.toolIdx[t.ID] = i
		c.partial = append(c.partial, toolAssembly{id: t.ID, name: t.Name, seed: t.Input})
	}
	a := &c.partial[i]
	if t.Name != "" {
		a.name = t.Name
	}
	input := assembledInput(a, t.Input)
	c.blocks = append(c.blocks, provider.ToolUseBlock(a.id, a.name, input))
	c.calls = append(c.calls, ToolCall{ID: a.id, Name: a.name, Input: input})
}

// assembledInput resolves a tool call's final input: the End event's assembled
// input if present, else the accumulated deltas, else the Start seed.
func assembledInput(a *toolAssembly, end json.RawMessage) json.RawMessage {
	if len(end) > 0 {
		return end
	}
	if a.buf.Len() > 0 {
		return json.RawMessage(a.buf.String())
	}
	return a.seed
}

// flush closes any open message. Tool calls that saw a Start but no End are
// finalized from their accumulated deltas so no requested call is dropped.
func (c *converter) flush() {
	c.closeMessage()
	for i := range c.partial {
		a := &c.partial[i]
		if c.callSeen(a.id) {
			continue
		}
		input := assembledInput(a, nil)
		c.blocks = append(c.blocks, provider.ToolUseBlock(a.id, a.name, input))
		c.calls = append(c.calls, ToolCall{ID: a.id, Name: a.name, Input: input})
	}
}

func (c *converter) callSeen(id string) bool {
	for _, call := range c.calls {
		if call.ID == id {
			return true
		}
	}
	return false
}
