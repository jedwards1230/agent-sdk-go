package device

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/nacl/box"
)

// NonceSize is the length in bytes of a sealed envelope's nonce
// (XSalsa20-Poly1305).
const NonceSize = 24

// Sealed is one opaque, authenticated message from one device to another. A
// relay can route a Sealed without being able to read it: only the holder of
// the recipient's private key can Open it.
//
// # The plaintext is opaque to the SDK
//
// This is the main misuse risk of a minimal envelope, so it is stated loudly:
// ANY metadata that must be authenticated — session ID, sequence number,
// message kind, timestamps — MUST be inside the plaintext the caller seals. The
// cleartext fields of a Sealed are routing hints for a relay. Only Sender is
// bound by the authenticator (box derives the shared secret from it, so a
// substituted Sender fails to open), and Nonce is bound only in the sense that
// altering it makes the tag fail. Nothing else about the envelope is
// authenticated, because there is nothing else — an attacker may reorder, drop,
// replay, or duplicate envelopes, and only the caller's in-plaintext sequencing
// can detect it.
type Sealed struct {
	// Sender is the sealing device's public key. It is cleartext so the
	// recipient knows which key to open with, and it is bound by box's shared
	// secret: substituting it makes Open fail.
	Sender PublicKey

	// Nonce is a fresh random 192-bit nonce, generated per message by Seal.
	Nonce [NonceSize]byte

	// Ciphertext is box.Seal output: authenticated ciphertext with its
	// Poly1305 tag. There is no path that decrypts it without verifying.
	Ciphertext []byte
}

// ErrOpen reports that a sealed envelope could not be opened. It is
// deliberately the ONLY error Open returns for a cryptographic failure: a
// caller learns that the envelope did not verify and nothing more. Reporting
// whether the nonce, the sender key, or the authentication tag was at fault
// would hand an attacker a decryption oracle.
var ErrOpen = errors.New("device: sealed envelope failed to open")

// Seal encrypts plaintext to recipient, authenticated as coming from sender,
// using NaCl box (X25519 key agreement + XSalsa20-Poly1305 authenticated
// encryption). It returns an opaque envelope safe to hand to an untrusted relay.
//
// # Nonce handling
//
// Every call reads a fresh 24-byte nonce from rand; a nil rand uses
// crypto/rand.Reader (a deterministic reader is for tests only). Nonces are
// never derived, never counted, and never reused. XSalsa20-Poly1305's nonce is
// 192 bits precisely so that random nonces are safe — the collision probability
// across any realistic number of messages is negligible, and unlike a counter
// there is no per-peer state to persist, resume, or desynchronize after a
// crash or a session resume on another device. If rand fails or returns short,
// Seal returns an error rather than sealing under a weak nonce.
//
// Metadata that must be authenticated belongs inside plaintext; see Sealed. An
// empty plaintext is valid and produces a sealable, openable envelope.
func Seal(rand io.Reader, recipient PublicKey, sender *KeyPair, plaintext []byte) (Sealed, error) {
	if rand == nil {
		rand = cryptorand.Reader
	}
	if !recipient.Valid() {
		return Sealed{}, fmt.Errorf("device: seal: recipient: %w", ErrInvalidKey)
	}
	if sender == nil || !sender.Public.Valid() || sender.private == ([KeySize]byte{}) {
		return Sealed{}, fmt.Errorf("device: seal: sender: %w", ErrInvalidKey)
	}

	var nonce [NonceSize]byte
	if _, err := io.ReadFull(rand, nonce[:]); err != nil {
		return Sealed{}, fmt.Errorf("device: seal: read nonce: %w", err)
	}

	peer := [KeySize]byte(recipient)
	priv := sender.private
	return Sealed{
		Sender:     sender.Public,
		Nonce:      nonce,
		Ciphertext: box.Seal(nil, plaintext, &nonce, &peer, &priv),
	}, nil
}

// Open decrypts and authenticates s with the recipient's keypair, returning the
// plaintext. Because box is authenticated encryption, tampering with the
// ciphertext, the nonce, or the sender key yields ErrOpen rather than plaintext;
// this package exposes no way to decrypt without verifying.
//
// Any cryptographic failure returns exactly ErrOpen, with no detail about which
// check failed. Only structurally invalid keys — a zero recipient or a zero
// sender — are reported distinctly, and those are checked before any decryption
// is attempted.
func Open(recipient *KeyPair, s Sealed) ([]byte, error) {
	if recipient == nil || !recipient.Public.Valid() || recipient.private == ([KeySize]byte{}) {
		return nil, fmt.Errorf("device: open: recipient: %w", ErrInvalidKey)
	}
	if !s.Sender.Valid() {
		return nil, fmt.Errorf("device: open: sender: %w", ErrInvalidKey)
	}

	peer := [KeySize]byte(s.Sender)
	priv := recipient.private
	plaintext, ok := box.Open(nil, s.Ciphertext, &s.Nonce, &peer, &priv)
	if !ok {
		return nil, ErrOpen
	}
	return plaintext, nil
}

// sealedWire is the JSON form of a Sealed: the sender in PublicKey's existing
// base64 text form, the nonce and ciphertext as raw URL-safe unpadded base64.
type sealedWire struct {
	Sender     PublicKey `json:"sender"`
	Nonce      string    `json:"nonce"`
	Ciphertext string    `json:"ciphertext"`
}

// MarshalJSON implements json.Marshaler.
func (s Sealed) MarshalJSON() ([]byte, error) {
	return json.Marshal(sealedWire{
		Sender:     s.Sender,
		Nonce:      base64.RawURLEncoding.EncodeToString(s.Nonce[:]),
		Ciphertext: base64.RawURLEncoding.EncodeToString(s.Ciphertext),
	})
}

// UnmarshalJSON implements json.Unmarshaler. A nonce of the wrong length is
// rejected: a short nonce is never zero-padded into something openable.
func (s *Sealed) UnmarshalJSON(b []byte) error {
	var w sealedWire
	if err := json.Unmarshal(b, &w); err != nil {
		return fmt.Errorf("device: unmarshal sealed: %w", err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(w.Nonce)
	if err != nil {
		return fmt.Errorf("device: unmarshal sealed: nonce: %w", err)
	}
	if len(nonce) != NonceSize {
		return fmt.Errorf("device: unmarshal sealed: nonce: got %d bytes, want %d", len(nonce), NonceSize)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(w.Ciphertext)
	if err != nil {
		return fmt.Errorf("device: unmarshal sealed: ciphertext: %w", err)
	}
	*s = Sealed{Sender: w.Sender, Ciphertext: ciphertext}
	copy(s.Nonce[:], nonce)
	return nil
}
