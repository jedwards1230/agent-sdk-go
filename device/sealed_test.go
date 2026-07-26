package device

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
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
			if sealed.Sender != sender.Public {
				t.Errorf("Sealed.Sender = %v, want %v", sealed.Sender, sender.Public)
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

func TestSealFreshNoncePerMessage(t *testing.T) {
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
	if first.Nonce == second.Nonce {
		t.Error("two seals reused a nonce")
	}
	if bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Error("two seals of the same plaintext produced identical ciphertext")
	}
	if first.Nonce == ([NonceSize]byte{}) {
		t.Error("nonce is all zero")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	sender, recipient, other := pair(t), pair(t), pair(t)
	plaintext := []byte("do not tamper")

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
			name:    "tampered nonce",
			mutate:  func(s *Sealed) { s.Nonce[0] ^= 0x01 },
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
			rand:      &stubReader{data: bytes.Repeat([]byte{0xab}, NonceSize-1)},
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
			if sealed.Ciphertext != nil || sealed.Nonce != ([NonceSize]byte{}) {
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
	var got Sealed
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Sender != sealed.Sender {
		t.Errorf("Sender = %v, want %v", got.Sender, sealed.Sender)
	}
	if got.Nonce != sealed.Nonce {
		t.Errorf("Nonce = %x, want %x", got.Nonce, sealed.Nonce)
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
	valid := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, NonceSize))
	short := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, NonceSize-1))
	long := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, NonceSize+1))
	sender := pair(t).Public

	tests := []struct {
		name string
		in   string
	}{
		{name: "short nonce", in: fmt.Sprintf(`{"sender":%q,"nonce":%q,"ciphertext":"AAAA"}`, sender, short)},
		{name: "long nonce", in: fmt.Sprintf(`{"sender":%q,"nonce":%q,"ciphertext":"AAAA"}`, sender, long)},
		{name: "empty nonce", in: fmt.Sprintf(`{"sender":%q,"nonce":"","ciphertext":"AAAA"}`, sender)},
		{name: "nonce not base64", in: fmt.Sprintf(`{"sender":%q,"nonce":"!!!!","ciphertext":"AAAA"}`, sender)},
		{name: "ciphertext not base64", in: fmt.Sprintf(`{"sender":%q,"nonce":%q,"ciphertext":"!!!!"}`, sender, valid)},
		{name: "sender not base64", in: fmt.Sprintf(`{"sender":"!!!!","nonce":%q,"ciphertext":"AAAA"}`, valid)},
		{name: "not an object", in: `["nope"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s Sealed
			if err := json.Unmarshal([]byte(tt.in), &s); err == nil {
				t.Fatalf("Unmarshal accepted %s", tt.in)
			}
		})
	}
}
