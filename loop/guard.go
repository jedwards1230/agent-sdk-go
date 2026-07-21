package loop

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/permission"
)

// Decision is a guard's verdict on a tool call before execution (M3:
// binary+deny).
type Decision int

const (
	// DecisionRunContained means a sandbox backend can contain the call; run it.
	DecisionRunContained Decision = iota
	// DecisionAsk means the call can't be contained → escalate to a human.
	DecisionAsk
	// DecisionDeny means a static rule denied the call → block it.
	DecisionDeny
)

// Guarding is a guard's decision plus what the permission events carry.
type Guarding struct {
	// Decision is the guard's verdict.
	Decision Decision
	// Rule is the matched-rule / reason label, carried onto
	// event.PermissionResolved.Rule.
	Rule string
	// Spec is an args summary carried onto event.PermissionRequested.Spec; the
	// loop fills it from call.Input when nil.
	Spec map[string]any
	// Trace is the decision trace carried onto event.PermissionRequested.Trace.
	Trace []string
}

// Guard decides how each tool call is handled before execution. Injected on
// Config; nil ⇒ every call runs uncontained (legacy behavior, no gating).
type Guard interface {
	Evaluate(ctx context.Context, call ToolCall) Guarding
}

// Granter is an optional Guard capability: record a remembered allow so future
// identical calls skip the ask. Persistence policy is the guard's concern.
type Granter interface {
	Grant(call ToolCall)
}

// Reply is a human's answer to a permission request (mirrors
// event.PermissionReply).
type Reply struct {
	Verdict  event.Verdict
	Remember bool
	// Input, when non-nil, is replacement tool input supplied with an amended
	// allow: the call runs with this input in place of the model's original
	// arguments. It is honored only when Verdict is event.VerdictAllow; a nil
	// Input leaves the original call unchanged (the plain allow/deny path).
	Input json.RawMessage
}

// Approver awaits a human's reply to an emitted permission request. Await
// blocks until the matching permission.reply arrives or ctx is done. Required
// on Config if the Guard can return DecisionAsk; nil ⇒ an ask fails closed
// (deny).
type Approver interface {
	Await(ctx context.Context, id string) (Reply, error)
}

// Container reports whether a tool call can be contained by a sandbox backend
// on this host. The SDK defines ONLY this interface — concrete backends
// (Seatbelt, bwrap+seccomp) live in the consuming application. NEVER add a
// backend here.
type Container interface {
	CanContain(ctx context.Context, call ToolCall) (bool, error)
}

// RuleGuard composes a permission.Engine with an optional Container into the
// M3 binary+deny policy:
//
//	deny rule            → DecisionDeny
//	ask rule / unmatched → DecisionAsk
//	allow rule           → DecisionRunContained if Container.CanContain, else
//	                       DecisionAsk (never run uncontained — decided
//	                       2026-07-13)
type RuleGuard struct {
	Engine *permission.Engine
	// Container reports containability for an allow-matched call. Nil ⇒ never
	// containable (every allow escalates to Ask).
	Container Container
	// Target extracts the specifier-match string from a call. Nil ⇒ "".
	Target func(ToolCall) string
}

// Evaluate implements Guard.
func (g RuleGuard) Evaluate(ctx context.Context, call ToolCall) Guarding {
	target := g.target(call)
	args := decodeInput(call.Input)
	verdict, rule, matched := g.Engine.Evaluate(permission.Request{Tool: call.Name, Target: target, Args: args})
	label := ruleLabel(verdict, rule, matched)
	trace := []string{"rule: " + label}

	switch verdict {
	case event.VerdictDeny:
		return Guarding{Decision: DecisionDeny, Rule: label, Spec: args, Trace: trace}
	case event.VerdictAllow:
		return g.containOrAsk(ctx, call, label, args, trace)
	default: // event.VerdictAsk, or an unrecognized verdict — fail closed to ask.
		return Guarding{Decision: DecisionAsk, Rule: label, Spec: args, Trace: trace}
	}
}

// containOrAsk resolves an allow-matched rule to run-contained or ask,
// depending on whether the configured Container reports the call as
// containable. Container error ⇒ fail closed to Ask (containment uncertain ⇒
// ask a human).
func (g RuleGuard) containOrAsk(ctx context.Context, call ToolCall, label string, args map[string]any, trace []string) Guarding {
	if g.Container == nil {
		trace = append(trace, "containable: false (no container configured)")
		return Guarding{Decision: DecisionAsk, Rule: label, Spec: args, Trace: trace}
	}
	ok, err := g.Container.CanContain(ctx, call)
	if err != nil {
		trace = append(trace, "containable: error: "+err.Error())
		return Guarding{Decision: DecisionAsk, Rule: label, Spec: args, Trace: trace}
	}
	trace = append(trace, fmt.Sprintf("containable: %v", ok))
	if ok {
		return Guarding{Decision: DecisionRunContained, Rule: label, Spec: args, Trace: trace}
	}
	return Guarding{Decision: DecisionAsk, Rule: label, Spec: args, Trace: trace}
}

// Grant implements Granter: it records call as a remembered allow rule.
func (g RuleGuard) Grant(call ToolCall) {
	g.Engine.Grant(permission.Rule{
		Verdict:   event.VerdictAllow,
		Tool:      call.Name,
		Specifier: g.target(call),
		Source:    "session",
	})
}

func (g RuleGuard) target(call ToolCall) string {
	if g.Target == nil {
		return ""
	}
	return g.Target(call)
}

// ruleLabel builds a readable rule label for the permission events: the
// matched rule's Source when set, else a verdict/tool/specifier summary; an
// unmatched request is labeled "unmatched".
func ruleLabel(verdict event.Verdict, rule permission.Rule, matched bool) string {
	if !matched {
		return "unmatched"
	}
	if rule.Source != "" {
		return rule.Source
	}
	return string(verdict) + " " + rule.Tool + "(" + rule.Specifier + ")"
}
