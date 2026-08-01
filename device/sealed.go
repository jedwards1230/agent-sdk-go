package device

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jedwards1230/agent-sdk-go/internal/hpke"
)

// SealedVersion is the current sealed-envelope format version. Everything Seal
// produces carries it, and Open rejects anything else.
const SealedVersion = 1

// Sealed is one opaque, authenticated message from one device to another. A
// relay can route a Sealed without being able to read it: only the holder of
// the recipient's private key can Open it.
//
// # Construction
//
// RFC 9180 HPKE in mode_auth, ciphersuite DHKEM(X25519, HKDF-SHA256) /
// HKDF-SHA256 / ChaCha20Poly1305, single-shot at sequence 0. The sender mints a
// fresh ephemeral X25519 keypair per envelope (Ephemeral is its public half)
// and the KEM derives the shared secret from DH(ephemeral, recipient) ||
// DH(sender, recipient). The second DH is what authenticates the sender: only a
// holder of Sender's private key can produce a secret the recipient
// reproduces. That authentication is implicit and deniable — there is no
// signature, which is what lets the identity key stay X25519.
//
// # Forward secrecy: one-sided, deliberately
//
// Because the ephemeral is the sender's, compromise of the SENDER's long-term
// private key does not reveal past envelopes: the attacker still lacks the
// per-envelope ephemeral private key, which never left the sealing process.
// Compromise of the RECIPIENT's long-term private key DOES still reveal them —
// the recipient contributes a static key to both DHs. Closing that half needs
// the recipient to contribute an ephemeral too, i.e. an interactive handshake
// (Noise KK / WireGuard) or published one-time prekeys (X3DH), both of which
// need transport and application state a one-shot stateless envelope has no
// place to keep. This is a known and accepted limitation, not an oversight.
//
// # The plaintext is opaque to the SDK
//
// This is the main misuse risk of a minimal envelope, so it is stated loudly:
// ANY metadata that must be authenticated — session ID, sequence number,
// message kind, timestamps — MUST be inside the plaintext the caller seals. The
// cleartext fields of a Sealed are routing hints for a relay. They are all
// bound by the construction (Version, Sender and Ephemeral are the AEAD's
// associated data, and substituting Sender or Ephemeral additionally makes the
// KEM derive a different secret), but binding is not meaning: nothing else
// about the envelope is authenticated, because there is nothing else — an
// attacker may reorder, drop, replay, or duplicate envelopes, and only the
// caller's in-plaintext sequencing can detect it.
//
// # Sender is a claim until the caller checks it
//
// Open verifies that the envelope was sealed by the holder of Sender's private
// key. It does NOT verify that Sender is anyone the recipient trusts: anyone
// can seal with their own identity key. A recipient MUST compare Sender against
// its own pinned/authorized set before acting on the plaintext.
type Sealed struct {
	// Version is the envelope format version. Seal always sets SealedVersion;
	// Open and UnmarshalJSON reject anything else.
	Version int

	// Sender is the sealing device's LONG-TERM identity public key. It is
	// cleartext so the recipient knows which key to authenticate against, and
	// it is the routing/pin handle. Substituting it makes Open fail.
	Sender PublicKey

	// Ephemeral is the KEM encapsulation: the public half of the per-envelope
	// ephemeral X25519 keypair the sender minted for this message. It is fresh
	// for every Seal and is never the sender's identity key.
	Ephemeral [KeySize]byte

	// Ciphertext is the ChaCha20Poly1305 output: authenticated ciphertext with
	// its tag. There is no path that decrypts it without verifying.
	Ciphertext []byte
}

// ErrOpen reports that a sealed envelope could not be opened. It is
// deliberately the ONLY error Open returns for a cryptographic failure: a
// caller learns that the envelope did not verify and nothing more. Reporting
// whether the encapsulation, the sender key, or the authentication tag was at
// fault would hand an attacker a decryption oracle.
var ErrOpen = errors.New("device: sealed envelope failed to open")

// ErrUnsupportedVersion reports an envelope whose version this build does not
// implement.
//
// Version handling fails CLOSED, which is the deliberate opposite of the
// open-set enums elsewhere in the SDK (announce.EndpointKind and
// announce.SessionState keep unknown values so a newer peer's payload still
// round-trips). An envelope is not a descriptor: an unknown version means an
// unknown construction, and processing it under v1's rules — or forwarding it
// as if understood — is exactly the downgrade this rejects.
var ErrUnsupportedVersion = errors.New("device: unsupported sealed envelope version")

// sealedInfo is the HPKE info string, derived from the envelope version so a
// future v2 can never key-collide with v1 even on the same ciphersuite.
func sealedInfo(version int) []byte {
	return fmt.Appendf(nil, "agent-sdk-go/device/sealed/v%d", version)
}

// sealedAAD is the canonical header bytes used as HPKE associated data:
//
//	I2OSP(version, 1) || Sender || Ephemeral
//
// Sender and Ephemeral are already implicitly bound — substituting either makes
// the KEM derive a different shared secret, so Open fails regardless. The AAD
// makes that binding explicit and, more importantly, covers the version byte,
// which the KEM does not see.
//
// PRECONDITION: 0 <= version < 256. I2OSP(version, 1) is one byte, and
// byte(version) truncates silently past it — versions 1 and 257 would produce
// the same AAD. Both call sites gate on version == SealedVersion first, so this
// is unreachable today; a v256 needs a wider length prefix here, and by then the
// envelope format has changed enough to be revisiting this function anyway.
func sealedAAD(version int, sender PublicKey, ephemeral []byte) []byte {
	aad := make([]byte, 0, 1+2*KeySize)
	aad = append(aad, byte(version))
	aad = append(aad, sender[:]...)
	aad = append(aad, ephemeral...)
	return aad
}

// Seal encrypts plaintext to recipient, authenticated as coming from sender,
// using RFC 9180 HPKE mode_auth (see Sealed for the construction and its
// forward-secrecy limits). It returns an opaque envelope safe to hand to an
// untrusted relay.
//
// # Randomness
//
// Every call reads 32 bytes from rand for a fresh ephemeral private scalar; a
// nil rand uses crypto/rand.Reader (a deterministic reader is for tests only).
// There is no wire nonce to manage: HPKE derives the AEAD nonce from the key
// schedule, and PROVIDED rand is a real CSPRNG, each envelope gets its own
// ephemeral — hence its own key — so sealing at sequence 0 every time never
// reuses a (key, nonce) pair. If rand fails or returns short, Seal returns an
// error rather than sealing under weak randomness.
//
// That guarantee is conditional on rand, and the rand parameter is a seam for
// tests, so state the cost plainly: a reader that REPEATS its output produces
// the same ephemeral, hence the same HPKE key AND the same base_nonce, for two
// different envelopes. That is catastrophic, not degraded — ChaCha20Poly1305
// reuses its keystream, XORing two ciphertexts reveals the XOR of the
// plaintexts, and the Poly1305 one-time key is recoverable, so the attacker can
// forge tags as well as read. Pass nil, or crypto/rand.Reader, anywhere outside
// a test.
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

	senderPriv := sender.private
	enc, ciphertext, err := hpke.SealAuth(rand, recipient[:], senderPriv[:], sealedInfo(SealedVersion),
		func(enc []byte) []byte {
			return sealedAAD(SealedVersion, sender.Public, enc)
		}, plaintext)
	if err != nil {
		return Sealed{}, fmt.Errorf("device: seal: %w", err)
	}

	s := Sealed{Version: SealedVersion, Sender: sender.Public, Ciphertext: ciphertext}
	copy(s.Ephemeral[:], enc)
	return s, nil
}

// Open decrypts and authenticates s with the recipient's keypair, returning the
// plaintext. Because HPKE is authenticated encryption, tampering with the
// ciphertext, the encapsulation, the version, or the sender key yields ErrOpen
// rather than plaintext; this package exposes no way to decrypt without
// verifying.
//
// A successful Open proves the envelope was sealed by the holder of s.Sender's
// private key. It does NOT prove that key is trusted — check s.Sender against
// the caller's own authorized set.
//
// Any cryptographic failure returns exactly ErrOpen, with no detail about which
// check failed. Only structural problems — an unsupported version, a zero
// recipient, a zero sender — are reported distinctly, and those are checked
// before any decryption is attempted.
func Open(recipient *KeyPair, s Sealed) ([]byte, error) {
	if s.Version != SealedVersion {
		return nil, fmt.Errorf("device: open: version %d: %w", s.Version, ErrUnsupportedVersion)
	}
	if recipient == nil || !recipient.Public.Valid() || recipient.private == ([KeySize]byte{}) {
		return nil, fmt.Errorf("device: open: recipient: %w", ErrInvalidKey)
	}
	if !s.Sender.Valid() {
		return nil, fmt.Errorf("device: open: sender: %w", ErrInvalidKey)
	}

	recipientPriv := recipient.private
	plaintext, err := hpke.OpenAuth(recipientPriv[:], s.Sender[:], s.Ephemeral[:],
		sealedInfo(s.Version), sealedAAD(s.Version, s.Sender, s.Ephemeral[:]), s.Ciphertext)
	if err != nil {
		return nil, ErrOpen
	}
	return plaintext, nil
}

// sealedWire is the JSON form of a Sealed: the version as an integer, the
// sender in PublicKey's existing base64 text form, the encapsulated key and the
// ciphertext as raw URL-safe unpadded base64.
type sealedWire struct {
	Version    int       `json:"v"`
	Sender     PublicKey `json:"sender"`
	Ephemeral  string    `json:"enc"`
	Ciphertext string    `json:"ciphertext"`
}

// MarshalJSON implements json.Marshaler. It emits s.Version verbatim rather
// than substituting SealedVersion, so a hand-built envelope cannot be laundered
// into looking current.
func (s Sealed) MarshalJSON() ([]byte, error) {
	return json.Marshal(sealedWire{
		Version:    s.Version,
		Sender:     s.Sender,
		Ephemeral:  base64.RawURLEncoding.EncodeToString(s.Ephemeral[:]),
		Ciphertext: base64.RawURLEncoding.EncodeToString(s.Ciphertext),
	})
}

// UnmarshalJSON implements json.Unmarshaler. It fails closed on an unsupported
// version, and an encapsulated key of the wrong length is rejected: a short one
// is never zero-padded into something that reaches key agreement.
func (s *Sealed) UnmarshalJSON(b []byte) error {
	var w sealedWire
	if err := json.Unmarshal(b, &w); err != nil {
		return fmt.Errorf("device: unmarshal sealed: %w", err)
	}
	if w.Version != SealedVersion {
		return fmt.Errorf("device: unmarshal sealed: version %d: %w", w.Version, ErrUnsupportedVersion)
	}
	enc, err := base64.RawURLEncoding.DecodeString(w.Ephemeral)
	if err != nil {
		return fmt.Errorf("device: unmarshal sealed: enc: %w", err)
	}
	if len(enc) != KeySize {
		return fmt.Errorf("device: unmarshal sealed: enc: got %d bytes, want %d", len(enc), KeySize)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(w.Ciphertext)
	if err != nil {
		return fmt.Errorf("device: unmarshal sealed: ciphertext: %w", err)
	}
	*s = Sealed{Version: w.Version, Sender: w.Sender, Ciphertext: ciphertext}
	copy(s.Ephemeral[:], enc)
	return nil
}
