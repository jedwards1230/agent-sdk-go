package device

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParsePublicKey(t *testing.T) {
	var full PublicKey
	for i := range full {
		full[i] = byte(i + 1)
	}
	tests := []struct {
		name    string
		in      string
		want    PublicKey
		wantErr bool
	}{
		{name: "round trip", in: full.String(), want: full},
		// The zero key is well-formed and parses. It reports Valid() == false
		// and is rejected at key agreement, not at parse — see PublicKey.Valid.
		// TestParsePublicKeyZeroKeyIsParseableButInvalid pins that distinction.
		{name: "zero key parses", in: PublicKey{}.String(), want: PublicKey{}},
		{name: "not base64", in: "!!!!", wantErr: true},
		{name: "too short", in: "AAAA", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePublicKey(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePublicKey(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("ParsePublicKey(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestPublicKeyValid(t *testing.T) {
	if (PublicKey{}).Valid() {
		t.Error("zero key reported valid")
	}
	k := PublicKey{1}
	if !k.Valid() {
		t.Error("non-zero key reported invalid")
	}
}

func TestPublicKeyID(t *testing.T) {
	a, b := PublicKey{1}, PublicKey{2}
	if a.ID() == b.ID() {
		t.Errorf("distinct keys share fingerprint %q", a.ID())
	}
	if got, want := len(a.ID()), 16; got != want {
		t.Errorf("ID() length = %d, want %d", got, want)
	}
	if a.ID() != (PublicKey{1}).ID() {
		t.Error("ID() is not stable for equal keys")
	}
}

func TestPublicKeyJSON(t *testing.T) {
	var key PublicKey
	for i := range key {
		key[i] = byte(255 - i)
	}
	type holder struct {
		Key PublicKey `json:"key"`
	}
	b, err := json.Marshal(holder{Key: key})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got holder
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Key != key {
		t.Errorf("round trip = %v, want %v", got.Key, key)
	}
}

func TestPublicKeyUnmarshalTextEmpty(t *testing.T) {
	k := PublicKey{1}
	if err := k.UnmarshalText(nil); err != nil {
		t.Fatalf("UnmarshalText(nil): %v", err)
	}
	if k.Valid() {
		t.Error("empty text did not decode to the zero key")
	}
}

// TestParsePublicKeyZeroKeyIsParseableButInvalid pins the parse-vs-validity
// split that PublicKey.Valid documents: the all-zero key is a well-formed
// encoding and MUST parse (the announce payload uses it to mean "no key
// advertised"), but it is not a usable key and must never reach key agreement.
func TestParsePublicKeyZeroKeyIsParseableButInvalid(t *testing.T) {
	k, err := ParsePublicKey(PublicKey{}.String())
	if err != nil {
		t.Fatalf("ParsePublicKey(zero) err = %v, want nil — the zero key must parse", err)
	}
	if k != (PublicKey{}) {
		t.Errorf("ParsePublicKey(zero) = %v, want the zero key", k)
	}
	if k.Valid() {
		t.Error("zero key reports Valid() = true, want false")
	}
}

// TestParsePublicKeyErrorsAreErrInvalidKey pins that malformed input is
// reportable with errors.Is rather than only by string, so a caller can
// distinguish a bad key from any other failure.
func TestParsePublicKeyErrorsAreErrInvalidKey(t *testing.T) {
	for _, in := range []string{"!!!!", "AAAA", ""} {
		if _, err := ParsePublicKey(in); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("ParsePublicKey(%q) err = %v, want errors.Is(err, ErrInvalidKey)", in, err)
		}
	}
}
