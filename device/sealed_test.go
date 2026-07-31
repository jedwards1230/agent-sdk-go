package device

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

var (
	errStubExhausted = errors.New("stub reader exhausted")
	errNoEntropy     = errors.New("no entropy")
)

// stubReader is a deterministic io.Reader for the randomness argument: it hands
// out data and then reports err (io.EOF when unset), so tests can exercise a
// failing or short-reading CSPRNG.
type stubReader struct {
	data []byte
	err  error
}

func (r *stubReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		return 0, errStubExhausted
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// pair generates a keypair or fails the test.
func pair(t *testing.T) *KeyPair {
	t.Helper()
	kp, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return kp
}

func TestSealOpenRoundTrip(t *testing.T) {
	sender, recipient := pair(t), pair(t)
	tests := []struct {
		name      string
		plaintext []byte
	}{
		{name: "text", plaintext: []byte("session content: export API_KEY=hunter2")},
		{name: "empty", plaintext: []byte{}},
		{name: "nil", plaintext: nil},
		{name: "binary", plaintext: bytes.Repeat([]byte{0x00, 0xff, 0x7f}, 500)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sealed, err := Seal(nil, recipient.Public, sender, tt.plaintext)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if sealed.Version != SealedVersion {
				t.Errorf("Sealed.Version = %d, want %d", sealed.Version, SealedVersion)
			}
			if sealed.Sender != sender.Public {
				t.Errorf("Sealed.Sender = %v, want %v", sealed.Sender, sender.Public)
			}
			if sealed.Ephemeral == ([KeySize]byte{}) {
				t.Error("Sealed.Ephemeral is all zero")
			}
			if sealed.Ephemeral == [KeySize]byte(sender.Public) {
				t.Error("Sealed.Ephemeral is the sender's identity key, not a per-envelope ephemeral")
			}
			if bytes.Contains(sealed.Ciphertext, tt.plaintext) && len(tt.plaintext) > 0 {
				t.Error("ciphertext contains the plaintext")
			}
			got, err := Open(recipient, sealed)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(got, tt.plaintext) {
				t.Errorf("Open = %q, want %q", got, tt.plaintext)
			}
		})
	}
}

// TestSealFreshEphemeralPerMessage covers the property the whole construction
// rests on: the KEM contribution is fresh per envelope and is never the
// sender's long-term identity key.
func TestSealFreshEphemeralPerMessage(t *testing.T) {
	sender, recipient := pair(t), pair(t)
	plaintext := []byte("identical plaintext")

	first, err := Seal(nil, recipient.Public, sender, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	second, err := Seal(nil, recipient.Public, sender, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if first.Ephemeral == second.Ephemeral {
		t.Error("two seals reused an ephemeral key")
	}
	if bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Error("two seals of the same plaintext produced identical ciphertext")
	}
	for i, s := range []Sealed{first, second} {
		if s.Ephemeral == ([KeySize]byte{}) {
			t.Errorf("seal %d: ephemeral is all zero", i)
		}
		if s.Ephemeral == [KeySize]byte(sender.Public) {
			t.Errorf("seal %d: ephemeral equals the sender's identity key", i)
		}
		if s.Ephemeral == [KeySize]byte(recipient.Public) {
			t.Errorf("seal %d: ephemeral equals the recipient's identity key", i)
		}
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	sender, recipient, other := pair(t), pair(t), pair(t)
	plaintext := []byte("do not tamper")

	// spare is a second, independently sealed envelope: its Ephemeral is a
	// well-formed X25519 point, so substituting it tests the KEM binding rather
	// than input validation.
	spare, err := Seal(nil, recipient.Public, sender, plaintext)
	if err != nil {
		t.Fatalf("Seal spare: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(s *Sealed)
		openAs  func() *KeyPair
		wantErr error
	}{
		{
			name:    "tampered ciphertext",
			mutate:  func(s *Sealed) { s.Ciphertext[0] ^= 0x01 },
			wantErr: ErrOpen,
		},
		{
			name:    "truncated ciphertext",
			mutate:  func(s *Sealed) { s.Ciphertext = s.Ciphertext[:len(s.Ciphertext)-1] },
			wantErr: ErrOpen,
		},
		{
			name:    "substituted ephemeral",
			mutate:  func(s *Sealed) { s.Ephemeral = spare.Ephemeral },
			wantErr: ErrOpen,
		},
		{
			name:    "tampered ephemeral",
			mutate:  func(s *Sealed) { s.Ephemeral[0] ^= 0x01 },
			wantErr: ErrOpen,
		},
		{
			name:    "zero ephemeral",
			mutate:  func(s *Sealed) { s.Ephemeral = [KeySize]byte{} },
			wantErr: ErrOpen,
		},
		{
			name:    "substituted sender key",
			mutate:  func(s *Sealed) { s.Sender = other.Public },
			wantErr: ErrOpen,
		},
		{
			name:    "zero sender key",
			mutate:  func(s *Sealed) { s.Sender = PublicKey{} },
			wantErr: ErrInvalidKey,
		},
		{
			name:    "version zero",
			mutate:  func(s *Sealed) { s.Version = 0 },
			wantErr: ErrUnsupportedVersion,
		},
		{
			name:    "version two",
			mutate:  func(s *Sealed) { s.Version = SealedVersion + 1 },
			wantErr: ErrUnsupportedVersion,
		},
		{
			name:    "wrong recipient keypair",
			mutate:  func(*Sealed) {},
			openAs:  func() *KeyPair { return other },
			wantErr: ErrOpen,
		},
		{
			name:    "nil recipient keypair",
			mutate:  func(*Sealed) {},
			openAs:  func() *KeyPair { return nil },
			wantErr: ErrInvalidKey,
		},
		{
			name:    "recipient without a private key",
			mutate:  func(*Sealed) {},
			openAs:  func() *KeyPair { return &KeyPair{Public: recipient.Public} },
			wantErr: ErrInvalidKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sealed, err := Seal(nil, recipient.Public, sender, plaintext)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			tt.mutate(&sealed)
			opener := recipient
			if tt.openAs != nil {
				opener = tt.openAs()
			}
			got, err := Open(opener, sealed)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Open err = %v, want %v", err, tt.wantErr)
			}
			if got != nil {
				t.Errorf("Open returned plaintext %q alongside an error", got)
			}
		})
	}
}

func TestSealRejectsBadInput(t *testing.T) {
	sender, recipient := pair(t), pair(t)
	tests := []struct {
		name      string
		rand      io.Reader // nil means crypto/rand
		recipient PublicKey
		sender    *KeyPair
		wantErr   error
	}{
		{
			name:      "zero recipient",
			recipient: PublicKey{},
			sender:    sender,
			wantErr:   ErrInvalidKey,
		},
		{
			name:      "nil sender",
			recipient: recipient.Public,
			sender:    nil,
			wantErr:   ErrInvalidKey,
		},
		{
			name:      "zero sender",
			recipient: recipient.Public,
			sender:    &KeyPair{},
			wantErr:   ErrInvalidKey,
		},
		{
			name:      "sender without a private key",
			recipient: recipient.Public,
			sender:    &KeyPair{Public: sender.Public},
			wantErr:   ErrInvalidKey,
		},
		{
			name:      "failing rand",
			rand:      &stubReader{err: errNoEntropy},
			recipient: recipient.Public,
			sender:    sender,
			wantErr:   errNoEntropy,
		},
		{
			name:      "short rand",
			rand:      &stubReader{data: bytes.Repeat([]byte{0xab}, KeySize-1)},
			recipient: recipient.Public,
			sender:    sender,
			wantErr:   errStubExhausted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sealed, err := Seal(tt.rand, tt.recipient, tt.sender, []byte("payload"))
			if err == nil {
				t.Fatal("Seal accepted invalid input")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if sealed.Ciphertext != nil || sealed.Ephemeral != ([KeySize]byte{}) || sealed.Version != 0 {
				t.Error("Seal returned envelope material alongside an error")
			}
		})
	}
}

func TestSealedJSONRoundTrip(t *testing.T) {
	sender, recipient := pair(t), pair(t)
	sealed, err := Seal(nil, recipient.Public, sender, []byte("over the relay"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	b, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// The wire form is the relay's contract, so pin its shape: a version
	// integer, the sender, the encapsulated key, the ciphertext — and no nonce,
	// which HPKE derives rather than transmits.
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal into map: %v", err)
	}
	if got, want := len(raw), 4; got != want {
		t.Errorf("wire fields = %v, want exactly %d", raw, want)
	}
	if v, ok := raw["v"].(float64); !ok || int(v) != SealedVersion {
		t.Errorf(`wire "v" = %v, want %d`, raw["v"], SealedVersion)
	}
	for _, key := range []string{"sender", "enc", "ciphertext"} {
		if _, ok := raw[key].(string); !ok {
			t.Errorf("wire %q = %v, want a string", key, raw[key])
		}
	}
	if _, ok := raw["nonce"]; ok {
		t.Error(`wire carries a "nonce"; HPKE derives the AEAD nonce from the key schedule`)
	}

	var got Sealed
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Version != sealed.Version {
		t.Errorf("Version = %d, want %d", got.Version, sealed.Version)
	}
	if got.Sender != sealed.Sender {
		t.Errorf("Sender = %v, want %v", got.Sender, sealed.Sender)
	}
	if got.Ephemeral != sealed.Ephemeral {
		t.Errorf("Ephemeral = %x, want %x", got.Ephemeral, sealed.Ephemeral)
	}
	if !bytes.Equal(got.Ciphertext, sealed.Ciphertext) {
		t.Errorf("Ciphertext = %x, want %x", got.Ciphertext, sealed.Ciphertext)
	}

	plaintext, err := Open(recipient, got)
	if err != nil {
		t.Fatalf("Open after JSON round trip: %v", err)
	}
	if string(plaintext) != "over the relay" {
		t.Errorf("Open = %q, want %q", plaintext, "over the relay")
	}
}

func TestSealedUnmarshalJSONRejectsMalformed(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, KeySize))
	short := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, KeySize-1))
	long := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, KeySize+1))
	sender := pair(t).Public

	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{name: "short enc", in: fmt.Sprintf(`{"v":1,"sender":%q,"enc":%q,"ciphertext":"AAAA"}`, sender, short)},
		{name: "long enc", in: fmt.Sprintf(`{"v":1,"sender":%q,"enc":%q,"ciphertext":"AAAA"}`, sender, long)},
		{name: "empty enc", in: fmt.Sprintf(`{"v":1,"sender":%q,"enc":"","ciphertext":"AAAA"}`, sender)},
		{name: "enc not base64", in: fmt.Sprintf(`{"v":1,"sender":%q,"enc":"!!!!","ciphertext":"AAAA"}`, sender)},
		{name: "ciphertext not base64", in: fmt.Sprintf(`{"v":1,"sender":%q,"enc":%q,"ciphertext":"!!!!"}`, sender, valid)},
		{name: "sender not base64", in: fmt.Sprintf(`{"v":1,"sender":"!!!!","enc":%q,"ciphertext":"AAAA"}`, valid)},
		{name: "not an object", in: `["nope"]`},
		{
			name:    "missing version",
			in:      fmt.Sprintf(`{"sender":%q,"enc":%q,"ciphertext":"AAAA"}`, sender, valid),
			wantErr: ErrUnsupportedVersion,
		},
		{
			name:    "version zero",
			in:      fmt.Sprintf(`{"v":0,"sender":%q,"enc":%q,"ciphertext":"AAAA"}`, sender, valid),
			wantErr: ErrUnsupportedVersion,
		},
		{
			name:    "version two",
			in:      fmt.Sprintf(`{"v":2,"sender":%q,"enc":%q,"ciphertext":"AAAA"}`, sender, valid),
			wantErr: ErrUnsupportedVersion,
		},
		{
			name:    "legacy nonce envelope",
			in:      fmt.Sprintf(`{"sender":%q,"nonce":%q,"ciphertext":"AAAA"}`, sender, valid),
			wantErr: ErrUnsupportedVersion,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s Sealed
			err := json.Unmarshal([]byte(tt.in), &s)
			if err == nil {
				t.Fatalf("Unmarshal accepted %s", tt.in)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestSealForwardSecrecyAgainstSenderKeyCompromise is the proof obligation for
// the move to HPKE mode_auth: an attacker who later steals the SENDER's
// long-term private key must not be able to decrypt an envelope it captured off
// the relay.
//
// The attacker's view is exactly three things — the sender's long-term private
// key bytes, the recipient's public key, and the envelope's wire JSON — and the
// recovery attempted is the pre-HPKE one: NaCl box under the static-static
// X25519 shared secret. The wire is decoded into a map, never into Sealed, so
// this test compiles and runs against BOTH the old envelope and the new one. It
// FAILS against the old construction, where that recovery succeeds; that
// mutation is the evidence, since against the new construction the test can only
// pass.
func TestSealForwardSecrecyAgainstSenderKeyCompromise(t *testing.T) {
	sender, recipient := pair(t), pair(t)
	plaintext := []byte("compromise the sender key after the fact")

	sealed, err := Seal(nil, recipient.Public, sender, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	wire, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(wire, &env); err != nil {
		t.Fatalf("Unmarshal wire: %v", err)
	}

	field := func(key string) ([]byte, bool) {
		s, ok := env[key].(string)
		if !ok {
			return nil, false
		}
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return nil, false
		}
		return b, true
	}

	ciphertext, ok := field("ciphertext")
	if !ok {
		t.Fatalf("wire carries no decodable ciphertext: %s", wire)
	}
	nonceBytes, ok := field("nonce")
	if !ok || len(nonceBytes) != 24 {
		// No usable wire nonce: the static-static recovery has nothing to run
		// under, so the attacker cannot recover the plaintext. That is a pass,
		// not a test error.
		return
	}
	var nonce [24]byte
	copy(nonce[:], nonceBytes)

	senderPriv := sender.PrivateBytes()
	recipientPub := [KeySize]byte(recipient.Public)
	var shared [32]byte
	box.Precompute(&shared, &recipientPub, &senderPriv)

	if got, ok := box.OpenAfterPrecomputation(nil, ciphertext, &nonce, &shared); ok {
		t.Fatalf("the sender's long-term key alone recovered the plaintext %q; there is no forward secrecy against sender key compromise", got)
	}
}
