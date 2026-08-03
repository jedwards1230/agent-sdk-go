// Package device is the SDK's device key vocabulary: the public half of a
// device's session-encryption keypair, and (with the sealing surface) the
// envelope type that lets a relay carry session content it cannot read.
//
// This package is types only. It opens no socket, fetches no key, and knows
// nothing about accounts, rosters, or pairing — those are the consuming
// application's concerns.
//
// SPECULATIVE — no consumer. Only announce/ imports this package, and announce/
// itself has no importer in this repo or any known embedder; both were built as
// M8 pairing groundwork ahead of the application work that would use them. Per
// the third gate in CLAUDE.md they carry no stability guarantee, and a breaking
// change here is routine until a real consumer lands. See docs/DESIGN.md,
// "Extension tiers → Speculative".
//
// # Construction
//
// Envelopes use RFC 9180 HPKE in mode_auth (0x02), ciphersuite
// DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / ChaCha20Poly1305, single-shot at
// sequence 0. The sender mints a fresh ephemeral X25519 keypair for every
// envelope, and the KEM derives the shared secret from
// DH(ephemeral, recipient) || DH(sender, recipient). The second DH is what
// authenticates the sender: only a holder of the sender's long-term private key
// can produce a secret the recipient reproduces. That authentication is
// implicit and deniable — there is no signature, which is what lets the device
// identity key stay X25519. No key derivation, framing, or handshake is
// invented here; the labels and orderings are the RFC's.
//
// Forward secrecy is ONE-SIDED, deliberately. Compromise of the SENDER's
// long-term private key does not reveal past envelopes, because the attacker
// still lacks the per-envelope ephemeral private key, which never left the
// sealing process. Compromise of the RECIPIENT's long-term private key DOES
// still reveal them: the recipient contributes a static key to both DHs.
// Closing that half needs the recipient to contribute an ephemeral too — an
// interactive handshake or published one-time prekeys — which needs transport
// and application state a one-shot stateless envelope has nowhere to keep. See
// Sealed for the full statement.
//
// The module still requires golang.org/x/crypto, but the sealing path no longer
// uses NaCl box at all: box.GenerateKey in the keypair constructor is the last
// remaining use, and that is why the dependency remains.
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
// all-zero key: it is a well-formed 32-byte encoding, and a fixed-size array
// has no room for an "absent" state, so refusing to parse it would mean
// refusing to decode a structurally valid message. What the zero key is not is
// *usable* — it is the X25519 identity element, whose shared secret is
// degenerate, so Seal and Open reject it at the point of key agreement, and
// announce.Payload.Validate rejects it as a device key.
//
// So: the zero key parses, reports Valid() == false, is never accepted where a
// key is required, and never reaches a cryptographic operation. Test presence
// with Valid, not with a parse error.
func (k PublicKey) Valid() bool { return k != PublicKey{} }

// String returns the raw URL-safe unpadded base64 encoding of k, the wire form
// used in the announce payload and the sealed envelope.
func (k PublicKey) String() string { return base64.RawURLEncoding.EncodeToString(k[:]) }

// ID returns a short, stable, human-comparable fingerprint of k: the first 8
// bytes of SHA-256(k), hex encoded.
//
// # It is the pin handle
//
// This is the identifier an embedder that pins device keys stores alongside the
// pinned key and shows to a human. It is derived from the key alone, so it is
// stable for the life of that key and identical on both ends of a pairing; it
// carries no key material, so it is safe in a log line, a roster row, a
// revocation list, or a confirmation prompt ("pair with device a1b2c3d4e5f60718?").
// The full key is what gets compared; the ID is what gets displayed and indexed.
//
// It is an identifier, NOT a security check. Sixty-four bits is short enough to
// collide on purpose, so authenticate by comparing full keys — a check that
// matches on fingerprints would let an attacker's key pass for a pinned one.
// See ErrPinnedKeyMismatch.
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

// ErrPinnedKeyMismatch reports that a device presented a key other than the one
// already pinned for it.
//
// # Trust on first use
//
// Trust in this vocabulary is trust-on-first-use. An embedder learns a device's
// public key ONCE, during the pairing the user already performs, and pins it
// against that device's identity — PublicKey.ID is the handle it files the pin
// under and shows the user. A later announce claiming to be that device with a
// DIFFERENT key is rejected rather than accepted as a rotation, because a
// substituted key and a rotated key are indistinguishable from the payload
// alone; nothing in an announce is signed by the old key, so believing the new
// one would make key substitution free. The accepted tradeoff is that a
// legitimate device rebuild — a reinstall, a wiped disk, a fresh keypair —
// requires an explicit re-pair by the user. Making that automatic would make
// the attack automatic too.
//
// # What the SDK does and does not ship
//
// It ships this sentinel and PublicKey.ID, and nothing else. Where pins live,
// how long they last, and when a re-pair is permitted are application policy
// and fail the membership test in CLAUDE.md — there is no pin store, no roster,
// no account type, and no comparison helper here, because a second application
// would not want this one's answers. What a second application does want
// unchanged is a distinct, matchable error, so a mismatch can be reported as
// itself instead of collapsing into a generic auth failure — the difference
// between "log in again" and "this is not the device you paired with":
//
//	pinned, ok := app.PinnedKey(serverID)
//	if ok && pinned != p.DeviceKey {
//		return fmt.Errorf("server %s: pinned %s: %w", serverID, pinned.ID(), device.ErrPinnedKeyMismatch)
//	}
//
// This package never returns it. It is vocabulary for the embedder that does.
var ErrPinnedKeyMismatch = errors.New("device: pinned key mismatch")
