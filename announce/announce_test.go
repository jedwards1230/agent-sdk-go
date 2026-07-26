package announce

import (
	"encoding/json"
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
		Credential: Credential("opaque.bearer.blob"),
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
			name:   "absent device key is accepted",
			mutate: func(p *Payload) { p.DeviceKey = device.PublicKey{} },
		},
		{
			name:   "absent credential is accepted",
			mutate: func(p *Payload) { p.Credential = "" },
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
	// An empty payload trips the identity, endpoint, and scope rules at once;
	// a caller should see all of them, not just the first.
	err := Payload{}.Validate()
	if err == nil {
		t.Fatal("Validate() on the zero payload = nil, want an error")
	}
	for _, field := range []string{"server_id", "account_id", "endpoints", "scope"} {
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
	empty := validPayload(t)
	empty.DeviceKey = device.PublicKey{}
	empty.Credential = ""
	empty.Sessions = nil
	empty.DisplayName = ""

	payloads := []struct {
		name string
		p    Payload
	}{
		{"fully populated", validPayload(t)},
		{"empty key and empty optional fields", empty},
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
				if err := got.Validate(); err != nil {
					t.Errorf("decoded payload does not validate: %v", err)
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
