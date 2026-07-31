package announce

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jedwards1230/agent-sdk-go/device"
)

// testKey is a 32-byte device public key: the ASCII bytes
// "0123456789abcdef0123456789abcdef", base64 raw-URL encoded.
const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"

// testSecret is the credential value the leak tests look for. It is
// distinctive on purpose: any output containing it came from the credential.
const testSecret = "opaque.bearer.blob"

func mustKey(t *testing.T) device.PublicKey {
	t.Helper()
	k, err := device.ParsePublicKey(testKey)
	if err != nil {
		t.Fatalf("ParsePublicKey(%q): %v", testKey, err)
	}
	return k
}

// validPayload is a fully populated, valid payload. Tests mutate a copy of it to
// exercise one rule at a time.
func validPayload(t *testing.T) Payload {
	t.Helper()
	return Payload{
		ServerID:    "0198c0d1-1b7a-7c3e-9f21-0c5f3a1b2d44",
		AccountID:   "acct-7f21",
		DisplayName: "justin's laptop",
		Endpoints: []Endpoint{
			{Kind: EndpointKindLAN, Address: "192.168.8.31:7777", Priority: 0},
			{Kind: EndpointKindTailnet, Address: "laptop-1.tail-scale.ts.net:7777", Priority: 10},
			{Kind: EndpointKindRelay, Address: "relay.example:443/abc", Priority: 100},
		},
		Sessions: []SessionSummary{
			{
				ID:         "0198c0d1-2000-7000-8000-000000000001",
				Title:      "refactor the broker",
				State:      SessionStateBusy,
				LastActive: time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC),
			},
			{
				ID:         "0198c0d1-2000-7000-8000-000000000002",
				State:      SessionStateWaiting,
				LastActive: time.Date(2026, 7, 26, 1, 5, 0, 0, time.UTC),
			},
		},
		Credential: NewCredential(testSecret),
		DeviceKey:  mustKey(t),
		Scope:      ScopeDriver,
	}
}

func TestPayloadValidate(t *testing.T) {
	tests := []struct {
		name string
		// mutate turns the valid payload into the case under test.
		mutate func(*Payload)
		// wantField is the substring the error must name; empty means the
		// payload must validate.
		wantField string
	}{
		{
			name:   "fully populated payload is valid",
			mutate: func(*Payload) {},
		},
		{
			name:      "missing server id",
			mutate:    func(p *Payload) { p.ServerID = "" },
			wantField: "server_id",
		},
		{
			name:      "missing account id",
			mutate:    func(p *Payload) { p.AccountID = "" },
			wantField: "account_id",
		},
		{
			name:      "no endpoints",
			mutate:    func(p *Payload) { p.Endpoints = nil },
			wantField: "endpoints",
		},
		{
			name:      "endpoint without a kind",
			mutate:    func(p *Payload) { p.Endpoints[1].Kind = "" },
			wantField: "endpoints[1].kind",
		},
		{
			name:      "endpoint without an address",
			mutate:    func(p *Payload) { p.Endpoints[2].Address = "" },
			wantField: "endpoints[2].address",
		},
		{
			name:      "session summary without an id",
			mutate:    func(p *Payload) { p.Sessions[0].ID = "" },
			wantField: "sessions[0].id",
		},
		{
			name:      "session summary without a state",
			mutate:    func(p *Payload) { p.Sessions[1].State = "" },
			wantField: "sessions[1].state",
		},
		{
			name:      "session summary without a last-active time",
			mutate:    func(p *Payload) { p.Sessions[0].LastActive = time.Time{} },
			wantField: "sessions[0].last_active",
		},
		{
			name:      "empty scope",
			mutate:    func(p *Payload) { p.Scope = "" },
			wantField: "scope",
		},
		{
			name:      "unrecognized scope",
			mutate:    func(p *Payload) { p.Scope = "admin" },
			wantField: "scope",
		},
		{
			name:      "absent device key is rejected",
			mutate:    func(p *Payload) { p.DeviceKey = device.PublicKey{} },
			wantField: "device_key",
		},
		{
			name:   "absent credential is accepted",
			mutate: func(p *Payload) { p.Credential = Credential{} },
		},
		{
			name:   "no sessions is accepted",
			mutate: func(p *Payload) { p.Sessions = nil },
		},
		{
			name:   "absent display name is accepted",
			mutate: func(p *Payload) { p.DisplayName = "" },
		},
		{
			name:   "observer scope is accepted",
			mutate: func(p *Payload) { p.Scope = ScopeObserver },
		},
		{
			name:   "unknown endpoint kind is accepted",
			mutate: func(p *Payload) { p.Endpoints[0].Kind = "quantum-tunnel" },
		},
		{
			name:   "unknown session state is accepted",
			mutate: func(p *Payload) { p.Sessions[0].State = "hibernating" },
		},
		{
			name:      "a malformed address is not rejected but a missing one is",
			mutate:    func(p *Payload) { p.Endpoints[0].Address = "" },
			wantField: "endpoints[0].address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPayload(t)
			tt.mutate(&p)

			err := p.Validate()
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error naming %q", tt.wantField)
			}
			if !strings.Contains(err.Error(), tt.wantField) {
				t.Errorf("Validate() = %q, want it to name the field %q", err, tt.wantField)
			}
		})
	}
}

func TestValidateJoinsEveryFailingRule(t *testing.T) {
	// An empty payload trips the identity, endpoint, device-key, and scope
	// rules at once; a caller should see all of them, not just the first.
	err := Payload{}.Validate()
	if err == nil {
		t.Fatal("Validate() on the zero payload = nil, want an error")
	}
	for _, field := range []string{"server_id", "account_id", "endpoints", "device_key", "scope"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("Validate() = %q, want it to name %q", err, field)
		}
	}
}

// TestValidateIgnoresAddressSyntax pins the deliberate non-behavior: an address
// is opaque, so nonsense that a dialer would reject still validates here.
func TestValidateIgnoresAddressSyntax(t *testing.T) {
	for _, addr := range []string{"not a host", "::::", "relay-ticket:AAAA", "999.999.999.999:0"} {
		p := validPayload(t)
		p.Endpoints = []Endpoint{{Kind: EndpointKindLAN, Address: addr}}
		if err := p.Validate(); err != nil {
			t.Errorf("Validate() with address %q = %v, want nil (addresses are opaque)", addr, err)
		}
	}
}

// codec is one of the two encodings the payload must round-trip through.
type codec struct {
	name      string
	marshal   func(any) ([]byte, error)
	unmarshal func([]byte, any) error
}

func codecs() []codec {
	return []codec{
		{"json", json.Marshal, json.Unmarshal},
		{"yaml", yaml.Marshal, func(b []byte, v any) error { return yaml.Unmarshal(b, v) }},
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	minimal := validPayload(t)
	minimal.Credential = Credential{}
	minimal.Sessions = nil
	minimal.DisplayName = ""

	// The zero device key no longer validates (see Payload.Validate), but it
	// must still ROUND-TRIP: decoding is not validation, and a peer that sends
	// a keyless payload has to decode into something a reader can reject
	// rather than into a decode error.
	keyless := validPayload(t)
	keyless.DeviceKey = device.PublicKey{}

	payloads := []struct {
		name string
		p    Payload
		// validates says whether the decoded payload must pass Validate.
		validates bool
	}{
		{"fully populated", validPayload(t), true},
		{"empty optional fields", minimal, true},
		{"zero device key", keyless, false},
	}

	for _, c := range codecs() {
		for _, tc := range payloads {
			t.Run(c.name+"/"+tc.name, func(t *testing.T) {
				data, err := c.marshal(tc.p)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				var got Payload
				if err := c.unmarshal(data, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if !reflect.DeepEqual(got, tc.p) {
					t.Errorf("round trip mismatch:\n got %+v\nwant %+v\nwire: %s", got, tc.p, data)
				}
				if got.DeviceKey != tc.p.DeviceKey {
					t.Errorf("device key = %q, want %q", got.DeviceKey, tc.p.DeviceKey)
				}
				if got.DeviceKey.Valid() != tc.p.DeviceKey.Valid() {
					t.Errorf("device key presence changed: got Valid()=%v, want %v", got.DeviceKey.Valid(), tc.p.DeviceKey.Valid())
				}
				if err := got.Validate(); (err == nil) != tc.validates {
					t.Errorf("decoded payload Validate() = %v, want validates=%v", err, tc.validates)
				}
				if got.Credential.Reveal() != tc.p.Credential.Reveal() {
					t.Error("credential value did not survive the round trip")
				}
			})
		}
	}
}

// TestRoundTripPreservesEndpointOrderAndPriority pins the load-bearing detail:
// endpoints are an ordered list of candidates, and neither the order nor the
// priorities may be reshuffled by encoding.
func TestRoundTripPreservesEndpointOrderAndPriority(t *testing.T) {
	p := validPayload(t)
	p.Endpoints = []Endpoint{
		{Kind: EndpointKindRelay, Address: "relay.example:443/abc", Priority: 100},
		{Kind: EndpointKindLAN, Address: "192.168.8.31:7777", Priority: 0},
		{Kind: EndpointKindTailnet, Address: "laptop-1.ts.net:7777", Priority: 10},
		{Kind: EndpointKindLAN, Address: "[fe80::1]:7777", Priority: 0},
	}

	for _, c := range codecs() {
		t.Run(c.name, func(t *testing.T) {
			data, err := c.marshal(p)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got Payload
			if err := c.unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got.Endpoints, p.Endpoints) {
				t.Fatalf("endpoints changed:\n got %+v\nwant %+v", got.Endpoints, p.Endpoints)
			}
			for i := range got.Endpoints {
				if got.Endpoints[i].Kind != p.Endpoints[i].Kind {
					t.Errorf("endpoint %d kind = %q, want %q (order not preserved)", i, got.Endpoints[i].Kind, p.Endpoints[i].Kind)
				}
				if got.Endpoints[i].Priority != p.Endpoints[i].Priority {
					t.Errorf("endpoint %d priority = %d, want %d", i, got.Endpoints[i].Priority, p.Endpoints[i].Priority)
				}
			}
		})
	}
}

// TestUnknownValuesSurviveRoundTrip is the forward-compatibility guarantee: a
// payload minted by a newer peer, carrying an endpoint kind and a session state
// this build has never heard of, decodes intact instead of erroring or dropping
// the field.
func TestUnknownValuesSurviveRoundTrip(t *testing.T) {
	const (
		futureKind  EndpointKind = "quantum-tunnel"
		futureState SessionState = "hibernating"
	)

	for _, c := range codecs() {
		t.Run(c.name, func(t *testing.T) {
			p := validPayload(t)
			p.Endpoints = append(p.Endpoints, Endpoint{Kind: futureKind, Address: "q://somewhere", Priority: 5})
			p.Sessions[0].State = futureState

			data, err := c.marshal(p)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got Payload
			if err := c.unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("Validate() with unknown-but-well-formed values = %v, want nil", err)
			}
			if n := len(got.Endpoints); n != len(p.Endpoints) {
				t.Fatalf("endpoint count = %d, want %d (an unknown kind was dropped)", n, len(p.Endpoints))
			}
			last := got.Endpoints[len(got.Endpoints)-1]
			if last.Kind != futureKind {
				t.Errorf("unknown endpoint kind = %q, want %q", last.Kind, futureKind)
			}
			if got.Sessions[0].State != futureState {
				t.Errorf("unknown session state = %q, want %q", got.Sessions[0].State, futureState)
			}
		})
	}
}

func TestScope(t *testing.T) {
	tests := []struct {
		name      string
		scope     Scope
		wantKnown bool
		wantDrive bool
	}{
		{name: "zero value is neither known nor a driver", scope: Scope("")},
		{name: "observer may watch but not drive", scope: ScopeObserver, wantKnown: true},
		{name: "driver may drive", scope: ScopeDriver, wantKnown: true, wantDrive: true},
		{name: "unknown scope may not drive", scope: Scope("admin")},
		{name: "near-miss spelling may not drive", scope: Scope("Driver")},
		{name: "unknown future scope may not drive", scope: Scope("maintainer")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scope.Known(); got != tt.wantKnown {
				t.Errorf("Scope(%q).Known() = %v, want %v", string(tt.scope), got, tt.wantKnown)
			}
			if got := tt.scope.CanDrive(); got != tt.wantDrive {
				t.Errorf("Scope(%q).CanDrive() = %v, want %v", string(tt.scope), got, tt.wantDrive)
			}
		})
	}
}

// TestZeroPayloadScopeIsNotDriver pins the zero-value story end to end: a
// payload nobody set a scope on grants nothing and does not validate.
func TestZeroPayloadScopeIsNotDriver(t *testing.T) {
	var p Payload
	if p.Scope.CanDrive() {
		t.Error("the zero payload's scope reports CanDrive() = true, want false")
	}
	if err := p.Validate(); err == nil {
		t.Error("Validate() on the zero payload = nil, want a rejected scope")
	}
}

// TestScopeMissingFromWireIsNotDriver covers the same story across the wire: a
// peer that omits the scope field entirely must not be read as a driver.
func TestScopeMissingFromWireIsNotDriver(t *testing.T) {
	wire := map[string]struct {
		data []byte
		fn   func([]byte, any) error
	}{
		"json": {[]byte(`{"server_id":"s","account_id":"a","endpoints":[{"kind":"lan","address":"h:1"}]}`), json.Unmarshal},
		"yaml": {[]byte("server_id: s\naccount_id: a\nendpoints:\n  - kind: lan\n    address: h:1\n"), func(b []byte, v any) error { return yaml.Unmarshal(b, v) }},
	}

	for name, w := range wire {
		t.Run(name, func(t *testing.T) {
			var p Payload
			if err := w.fn(w.data, &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if p.Scope.CanDrive() {
				t.Error("a payload with no scope field reports CanDrive() = true, want false")
			}
			if err := p.Validate(); err == nil {
				t.Error("Validate() = nil, want the missing scope rejected")
			}
		})
	}
}

// minimalPayload is the smallest valid payload: every optional field left
// unset, so the emitted bytes show exactly which fields survive omission.
func minimalPayload(t *testing.T) Payload {
	t.Helper()
	return Payload{
		ServerID:  "s",
		AccountID: "a",
		Endpoints: []Endpoint{{Kind: EndpointKindLAN, Address: "h:1"}},
		DeviceKey: mustKey(t),
		Scope:     ScopeObserver,
	}
}

// TestPayloadOmitsEmptyCredential pins the EMITTED BYTES for both codecs. The
// tags differ (json omitzero, yaml omitempty) because the two packages disagree
// about zero structs, and nothing but the wire output proves the pair is right:
// json's omitempty does not omit a zero struct, and yaml.v3 panics on a tag it
// does not recognize. An empty credential must be absent from the wire exactly
// as it was when Credential was a bare string.
func TestPayloadOmitsEmptyCredential(t *testing.T) {
	const (
		wantJSONEmpty = `{"server_id":"s","account_id":"a","endpoints":[{"kind":"lan","address":"h:1","priority":0}],"device_key":"` + testKey + `","scope":"observer"}`
		wantJSONSet   = `{"server_id":"s","account_id":"a","endpoints":[{"kind":"lan","address":"h:1","priority":0}],"credential":"` + testSecret + `","device_key":"` + testKey + `","scope":"observer"}`

		wantYAMLEmpty = "server_id: s\n" +
			"account_id: a\n" +
			"endpoints:\n" +
			"    - kind: lan\n" +
			"      address: h:1\n" +
			"      priority: 0\n" +
			"device_key: " + testKey + "\n" +
			"scope: observer\n"
		wantYAMLSet = "server_id: s\n" +
			"account_id: a\n" +
			"endpoints:\n" +
			"    - kind: lan\n" +
			"      address: h:1\n" +
			"      priority: 0\n" +
			"credential: " + testSecret + "\n" +
			"device_key: " + testKey + "\n" +
			"scope: observer\n"
	)

	tests := []struct {
		name       string
		credential Credential
		wantJSON   string
		wantYAML   string
	}{
		{"empty credential is omitted", Credential{}, wantJSONEmpty, wantYAMLEmpty},
		{"set credential rides the wire verbatim", NewCredential(testSecret), wantJSONSet, wantYAMLSet},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := minimalPayload(t)
			p.Credential = tt.credential

			gotJSON, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(gotJSON) != tt.wantJSON {
				t.Errorf("json bytes:\n got %s\nwant %s", gotJSON, tt.wantJSON)
			}

			gotYAML, err := yaml.Marshal(p)
			if err != nil {
				t.Fatalf("yaml.Marshal: %v", err)
			}
			if string(gotYAML) != tt.wantYAML {
				t.Errorf("yaml bytes:\n got %q\nwant %q", gotYAML, tt.wantYAML)
			}
		})
	}
}

// TestCredentialDoesNotLeakThroughFormatting is the real gate on [Credential].
// A Payload is exactly the sort of value that gets printed while debugging, so
// the secret must survive none of it: not %v on the payload, not %+v, not %s,
// not %#v, and not a verb the type was never meant for.
//
// %d is in the list on purpose. fmt only consults a Stringer for %v, %s, %q, %x
// and %X; every other verb falls through to reflection, which happily prints an
// unexported field. Before Credential.Format existed, %d on a struct holding a
// credential printed `{{%!d(string=s3cr3t)}}` — the value, in clear.
func TestCredentialDoesNotLeakThroughFormatting(t *testing.T) {
	p := validPayload(t)
	if p.Credential.Reveal() != testSecret {
		t.Fatalf("fixture credential = %q, want %q — this test proves nothing otherwise", p.Credential.Reveal(), testSecret)
	}

	verbs := []struct {
		name string
		verb string
	}{
		{"default", "%v"},
		{"field names", "%+v"},
		{"string", "%s"},
		{"go syntax", "%#v"},
		{"quoted", "%q"},
		{"hex", "%x"},
		{"upper hex", "%X"},
		{"decimal", "%d"},
		{"float", "%f"},
		{"bool", "%t"},
	}

	for _, v := range verbs {
		t.Run(v.name, func(t *testing.T) {
			subjects := []struct {
				what string
				val  any
			}{
				{"payload", p},
				{"payload pointer", &p},
				{"credential", p.Credential},
				{"credential pointer", &p.Credential},
				{"payload slice", []Payload{p}},
				{"payload map", map[string]Payload{"k": p}},
			}
			for _, s := range subjects {
				got := fmt.Sprintf(v.verb, s.val)
				if strings.Contains(got, testSecret) {
					t.Errorf("fmt.Sprintf(%q, %s) leaked the credential: %s", v.verb, s.what, got)
				}
			}
		})
	}

	// The convenience wrappers that do not name a verb route through %v, and
	// are what a careless log line actually calls.
	for _, got := range []string{fmt.Sprint(p), fmt.Sprintln(p), fmt.Sprint(p.Credential)} {
		if strings.Contains(got, testSecret) {
			t.Errorf("fmt.Sprint-family leaked the credential: %s", got)
		}
	}
}

// TestCredentialRedactedForms pins the redactions themselves, so a String that
// started returning the value would fail here as well as in the formatting
// test.
func TestCredentialRedactedForms(t *testing.T) {
	set := NewCredential(testSecret)
	if got := set.String(); got == testSecret {
		t.Fatalf("String() = %q, want a redaction — it must never return the value", got)
	}
	if got, want := set.String(), "[redacted]"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if got := set.GoString(); strings.Contains(got, testSecret) {
		t.Errorf("GoString() = %q, want a redaction", got)
	}

	var zero Credential
	if got, want := zero.String(), ""; got != want {
		t.Errorf("zero String() = %q, want %q", got, want)
	}
	if got, want := zero.GoString(), "announce.Credential{}"; got != want {
		t.Errorf("zero GoString() = %q, want %q", got, want)
	}
}

// TestCredentialRevealIsTheOnlyReader pins the wrapper's contract: the value
// goes in through NewCredential and comes back out through Reveal, unchanged.
func TestCredentialRevealIsTheOnlyReader(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantZero   bool
		wantReveal string
	}{
		{name: "empty string is the zero credential", in: "", wantZero: true},
		{name: "value survives verbatim", in: testSecret, wantReveal: testSecret},
		{name: "whitespace is not trimmed", in: "  padded  ", wantReveal: "  padded  "},
		{name: "a literal redaction is still just a value", in: "[redacted]", wantReveal: "[redacted]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCredential(tt.in)
			if got := c.IsZero(); got != tt.wantZero {
				t.Errorf("IsZero() = %v, want %v", got, tt.wantZero)
			}
			if got := c.Reveal(); got != tt.in {
				t.Errorf("Reveal() = %q, want %q", got, tt.in)
			}
		})
	}
	if !(Credential{}).IsZero() {
		t.Error("the zero Credential reports IsZero() = false")
	}
}

// TestCredentialCodecsCarryTheRealValue is the deliberate other half of the
// redaction story: printing must not reveal the credential, but encoding MUST —
// the credential's whole job is to ride the wire back to the server.
func TestCredentialCodecsCarryTheRealValue(t *testing.T) {
	for _, c := range codecs() {
		t.Run(c.name, func(t *testing.T) {
			p := validPayload(t)
			data, err := c.marshal(p)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(data), testSecret) {
				t.Fatalf("%s wire form does not carry the credential: %s", c.name, data)
			}
			var got Payload
			if err := c.unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Credential.Reveal() != testSecret {
				t.Errorf("decoded credential = %q, want %q", got.Credential.Reveal(), testSecret)
			}
		})
	}
}

// TestCredentialDecodesNullAndAbsent covers the two ways a peer says "no
// credential": omitting the field, and sending an explicit null.
func TestCredentialDecodesNullAndAbsent(t *testing.T) {
	wire := []struct {
		name string
		data []byte
		fn   func([]byte, any) error
	}{
		{"json absent", []byte(`{"server_id":"s"}`), json.Unmarshal},
		{"json null", []byte(`{"server_id":"s","credential":null}`), json.Unmarshal},
		{"yaml absent", []byte("server_id: s\n"), func(b []byte, v any) error { return yaml.Unmarshal(b, v) }},
		{"yaml null", []byte("server_id: s\ncredential: null\n"), func(b []byte, v any) error { return yaml.Unmarshal(b, v) }},
	}
	for _, w := range wire {
		t.Run(w.name, func(t *testing.T) {
			var p Payload
			if err := w.fn(w.data, &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !p.Credential.IsZero() {
				t.Errorf("credential = %q, want the zero credential", p.Credential.Reveal())
			}
		})
	}
}
