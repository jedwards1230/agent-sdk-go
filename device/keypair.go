package device

import (
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

// KeyPair is a device's session-encryption keypair: an X25519 public key and
// the private scalar that opens envelopes sealed to it.
//
// Like PublicKey, a keypair is minted once during the pairing the user already
// performs and is used for exactly ONE purpose — sealing and opening session
// envelopes (Seal and Open). It is not a signing key, not a transport key, and
// is never reused in another protocol context. Key separation is maintained by
// that rule and by this package exposing no other operation on it.
//
// The private half is unexported so it cannot be marshalled by accident:
// encoding/json goes through MarshalJSON (public half only), and encoding/yaml
// and reflect-based encoders skip unexported fields. Persist a keypair by
// storing PrivateBytes and reconstructing it with KeyPairFromPrivate.
//
// The zero KeyPair is not usable; Seal and Open reject it.
type KeyPair struct {
	// Public is the public half, safe to publish in an announce payload.
	Public PublicKey

	private [KeySize]byte
}

// Generate mints a new device keypair, reading randomness from rand. A nil rand
// uses crypto/rand.Reader, which is what production callers want; passing a
// deterministic reader is for tests only.
func Generate(rand io.Reader) (*KeyPair, error) {
	if rand == nil {
		rand = cryptorand.Reader
	}
	pub, priv, err := box.GenerateKey(rand)
	if err != nil {
		return nil, fmt.Errorf("device: generate keypair: %w", err)
	}
	kp := &KeyPair{Public: PublicKey(*pub), private: *priv}
	if !kp.Public.Valid() {
		return nil, fmt.Errorf("device: generate keypair: %w", ErrInvalidKey)
	}
	return kp, nil
}

// KeyPairFromPrivate reconstructs a keypair from stored private bytes, deriving
// the public half from the X25519 basepoint. It is the counterpart to
// PrivateBytes: a daemon persists the private bytes at pairing time and rebuilds
// the keypair on every start rather than re-pairing.
//
// The all-zero private key is rejected, as is any scalar whose public half is
// degenerate.
func KeyPairFromPrivate(priv [KeySize]byte) (*KeyPair, error) {
	if priv == ([KeySize]byte{}) {
		return nil, fmt.Errorf("device: keypair from private: %w", ErrInvalidKey)
	}
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("device: keypair from private: %w", err)
	}
	kp := &KeyPair{private: priv}
	copy(kp.Public[:], pub)
	if !kp.Public.Valid() {
		return nil, fmt.Errorf("device: keypair from private: %w", ErrInvalidKey)
	}
	return kp, nil
}

// PrivateBytes returns a copy of the private key. It is deliberately awkward to
// reach: it exists for durable storage at pairing time only (write it to the
// daemon's key file, restore it with KeyPairFromPrivate). It must never be
// logged, sent over a wire, or included in a diagnostic bundle.
func (kp *KeyPair) PrivateBytes() [KeySize]byte { return kp.private }

// MarshalJSON emits only the public half, as {"public":"<base64>"}. The private
// key is never marshalled — use PrivateBytes for the one place it belongs.
func (kp KeyPair) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Public PublicKey `json:"public"`
	}{Public: kp.Public})
}

// ErrNoPrivateKey reports an attempt to reconstruct a KeyPair from a form that
// cannot carry the private half.
var ErrNoPrivateKey = errors.New("device: keypair cannot be decoded (private key is never marshalled)")

// UnmarshalJSON always fails. A marshalled KeyPair carries no private key, so
// decoding one would yield a pair that looks usable but cannot open anything.
// Reconstruct a keypair with KeyPairFromPrivate instead.
func (kp *KeyPair) UnmarshalJSON([]byte) error { return ErrNoPrivateKey }
