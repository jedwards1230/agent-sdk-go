package announce

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jedwards1230/agent-sdk-go/device"
)

// Payload is what one agent server publishes about itself: who it is, where it
// might be reachable, what it is running, and what a holder of it is allowed to
// do. It is the whole announce vocabulary — a transport carries this value, a
// client reads it, and neither concern belongs in this package.
//
// A payload is a snapshot, not a stream: it describes the server at the moment
// it was built. Re-announcing means building a new one.
type Payload struct {
	// ServerID is the stable identifier of this server instance — stable
	// across restarts, unique across the account. Required.
	//
	// The SDK does not mint it and imposes no format, but a UUIDv7 is the
	// house style (session ids are UUIDv7 so they sort time-ordered; see the
	// architecture invariants in CLAUDE.md) and is the recommended choice.
	ServerID string `json:"server_id" yaml:"server_id"`

	// AccountID identifies the account this server belongs to. Required.
	//
	// It is an opaque grouping key: the SDK never resolves it, looks it up, or
	// attaches an identity provider to it. Two payloads sharing an AccountID
	// claim to belong together; deciding whether to believe that claim is the
	// application's job.
	AccountID string `json:"account_id" yaml:"account_id"`

	// DisplayName is the human-readable label for this server, for a roster row
	// or a picker ("justin's laptop"). Optional — a client falls back to
	// ServerID. It is decoration and carries no identity weight; never
	// authenticate or route on it.
	DisplayName string `json:"display_name,omitempty" yaml:"display_name,omitempty"`

	// Endpoints are the candidate paths by which this server MIGHT be
	// reachable, best-first by Endpoint.Priority. At least one is required.
	//
	// A list, not an address: see the package doc. The SDK neither orders nor
	// probes them — it carries the server's stated preference, and the client
	// decides what to try.
	Endpoints []Endpoint `json:"endpoints" yaml:"endpoints"`

	// Sessions are coarse summaries of the sessions this server is hosting,
	// enough to render a roster without attaching to any of them. Optional: a
	// server with no sessions announces none.
	Sessions []SessionSummary `json:"sessions,omitempty" yaml:"sessions,omitempty"`

	// Credential is the opaque credential slot. See [Credential]: the SDK
	// carries it and does nothing else with it. Optional.
	//
	// The tags differ between the two codecs on purpose, and both are load
	// bearing. Credential is a struct, and encoding/json's omitempty does NOT
	// omit a zero struct — only Go 1.24+'s omitzero does (via the IsZero
	// method). yaml.v3, meanwhile, does not know the word "omitzero" at all:
	// an unrecognized flag makes it PANIC at marshal time, and its omitempty
	// already omits a zero struct (it consults IsZero through its own
	// IsZeroer interface). So: omitzero for JSON, omitempty for YAML. Both
	// were measured, not assumed; TestPayloadOmitsEmptyCredential pins the
	// emitted bytes.
	Credential Credential `json:"credential,omitzero" yaml:"credential,omitempty"`

	// DeviceKey is this device's session-encryption public key: the key a
	// client seals an envelope to, so a relay in the middle carries session
	// content it cannot read. REQUIRED — [Payload.Validate] rejects the zero
	// key.
	//
	// A keyless server is not a degraded server, it is one that cannot take
	// part. End-to-end encryption is a hard requirement of this protocol, and
	// a server with no key offers nothing an envelope can be sealed to.
	// Accepting a keyless announce would open a silent downgrade path: a
	// client reads a well-formed, validated payload, finds no key, and either
	// falls back to cleartext or fails at the first envelope — in both cases
	// having already trusted a peer it can never encrypt to. Rejecting it in
	// Validate makes that failure loud, early, and impossible to miss.
	//
	// This does not change device.PublicKey. The zero key stays
	// REPRESENTABLE and stays PARSEABLE — a fixed-size array leaves no room
	// for an "absent" state, and device.ParsePublicKey must accept a
	// well-formed all-zero encoding like any other. It is simply no longer
	// VALID IN A PAYLOAD. That is the same parse-versus-validity split
	// device.PublicKey.Valid documents, applied one level up: parsing says the
	// bytes are well formed, validity says the value is usable here.
	//
	// Readers still test presence with device.PublicKey.Valid, never against
	// the encoded form — the zero key encodes as its all-zero base64 string,
	// not as an empty one.
	DeviceKey device.PublicKey `json:"device_key" yaml:"device_key"`

	// Scope is the capability the holder of this payload is being granted. It
	// has no safe zero value and must be set explicitly; see [Scope].
	Scope Scope `json:"scope" yaml:"scope"`
}

// EndpointKind labels the sort of network path an endpoint represents, so a
// client can order candidates by class before it tries any of them (prefer the
// LAN, fall back to the tailnet, fall back to a relay).
//
// The set is OPEN. The constants below are the kinds named today; a payload may
// carry any other string, and one that does still decodes, still validates, and
// still round-trips. Treat an unrecognized kind as "a path I do not know how to
// use" and skip it — never as a decode error. That openness is what lets a new
// path be introduced without a breaking change.
type EndpointKind string

// The endpoint kinds named today. The set is open — see [EndpointKind].
const (
	// EndpointKindLAN is a direct address on the local network.
	EndpointKindLAN EndpointKind = "lan"
	// EndpointKindTailnet is an address on an overlay/mesh network.
	EndpointKindTailnet EndpointKind = "tailnet"
	// EndpointKindRelay is an address reached through a relay that forwards
	// traffic it cannot read.
	EndpointKindRelay EndpointKind = "relay"
)

// Endpoint is one candidate path to a server. It is a claim about reachability,
// not a proven route: the client that finds a working path is the authority.
type Endpoint struct {
	// Kind labels the class of path. Required; the set is open.
	Kind EndpointKind `json:"kind" yaml:"kind"`

	// Address is the path itself, opaque to this SDK. Required.
	//
	// It is never parsed, resolved, validated, or dialed here — not even for
	// syntax. It is whatever string the transport that will use it understands:
	// a host:port, a URL, a node name, a relay ticket. Constraining it would
	// make a future transport a breaking change.
	Address string `json:"address" yaml:"address"`

	// Priority orders candidates, LOWEST FIRST, in the style of a DNS SRV
	// record: 0 is tried before 10. Equal priorities are unordered relative to
	// each other, so slice order is the tiebreak. The zero value is therefore
	// "most preferred", which makes a single-endpoint payload correct without
	// setting anything.
	Priority int `json:"priority" yaml:"priority"`
}

// SessionState is the coarse, roster-level state of a session — enough to
// render a row, not enough to reconstruct anything. The set is OPEN for the
// same reason [EndpointKind] is: an unrecognized state must render as unknown,
// not fail to decode.
type SessionState string

// The session states named today. The set is open — see [SessionState].
const (
	// SessionStateIdle means the session is attached to nothing and waiting for
	// input.
	SessionStateIdle SessionState = "idle"
	// SessionStateBusy means a turn is running.
	SessionStateBusy SessionState = "busy"
	// SessionStateWaiting means the session is blocked on the user — a pending
	// permission request, say. It is the one state a roster must be able to
	// surface without attaching, because it is the one that needs a human.
	SessionStateWaiting SessionState = "waiting"
)

// SessionSummary is a roster ROW, not a session: the least a client needs to
// list what a server is running before deciding whether to attach.
//
// It deliberately carries no transcript, no messages, no tool calls, no token
// counts, and no cost. Those arrive over the Event/Op contract once a client
// attaches; duplicating them here would make an announce payload grow without
// bound and give a client a second, staler source of truth for session content.
type SessionSummary struct {
	// ID is the session's identifier, matching the id used on the Event/Op
	// contract so a client can attach with it. Required.
	ID string `json:"id" yaml:"id"`

	// Title is the session's human-readable label, if it has one. Optional —
	// titles are embedder-supplied and a session may have none.
	Title string `json:"title,omitempty" yaml:"title,omitempty"`

	// State is the coarse lifecycle state. Required; the set is open.
	State SessionState `json:"state" yaml:"state"`

	// LastActive is when the session last did anything, for sorting a roster
	// and for greying out stale rows. Required (a zero timestamp is rejected).
	LastActive time.Time `json:"last_active" yaml:"last_active"`
}

// Credential is the opaque credential slot: a bearer blob a client presents
// back to the server, carried verbatim.
//
// The SDK NEVER touches it. It does not fetch it, mint it, refresh it, expire
// it, validate it, decode it, or interpret it — it is an opaque string to this
// package and nothing more. That is why it is not a parsed token type: giving
// it structure here would imply the SDK understands (and therefore must keep up
// with) whatever scheme the application chose. Issuing and checking credentials
// is the application's job, on both ends.
//
// It is optional. A deployment that authenticates some other way announces
// without one.
//
// # Why it is a struct with a hidden field
//
// The value is a bearer secret: whoever holds it can act as the holder of this
// payload. The obvious modelling — a defined string type — puts that secret one
// careless format verb away from a log line, because a [Payload] is exactly the
// sort of value that gets printed while debugging. Wrapping it means the raw
// string is reachable only through [Credential.Reveal], so `grep -rn Reveal`
// enumerates every legitimate read of the secret in a codebase, and a
// [Payload] printed with any verb prints a redaction instead.
//
// This is containment, not encryption, and the boundary is drawn on purpose:
// the JSON and YAML codecs carry the REAL value, because the credential's whole
// job is to ride the wire. A redacting String alone was considered and
// rejected for the opposite reason — it would leave %d and every other
// non-string verb printing the field verbatim through reflection while looking
// protected, which is worse than no protection because it invites false
// confidence. [Credential.Format] closes that gap; serialization is the one
// path that is meant to be open, and it is documented here rather than left to
// be discovered.
type Credential struct{ value string }

// NewCredential wraps s as a Credential. The empty string yields the zero
// Credential, which is what "no credential announced" means.
func NewCredential(s string) Credential { return Credential{value: s} }

// Reveal returns the raw credential. It is the ONLY way to read the value, and
// that is the point: every legitimate use of the secret is a call to a method
// named Reveal, so they are all greppable in a codebase and all reviewable.
//
// Call it where the value goes on the wire. Do not park the result in a
// variable, put it in an error, or hand it to a logger — the wrapper stops
// being worth anything the moment a bare string escapes.
func (c Credential) Reveal() string { return c.value }

// IsZero reports whether no credential is set.
//
// It is also load-bearing for both codecs: encoding/json's omitzero calls it,
// and yaml.v3's omitempty calls it through that package's IsZeroer interface.
// Changing it changes what gets emitted — see the tags on
// [Payload.Credential].
func (c Credential) IsZero() bool { return c.value == "" }

// String implements [fmt.Stringer] with a redacted form: "[redacted]" when a
// credential is set, and the empty string when none is. It NEVER returns the
// value.
func (c Credential) String() string {
	if c.value == "" {
		return ""
	}
	return "[redacted]"
}

// GoString implements [fmt.GoStringer] so %#v redacts too. It deliberately
// emits something that is not valid Go source for reconstructing the value —
// there is no round trip through a debug print.
func (c Credential) GoString() string {
	if c.value == "" {
		return "announce.Credential{}"
	}
	return "announce.Credential{/* redacted */}"
}

// Format implements [fmt.Formatter], which fmt consults before every other
// method and for EVERY verb. That is why it exists.
//
// String and GoString are not sufficient on their own. fmt only reaches a
// Stringer for %v, %s, %q, %x and %X; any other verb falls through to
// reflection over the struct's fields, and reflection does not care that the
// field is unexported. Measured, before this method existed: %d on a struct
// holding a Credential printed `{{%!d(string=s3cr3t)}}` — the secret, in
// clear. Format routes every verb through the redacted forms instead, and
// reports an unsupported verb the way fmt itself does rather than quietly
// rendering a redaction as if it were the value.
func (c Credential) Format(f fmt.State, verb rune) {
	switch verb {
	case 'v':
		if f.Flag('#') {
			_, _ = io.WriteString(f, c.GoString())
			return
		}
		_, _ = io.WriteString(f, c.String())
	case 's':
		_, _ = io.WriteString(f, c.String())
	case 'q':
		_, _ = fmt.Fprintf(f, "%q", c.String())
	default:
		_, _ = fmt.Fprintf(f, "%%!%c(announce.Credential=%s)", verb, c.String())
	}
}

// MarshalJSON implements [json.Marshaler] and emits the REAL credential, not
// the redaction. Serializing a payload is how the credential reaches the peer
// that must present it back; a redacting codec would break the protocol while
// looking safe. Redaction is a defence against accidental printing, not against
// deliberate serialization — treat encoded payloads as secret-bearing.
func (c Credential) MarshalJSON() ([]byte, error) { return json.Marshal(c.value) }

// UnmarshalJSON implements [json.Unmarshaler]. A JSON null decodes to the zero
// Credential.
func (c *Credential) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("announce: unmarshal credential: %w", err)
	}
	c.value = s
	return nil
}

// MarshalYAML implements yaml.Marshaler and, like [Credential.MarshalJSON],
// emits the REAL credential. yaml.v3 does not consult encoding.TextMarshaler,
// so this pair is not redundant with the JSON pair.
func (c Credential) MarshalYAML() (any, error) { return c.value, nil }

// UnmarshalYAML implements yaml.Unmarshaler. A YAML null decodes to the zero
// Credential.
func (c *Credential) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("announce: unmarshal credential: %w", err)
	}
	c.value = s
	return nil
}

// Scope is the coarse capability a payload grants its holder.
//
// # Zero value
//
// The zero value is the empty scope, and it is NOT a driver. It is not a valid
// scope at all: [Payload.Validate] rejects it, and [Scope.CanDrive] reports
// false for it and for every value except [ScopeDriver] exactly. There is no
// path by which forgetting to set a scope, dropping the field from a payload,
// or receiving a scope this build does not recognize yields drive access —
// failing to say "driver" always means "not driver".
//
// # Why the field exists now
//
// Enforcement is not this package's job and will be refined over time; the
// field exists so the protocol has somewhere to SAY that a device may watch but
// not drive. Adding it later would be a breaking change, whereas adding new
// scopes to it is additive — so it ships now, coarse, with two values.
type Scope string

// The capability scopes.
const (
	// ScopeObserver may watch a session but may not drive it: read the event
	// stream, render a roster, attach read-only. No prompting, no answering a
	// permission request, no tool approval.
	ScopeObserver Scope = "observer"
	// ScopeDriver may drive: everything an observer may do, plus submitting
	// ops that act — prompting, answering permission requests, interrupting.
	ScopeDriver Scope = "driver"
)

// Known reports whether s is a scope this build recognizes. An unknown scope is
// rejected rather than ignored, so a payload minted by a newer peer with a scope
// this build cannot reason about never silently degrades into some default.
func (s Scope) Known() bool {
	switch s {
	case ScopeObserver, ScopeDriver:
		return true
	default:
		return false
	}
}

// CanDrive reports whether s grants drive access. It is true for [ScopeDriver]
// and false for everything else, including the zero value and any scope this
// build does not recognize. This is the vocabulary's statement of intent, not
// an enforcement point — the application still checks it.
func (s Scope) CanDrive() bool { return s == ScopeDriver }

// Validate reports whether p is a coherent announce payload: identity present,
// at least one usable endpoint, well-formed session rows, a usable device key,
// and a recognized scope. Errors for every failing rule are joined, and each
// names the field it is about (indexed, for slice elements), so a caller can
// fix them in one pass.
//
// It validates SHAPE, not the world. It does not parse, resolve, or reach an
// [Endpoint.Address] — an address is opaque here (see the package doc) — and it
// does not judge a [Credential], which the SDK never interprets and which is
// optional besides. It makes no claim about the payload's authenticity: a
// well-formed payload is not a trusted one.
//
// [Payload.DeviceKey] is REQUIRED and the zero key is rejected. device.PublicKey
// is a fixed-size array, so a malformed key is unrepresentable and there is no
// well-formedness left to check — but the zero key IS representable, and it
// means "no key", which is not a state a payload may be in. End-to-end
// encryption is a hard requirement of this protocol: a server with no key
// cannot be sealed to, so a keyless announce would leave a client holding a
// validated payload for a peer it can never encrypt to — a silent downgrade,
// discovered late if at all. It is rejected here instead, loudly, at the one
// point every reader already calls.
func (p Payload) Validate() error {
	var errs []error
	if p.ServerID == "" {
		errs = append(errs, errors.New("announce: server_id is required"))
	}
	if p.AccountID == "" {
		errs = append(errs, errors.New("announce: account_id is required"))
	}
	if len(p.Endpoints) == 0 {
		errs = append(errs, errors.New("announce: endpoints requires at least one candidate"))
	}
	for i, ep := range p.Endpoints {
		if ep.Kind == "" {
			errs = append(errs, fmt.Errorf("announce: endpoints[%d].kind is required", i))
		}
		if ep.Address == "" {
			errs = append(errs, fmt.Errorf("announce: endpoints[%d].address is required", i))
		}
	}
	for i, s := range p.Sessions {
		if s.ID == "" {
			errs = append(errs, fmt.Errorf("announce: sessions[%d].id is required", i))
		}
		if s.State == "" {
			errs = append(errs, fmt.Errorf("announce: sessions[%d].state is required", i))
		}
		if s.LastActive.IsZero() {
			errs = append(errs, fmt.Errorf("announce: sessions[%d].last_active is required", i))
		}
	}
	if !p.DeviceKey.Valid() {
		errs = append(errs, errors.New("announce: device_key is required"))
	}
	if !p.Scope.Known() {
		errs = append(errs, fmt.Errorf("announce: scope %q is not recognized (want %q or %q)", string(p.Scope), ScopeObserver, ScopeDriver))
	}
	return errors.Join(errs...)
}
