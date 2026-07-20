package acp

// ProtocolVersion is the ACP protocol version this package targets.
const ProtocolVersion = 1

// ACP JSON-RPC method names. The consuming application's transport dispatches
// on these; this package does not perform dispatch itself.
const (
	// MethodInitialize negotiates the protocol version and capabilities.
	MethodInitialize = "initialize"
	// MethodAuthenticate performs out-of-band authentication.
	MethodAuthenticate = "authenticate"
	// MethodSessionNew creates a new session.
	MethodSessionNew = "session/new"
	// MethodSessionLoad reloads a persisted session.
	MethodSessionLoad = "session/load"
	// MethodSessionPrompt sends a prompt to a session.
	MethodSessionPrompt = "session/prompt"
	// MethodSessionCancel cancels the running turn of a session.
	MethodSessionCancel = "session/cancel"
	// MethodSessionUpdate is the notification method the agent sends to
	// stream session updates to the client.
	MethodSessionUpdate = "session/update"
	// MethodSessionRequestPermission asks the client to decide a tool call.
	MethodSessionRequestPermission = "session/request_permission"
	// MethodSessionList lists existing sessions (session metadata + pagination).
	MethodSessionList = "session/list"
	// MethodSessionSetConfigOption sets a session configuration option (the
	// stable ACP v1 model/mode/thought-level selector and boolean-toggle
	// mechanism).
	MethodSessionSetConfigOption = "session/set_config_option"
	// MethodSessionRequestDecision asks the client to answer one or more
	// structured questions (a decision) — distinct from a tool-call permission.
	MethodSessionRequestDecision = "session/request_decision"
)
