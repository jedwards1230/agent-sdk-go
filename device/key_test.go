package device

import (
	"encoding/json"
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
