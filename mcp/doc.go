// Package mcp is the SDK's optional Model Context Protocol seam: a
// hand-rolled JSON-RPC 2.0 client (stdio and streamable-HTTP transports) and
// the projection of a connected server's tools onto [tool.Tool].
//
// # Why hand-rolled, not the official go-sdk
//
// MCP is the same protocol family [lsp.Client] already speaks — JSON-RPC 2.0
// framed over stdio (here: newline-delimited, not LSP's Content-Length
// headers) or over HTTP. This package follows lsp/client.go's structure
// rather than importing modelcontextprotocol/go-sdk, so an optional,
// networked package still costs every embedder nothing in their module graph
// unless they import it — the SDK's three direct dependencies (uuid,
// yaml.v3, x/crypto) stay exactly that. See docs/DESIGN.md "MCP (M7)" for the
// full ruling, which supersedes the earlier 2026-07-11 sourcing survey.
//
// # Client and projection, nothing else
//
// [Client] owns the MCP lifecycle: [Client.Initialize] (the handshake),
// [Client.ListTools], [Client.CallTool]. [Project] turns a connected
// Client's tool list into []tool.Tool — each one's Run performs exactly one
// "tools/call". Server configuration, credential resolution, the
// connection manager (reconnect/backoff/readiness), and any tool-index
// decorator are the consuming application's job; this package does not
// build them (see docs/milestones/M7-round-ab.md).
//
// # tools/list already returns full schemas
//
// The MCP spec's "tools/list" response carries every tool's complete JSON
// Schema alongside its name — there is no protocol affordance for fetching a
// name without its schema. So an index-first tool registry is a
// context-window projection an application performs AFTER [Project] returns,
// never a network optimization this package could chase by fetching schemas
// lazily against the wire.
//
// # Schema projection degrades visibly, never silently
//
// A server's inputSchema is projected onto [tool.Schema], which represents
// type/description/properties/required (top level and nested), enum, items,
// default, oneOf/anyOf/allOf, and patternProperties. Anything else — and
// "anything else" is deny-by-default, so a keyword JSON Schema gains later
// counts — is dropped, because a schema more permissive than the server's is
// a tool call the model will confidently get wrong. Only genuinely inert keys
// stay silent: the annotations ($schema, $id, $comment, title, examples,
// deprecated, readOnly, writeOnly) and the definition blocks ($defs,
// definitions, $vocabulary), which constrain nothing on their own — the $ref
// into a definition block is reported at its own path.
//
// Representing a composition keyword is conditional, not unconditional: a
// branch whose every keyword was unrepresentable would marshal as {}, the
// schema matching everything, so a oneOf/anyOf carrying one is dropped whole
// rather than emitted vacuously. allOf keeps its representable members.
//
// Every drop is reported twice, split by who pays for it. The model gets a
// "Schema note:" paragraph appended to the projected tool's Description with
// deduplicated keyword counts under a hard cap and no paths — a description
// rides every request, and paths are unbounded and server-controlled. An
// operator gets one Warn on the Client's logger (see [WithLogger]) with the
// exhaustive per-occurrence list, full paths, and the decode error when the
// schema could not be read at all. A schema that projects cleanly and uses no
// composition keeps the server's own Description byte for byte and logs
// nothing. See [projectSchema].
//
// # Tool naming and sanitization
//
// A projected tool is named "mcp__<server>__<tool>", matching
// docs/DESIGN.md's permission rule grammar (`mcp__search__*(*)`).
// Sanitization is mandatory: providers cap tool names at 64 bytes of
// [A-Za-z0-9_-], and a real federated name can exceed that. Illegal runes are
// replaced with '_'; a name still over 64 bytes truncates to its first 57
// bytes plus '_' plus the first 6 hex characters of the sha256 of the FULL
// sanitized name — deterministic, and collision-safe even between two names
// that agree on everything up to the cut. See [qualifiedToolName].
//
// # Resilience is this client's contract, not the connection manager's
//
// A dead, slow, or unreachable server must never fail a whole turn:
// [projectedTool.Run] reports that as a normal [tool.Result] with IsError
// set, never as a Go error — see its doc for the exact ctx.Err()-based rule
// that distinguishes a caller-requested cancellation (a real error, aborting
// the turn) from this package's own internal per-call timeout or a
// transport failure (an IsError result the model can react to). Because a
// projectedTool never becomes invalid — it always returns SOME [tool.Result]
// — a caller never needs to unregister it when its server misbehaves.
//
// # A session's tool set is fixed at create — this package never mutates one
//
// [Project] is a one-shot snapshot: call it, get back a []tool.Tool, register
// them once. Nothing in this package watches a server or re-projects on
// reconnect, and a consuming application's connection manager MUST preserve
// that: a session's registered tool set is decided once, at create (after
// whatever bounded readiness wait the application allows already-configured
// servers), and never grows or shrinks for the life of that session. A
// server that finishes connecting after a session is already running joins
// the NEXT session, not the live one; a server that dies mid-session keeps
// its already-projected tools registered (calls degrade to IsError per
// above, and resume working again on reconnect — no re-registration needed
// either way). This is not a style preference: a resident-tool-index
// decorator built on top of a session's registry (see
// docs/milestones/M7-round-ab.md, `toolindex`) snapshots that registry once
// and stakes a prompt-cache byte-identity guarantee on the tool array never
// growing afterward; hot-adding a late-connecting server's tools into a live
// session would silently break that guarantee with no test to catch it.
package mcp
