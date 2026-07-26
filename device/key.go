// Package device is the SDK's device key vocabulary: the public half of a
// device's session-encryption keypair, and (with the sealing surface) the
// envelope type that lets a relay carry session content it cannot read.
//
// This package is types only. It opens no socket, fetches no key, and knows
// nothing about accounts, rosters, or pairing — those are the consuming
// application's concerns.
package device

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// KeySize is the length in bytes of a device public key (X25519).
const KeySize = 32

// PublicKey is the public half of a device's session-encryption keypair: a
// 32-byte X25519 public key.
//
// A device key is minted once, during the pairing the user already performs,
// and is used for exactly ONE purpose — sealing and opening session envelopes
// for that device. It is never a signing key, never a transport key, and never
// reused in another protocol context. Key separation is maintained by that
// rule and by this package exposing no other operation on it.
//
// The zero value is not a usable key; Valid reports whether a key is set.
type PublicKey [KeySize]byte

// ParsePublicKey decodes a base64 (raw URL-safe, unpadded) device public key
// as produced by PublicKey.String.
func ParsePublicKey(s string) (PublicKey, error) {
	var k PublicKey
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return k, fmt.Errorf("device: parse public key: %w", err)
	}
	if len(b) != KeySize {
		return k, fmt.Errorf("device: parse public key: got %d bytes, want %d", len(b), KeySize)
	}
	copy(k[:], b)
	return k, nil
}

// Valid reports whether k is a non-zero key. The all-zero key is rejected: it
// is the X25519 identity element, whose shared secret is degenerate.
func (k PublicKey) Valid() bool { return k != PublicKey{} }

// String returns the raw URL-safe unpadded base64 encoding of k, the wire form
// used in the announce payload and the sealed envelope.
func (k PublicKey) String() string { return base64.RawURLEncoding.EncodeToString(k[:]) }

// ID returns a short, stable, human-comparable fingerprint of k: the first 8
// bytes of SHA-256(k), hex encoded. It identifies a key in logs, rosters, and
// revocation lists without carrying the key itself. It is an identifier, not a
// security check — compare full keys, never fingerprints, when authenticating.
func (k PublicKey) ID() string {
	sum := sha256.Sum256(k[:])
	return hex.EncodeToString(sum[:8])
}

// MarshalText implements encoding.TextMarshaler, so a PublicKey round-trips
// through JSON and YAML as its base64 string form.
func (k PublicKey) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty string decodes to
// the zero key, so an absent key is representable.
func (k *PublicKey) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*k = PublicKey{}
		return nil
	}
	parsed, err := ParsePublicKey(string(b))
	if err != nil {
		return err
	}
	*k = parsed
	return nil
}

// ErrInvalidKey reports a malformed or zero key where a usable one is required.
var ErrInvalidKey = errors.New("device: invalid public key")
