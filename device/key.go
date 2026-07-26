// Package device is the SDK's device key vocabulary: the public half of a
// device's session-encryption keypair, and (with the sealing surface) the
// envelope type that lets a relay carry session content it cannot read.
//
// This package is types only. It opens no socket, fetches no key, and knows
// nothing about accounts, rosters, or pairing — those are the consuming
// application's concerns.
//
// # Construction
//
// Envelopes use NaCl box (golang.org/x/crypto/nacl/box): X25519 key agreement
// plus XSalsa20-Poly1305 authenticated encryption, pairwise from a sender
// keypair to a recipient public key. No key derivation, framing, or handshake
// is invented here.
//
// # Revocation: an assumption, not a settled decision
//
// The vocabulary in this package ASSUMES revocation means dropping a revoked
// device's public key from the account's authorized set. The authorized set is
// the consuming application's data — this package deliberately ships no account
// model, no roster, and no key store. PublicKey.ID is the handle a revocation
// list would use.
//
// That assumption is cheap but has a real cost: dropping a key needs no
// re-fetch and no coordination, yet gives NO forward secrecy. A stolen device
// can still open every envelope it already holds, and remains able to open
// anything sent to it until the drop propagates to every sender. The
// alternative — rotating a shared group key on revocation — gives forward
// secrecy against a stolen device but forces every remaining device to re-fetch
// the new key before it can talk again.
//
// This is an open protocol commitment that the project owner must confirm
// before the wire format is fixed. Nothing here forecloses either answer: Seal
// and Open are pairwise (sender keypair to recipient public key) and assume no
// single long-lived per-account key, so group-key rotation remains
// implementable on top without changing these types.
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
		return k, fmt.Errorf("device: parse public key: %w: %w", ErrInvalidKey, err)
	}
	if len(b) != KeySize {
		return k, fmt.Errorf("device: parse public key: %w: got %d bytes, want %d", ErrInvalidKey, len(b), KeySize)
	}
	copy(k[:], b)
	return k, nil
}

// Valid reports whether k is a non-zero key.
//
// Parsing and validity are deliberately separate. ParsePublicKey accepts the
// all-zero key — it is a well-formed 32-byte encoding, and callers use it to
// mean "no key advertised" (see the announce payload, where a key is optional).
// What the zero key is not is *usable*: it is the X25519 identity element,
// whose shared secret is degenerate, so Seal and Open reject it at the point of
// key agreement.
//
// So: the zero key parses, reports Valid() == false, and never reaches a
// cryptographic operation. Test presence with Valid, not with a parse error.
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
