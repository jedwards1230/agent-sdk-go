package device

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGenerate(t *testing.T) {
	a, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate(nil): %v", err)
	}
	if !a.Public.Valid() {
		t.Fatal("generated public key is invalid")
	}
	if a.PrivateBytes() == ([KeySize]byte{}) {
		t.Fatal("generated private key is all zero")
	}

	b, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate(nil): %v", err)
	}
	if a.Public == b.Public {
		t.Error("two generated keypairs share a public key")
	}
}

func TestGenerateFailingReader(t *testing.T) {
	tests := []struct {
		name string
		rand *stubReader
	}{
		{name: "error", rand: &stubReader{err: errors.New("no entropy")}},
		{name: "short read", rand: &stubReader{data: []byte{1, 2, 3}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Generate(tt.rand); err == nil {
				t.Fatal("Generate did not fail on a broken reader")
			}
		})
	}
}

func TestKeyPairFromPrivate(t *testing.T) {
	gen, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	restored, err := KeyPairFromPrivate(gen.PrivateBytes())
	if err != nil {
		t.Fatalf("KeyPairFromPrivate: %v", err)
	}
	if restored.Public != gen.Public {
		t.Errorf("restored public = %v, want %v", restored.Public, gen.Public)
	}
	if restored.PrivateBytes() != gen.PrivateBytes() {
		t.Error("restored private key differs from the stored one")
	}

	// A restored keypair opens what the original was sent.
	peer, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sealed, err := Seal(nil, gen.Public, peer, []byte("after restart"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := Open(restored, sealed)
	if err != nil {
		t.Fatalf("Open with restored keypair: %v", err)
	}
	if string(got) != "after restart" {
		t.Errorf("Open = %q, want %q", got, "after restart")
	}
}

func TestKeyPairFromPrivateRejectsZero(t *testing.T) {
	kp, err := KeyPairFromPrivate([KeySize]byte{})
	if err == nil {
		t.Fatal("KeyPairFromPrivate accepted an all-zero private key")
	}
	if kp != nil {
		t.Error("KeyPairFromPrivate returned a keypair alongside an error")
	}
	if !errors.Is(err, ErrInvalidKey) {
		t.Errorf("err = %v, want ErrInvalidKey", err)
	}
}

func TestKeyPairMarshalJSONOmitsPrivate(t *testing.T) {
	kp, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	priv := kp.PrivateBytes()

	for _, tt := range []struct {
		name string
		in   any
	}{
		{name: "value", in: *kp},
		{name: "pointer", in: kp},
		{name: "nested", in: struct {
			Device *KeyPair `json:"device"`
		}{Device: kp}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if bytes.Contains(b, priv[:]) {
				t.Fatal("marshalled output contains the raw private key bytes")
			}
			for _, enc := range []string{
				base64.RawURLEncoding.EncodeToString(priv[:]),
				base64.StdEncoding.EncodeToString(priv[:]),
				base64.RawStdEncoding.EncodeToString(priv[:]),
			} {
				if strings.Contains(string(b), enc) {
					t.Fatalf("marshalled output contains the encoded private key: %s", b)
				}
			}
			if !strings.Contains(string(b), kp.Public.String()) {
				t.Errorf("marshalled output is missing the public key: %s", b)
			}
		})
	}
}

func TestKeyPairMarshalYAMLOmitsPrivate(t *testing.T) {
	kp, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := yaml.Marshal(kp)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	var fields map[string]any
	if err := yaml.Unmarshal(b, &fields); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if _, ok := fields["public"]; !ok || len(fields) != 1 {
		t.Errorf("yaml fields = %v, want only the public key", fields)
	}
}

func TestKeyPairUnmarshalJSONRejected(t *testing.T) {
	kp, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := json.Marshal(kp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got KeyPair
	if err := json.Unmarshal(b, &got); !errors.Is(err, ErrNoPrivateKey) {
		t.Fatalf("Unmarshal err = %v, want ErrNoPrivateKey", err)
	}
	if got.Public.Valid() {
		t.Error("a rejected unmarshal still populated the keypair")
	}
}
