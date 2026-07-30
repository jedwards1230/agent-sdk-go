package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jedwards1230/agent-sdk-go/tool"
)

// Project lists server's tools ([Client.ListTools]) and wraps each as a
// [tool.Tool] whose Run performs one "tools/call" against server. This is the
// load-bearing output of this package: an MCP tool and a builtin tool become
// the same Go type in the same [tool.Registry], so anything layered above a
// registry (permission gating, an index-first context projection,
// diagnostics) applies to both structurally, not by convention. Duplicate
// projected names (two servers, or two tools on one server, whose sanitized
// names collide) are not de-duplicated here — that surfaces the normal way,
// as [tool.Registry.Register]'s [tool.ErrDuplicate], when the caller
// registers the returned tools.
//
// server is a short, stable identifier for this connection (e.g. a config
// key like "wiki" or "home-assistant") — it never touches the wire; it only
// feeds [qualifiedToolName] and error messages, so a caller should pick
// something a human recognizes over the connection's URL or PID.
//
// Project is a one-shot snapshot, deliberately: call it once when a session
// is created, register the result, and stop. A session's tool set is fixed
// at create — nothing in this package re-projects on its own, and a caller
// must not call Project again to hot-add a late-connecting server's tools
// into an already-running session's registry (a server that finishes
// connecting after the session started joins the NEXT session). See
// docs/DESIGN.md "MCP (M7)" and the mcp package doc for why this is a hard
// invariant, not a style preference.
func Project(ctx context.Context, c *Client, server string) ([]tool.Tool, error) {
	infos, err := c.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: project tools from %s: %w", server, err)
	}
	out := make([]tool.Tool, 0, len(infos))
	for _, info := range infos {
		out = append(out, &projectedTool{
			client:   c,
			server:   server,
			original: info.Name,
			name:     qualifiedToolName(server, info.Name),
			desc:     info.Description,
			schema:   schemaFromJSON(info.InputSchema),
		})
	}
	return out, nil
}

// projectedTool implements [tool.Tool] over one MCP server tool. It carries
// no state beyond its identity and the [Client] it calls through — a
// projectedTool is stateless configuration, safe to share and to keep
// registered even while its server is unreachable (see Run).
type projectedTool struct {
	client   *Client
	server   string
	original string // the server's own tool name, unsanitized — what tools/call is sent
	name     string // qualifiedToolName(server, original) — what the model sees
	desc     string
	schema   tool.Schema
}

func (t *projectedTool) Name() string        { return t.name }
func (t *projectedTool) Description() string { return t.desc }
func (t *projectedTool) Spec() tool.Schema   { return t.schema }

// OriginalName reports the tool's un-sanitized MCP name, for display or audit
// alongside the qualified [Name] a permission rule or the model actually
// sees.
func (t *projectedTool) OriginalName() string { return t.original }

// Server reports the identifier Project was called with, so a consumer can
// group projected tools back by their originating connection.
func (t *projectedTool) Server() string { return t.server }

// Run performs one "tools/call" against the server. Per [tool.Tool]'s
// (Result, error) contract: a non-nil error means ctx itself (the one Run was
// given) ended the call — the loop aborts the turn, exactly as for any other
// tool. Everything else — the server unreachable, its connection dying
// mid-call, this client's own per-call timeout elapsing, a malformed
// response, or the server's own JSON-RPC error — is a call that "could not
// run as asked" only from the transport's point of view, not the model's: the
// model can still usefully react to being told the tool failed, so it
// surfaces as a normal (nil-error) [tool.Result] with IsError set. This is
// the resilience contract: a dead or slow MCP server can degrade one tool
// call, never fail a whole turn, and never causes the tool to be
// unregistered — the caller sees exactly the same []tool.Tool on the next
// call too.
//
// The distinction between "ctx ended the call" and "the transport failed" is
// ctx.Err(): [Client.CallTool] derives its own bounded child context
// internally (see [WithCallTimeout]), so a timeout that child context alone
// hit leaves the ctx Run received un-cancelled, while an ctx cancellation the
// caller actually requested (interrupt, turn cancellation) always reports on
// ctx.Err() too.
func (t *projectedTool) Run(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	res, err := t.client.CallTool(ctx, t.original, input)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return tool.Result{}, ctxErr
		}
		return tool.Result{
			IsError: true,
			Content: fmt.Sprintf("mcp server %s tool %s failed: %v", t.server, t.original, err),
		}, nil
	}
	return tool.Result{IsError: res.IsError, Content: res.Text}, nil
}
