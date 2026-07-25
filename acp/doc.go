// Package acp models the Agent Client Protocol (ACP) v1 wire types and the
// pure projection functions between them and this SDK's typed [event.Event] /
// [event.Op] contract.
//
// It is clean-roomed from the public ACP v1 JSON schema
// (zed-industries/agent-client-protocol) — no dependency on that project or
// any other ACP implementation. The protocol version targeted is
// [ProtocolVersion] (1), the latest stable release at time of writing.
//
// # SDK extensions beyond ACP v1
//
// Three surfaces in this package are additive SDK extensions, NOT part of the
// published v1 schema. A stock ACP client will not decode them, so an embedder
// that talks to third-party clients should treat them as opt-in:
//
//   - [PermissionOutcomeAmended] — outcome "amended"; the v1 schema's outcome
//     union is only "selected" | "cancelled".
//   - [MethodSessionExplainPermission] ("session/explain_permission") — a
//     read-only rationale query for a pending request_permission.
//   - [MethodSessionRequestDecision] ("session/request_decision") — the
//     agent-initiated structured-question surface.
//
// Each is additive: the unextended paths (plain selected/cancelled outcomes, no
// explain query, no decision request) remain exactly the v1 behavior.
//
// This package is transport-agnostic by design: it owns message TYPES and
// MAPPING FUNCTIONS only. It does no networking, no JSON-RPC framing, and
// spawns no goroutines — stdlib only. The WebSocket transport and JSON-RPC
// method dispatch that carry these types over the wire live in the consuming
// application, which imports this package and wires its projection functions
// onto a broker subscription. That wiring is a straightforward application of
// the "every frontend is a client of the broker" invariant ([event] package
// doc): an ACP session is just another subscriber, translating [event.Event]
// to session/update notifications via [ToSessionUpdate] and ACP requests to
// [event.Op] values via the From* functions in this package.
//
// The ACP-JSON boundary lives here in both directions: outbound types marshal
// to their wire shape via their own MarshalJSON, and inbound [DecodeOp] turns a
// JSON-RPC method name plus raw params straight into the typed [event.Op] it
// projects to. A transport therefore only frames bytes — it never reaches into
// ACP field internals or hand-rolls an Event/Op codec.
package acp
