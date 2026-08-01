package device

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
		// and is rejected where a key is required — at key agreement, and by
		// announce.Payload.Validate — not at parse. See PublicKey.Valid.
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
// encoding and MUST parse — a fixed-size array has no room for an "absent"
// state, so refusing to decode it would mean refusing a structurally valid
// message — but it is not a usable key, never reaches key agreement, and is
// not accepted anywhere a key is required.
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

// TestPublicKeyIDIsAPinHandle pins what ID is FOR: the stable short identifier
// an embedder files a pinned key under and shows to a human. That job needs it
// to be derived from the key alone (so both ends compute the same one), to
// survive an encode/decode round trip, and to carry no key material.
func TestPublicKeyIDIsAPinHandle(t *testing.T) {
	var k PublicKey
	for i := range k {
		k[i] = byte(i + 1)
	}

	round, err := ParsePublicKey(k.String())
	if err != nil {
		t.Fatalf("ParsePublicKey: %v", err)
	}
	if round.ID() != k.ID() {
		t.Errorf("ID() changed across a round trip: %q then %q", k.ID(), round.ID())
	}

	if _, err := hex.DecodeString(k.ID()); err != nil {
		t.Errorf("ID() = %q is not hex, so it is not safe to paste into a prompt: %v", k.ID(), err)
	}
	if strings.Contains(k.String(), k.ID()) || strings.Contains(k.ID(), k.String()) {
		t.Errorf("ID() = %q overlaps the encoded key %q — a fingerprint must not carry key material", k.ID(), k.String())
	}
	// A fingerprint is not a key: it must be far too short to authenticate on.
	if len(k.ID()) >= len(k.String()) {
		t.Errorf("ID() length %d is not shorter than the key encoding length %d", len(k.ID()), len(k.String()))
	}
}

// TestErrPinnedKeyMismatchIsDistinct pins the only thing the SDK owes the
// trust-on-first-use model: an embedder that detects a pin mismatch can report
// it AS a pin mismatch. It must survive wrapping through errors.Is, and it must
// not be confusable with the other device sentinels — "this is not the device
// you paired with" and "that key is malformed" call for different responses.
func TestErrPinnedKeyMismatchIsDistinct(t *testing.T) {
	pinned, presented := PublicKey{1}, PublicKey{2}
	err := fmt.Errorf("server %s: pinned %s: %w", "srv-1", pinned.ID(), ErrPinnedKeyMismatch)

	if !errors.Is(err, ErrPinnedKeyMismatch) {
		t.Errorf("errors.Is(%v, ErrPinnedKeyMismatch) = false, want true", err)
	}
	for _, other := range []struct {
		name string
		err  error
	}{
		{"ErrInvalidKey", ErrInvalidKey},
		{"ErrOpen", ErrOpen},
		{"ErrNoPrivateKey", ErrNoPrivateKey},
		{"ErrUnsupportedVersion", ErrUnsupportedVersion},
	} {
		if errors.Is(err, other.err) {
			t.Errorf("a pin mismatch matches %s — the two are indistinguishable to a caller", other.name)
		}
		if errors.Is(other.err, ErrPinnedKeyMismatch) {
			t.Errorf("%s matches ErrPinnedKeyMismatch", other.name)
		}
	}
	if !strings.Contains(err.Error(), pinned.ID()) {
		t.Errorf("wrapped error %q does not name the pinned key handle %q", err, pinned.ID())
	}
	if pinned == presented {
		t.Fatal("fixture keys are equal, so this test proves nothing")
	}
}
