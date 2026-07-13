package acp

import "encoding/json"

// InitializeRequest is the payload of an initialize request, opening an ACP
// session and negotiating the protocol version.
type InitializeRequest struct {
	// ProtocolVersion is the highest ACP protocol version the client speaks.
	ProtocolVersion int `json:"protocolVersion"`
	// ClientCapabilities carries the client's capability declaration opaquely;
	// this package does not project it.
	ClientCapabilities json.RawMessage `json:"clientCapabilities,omitempty"`
}

// AgentCapabilities declares what an agent supports, per the ACP v1 schema
// (src/v1/agent.rs AgentCapabilities). Only the fields gofer advertises are
// modeled; the rest default to their zero (unsupported) values.
type AgentCapabilities struct {
	// LoadSession advertises session/load support (schema: loadSession,
	// default false). Omitted when false — a client treats absent as false.
	LoadSession bool `json:"loadSession,omitempty"`
	// SessionCapabilities advertises optional session-method support; omitted
	// when empty so a no-capability agent still marshals "agentCapabilities":{}.
	SessionCapabilities SessionCapabilities `json:"sessionCapabilities,omitzero"`
}

// SessionCapabilities advertises optional session methods (schema:
// SessionCapabilities). A present List advertises session/list.
type SessionCapabilities struct {
	// List, when non-nil, advertises session/list. Per the schema, supplying
	// {} means supported; omitted/null means not advertised.
	List *SessionListCapabilities `json:"list,omitempty"`
}

// SessionListCapabilities is the (currently field-less) capability object for
// session/list; its presence as {} is the advertisement.
type SessionListCapabilities struct{}

// AuthMethod describes an authentication method an agent offers. This
// package's agents authenticate out of band, so [InitializeResponse] always
// reports an empty method list; the type exists for wire completeness.
type AuthMethod struct {
	// ID identifies the auth method.
	ID string `json:"id"`
	// Name is a display name for the auth method.
	Name string `json:"name"`
}

// InitializeResponse is the payload of an initialize response.
type InitializeResponse struct {
	// ProtocolVersion is the protocol version the agent will use.
	ProtocolVersion int `json:"protocolVersion"`
	// AgentCapabilities declares what the agent supports.
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	// AuthMethods lists the agent's supported authentication methods.
	AuthMethods []AuthMethod `json:"authMethods"`
}

// NewInitializeResponse builds an [InitializeResponse] at [ProtocolVersion]
// with minimal capabilities and no auth methods.
func NewInitializeResponse() InitializeResponse {
	return InitializeResponse{
		ProtocolVersion:   ProtocolVersion,
		AgentCapabilities: AgentCapabilities{},
		AuthMethods:       []AuthMethod{},
	}
}
