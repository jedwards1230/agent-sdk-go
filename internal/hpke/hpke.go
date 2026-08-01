// Package hpke implements the subset of RFC 9180 (Hybrid Public Key
// Encryption) that device envelopes need: mode_auth with
// DHKEM(X25519, HKDF-SHA256), HKDF-SHA256 and ChaCha20Poly1305, single-shot at
// sequence 0.
//
// It is deliberately not a general HPKE library. There is one ciphersuite, one
// mode, no PSK, no exporter surface, and no multi-message context: a caller
// seals exactly one message per encapsulation. Anything outside that shape
// belongs in a real HPKE library, not here.
//
// # Why mode_auth
//
// mode_auth (0x02) is the mode where the sender contributes BOTH a fresh
// ephemeral key and its long-term static key. DHKEM's AuthEncap derives the
// shared secret from DH(skE, pkR) || DH(skS, pkR), so only a holder of the
// sender's long-term private key can produce a shared secret that the
// recipient's AuthDecap(enc, skR, pkS) reproduces. Sender authentication is
// therefore implicit and deniable — no signature, and no signing key, which
// matters because X25519 identity keys cannot sign.
//
// # Forward secrecy is one-sided
//
// The ephemeral is the SENDER's. The recipient contributes a static key to both
// DHs, so an attacker who later steals the recipient's long-term private key
// can still recompute the shared secret for captured ciphertexts. Closing that
// half needs a recipient ephemeral — an interactive handshake or published
// one-time prekeys — which a one-shot stateless envelope does not admit.
//
// All byte strings are RFC 9180's own serializations; the implementation
// follows §4.1 (LabeledExtract/LabeledExpand), §4.1 and §5.1 (key schedule) and
// §7.1.2 (DHKEM(X25519)) literally, and is checked against the CFRG test
// vectors in testdata.
package hpke

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// The single ciphersuite this package implements (RFC 9180 §7).
const (
	kemID  = 0x0020 // DHKEM(X25519, HKDF-SHA256)
	kdfID  = 0x0001 // HKDF-SHA256
	aeadID = 0x0003 // ChaCha20Poly1305

	modeAuth = 0x02 // RFC 9180 §5.1
)

// Suite parameter lengths, in bytes (RFC 9180 §7.1, §7.2, §7.3).
const (
	nSecret = 32 // DHKEM(X25519, HKDF-SHA256) shared secret
	nK      = 32 // ChaCha20Poly1305 key
	nN      = 12 // ChaCha20Poly1305 nonce

	// KeySize is the length of an X25519 public key, private scalar, and
	// therefore of an encapsulated key (enc).
	KeySize = 32
)

// ErrOpen reports that [OpenAuth] failed somewhere after its inputs were
// checked for length: decapsulation, the key schedule, or the AEAD. It
// deliberately carries no detail about which — see [OpenAuth] on why.
var ErrOpen = errors.New("hpke: open failed")

// ErrKeySize reports a key or encapsulation of the wrong length. It is
// structural, not cryptographic: it says the caller passed the wrong number of
// bytes, which is a bug in the caller and reveals nothing about any secret.
var ErrKeySize = errors.New("hpke: wrong key length")

// SealAuth encapsulates to recipientPub in mode_auth as senderPriv and seals
// plaintext under the resulting key schedule at sequence 0. It returns the
// encapsulated ephemeral public key (enc) and the AEAD ciphertext.
//
// rand supplies the 32-byte ephemeral private scalar; it must be
// crypto/rand.Reader outside tests. The ephemeral private key never leaves
// this function.
//
// aadFor builds the AEAD associated data and is a function, not a []byte,
// because a caller that binds enc into its AAD — as device envelopes do —
// cannot know enc until encapsulation has happened. Whatever it returns is
// passed to the AEAD verbatim; a caller with fixed associated data can ignore
// the argument. It is called exactly once, after encapsulation.
func SealAuth(rand io.Reader, recipientPub, senderPriv, info []byte, aadFor func(enc []byte) []byte, plaintext []byte) (enc, ciphertext []byte, err error) {
	enc, sharedSecret, err := authEncap(rand, recipientPub, senderPriv)
	if err != nil {
		return nil, nil, err
	}
	key, baseNonce, _, err := keySchedule(sharedSecret, info)
	if err != nil {
		return nil, nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, nil, fmt.Errorf("hpke: aead: %w", err)
	}
	var aad []byte
	if aadFor != nil {
		aad = aadFor(enc)
	}
	// Sequence 0: the RFC's nonce = base_nonce XOR I2OSP(seq, Nn) is the
	// identity, so base_nonce is the nonce.
	return enc, aead.Seal(nil, baseNonce, plaintext, aad), nil
}

// OpenAuth decapsulates enc with recipientPriv in mode_auth, authenticating it
// as coming from senderPub, and opens ciphertext at sequence 0.
//
// # The error contract, exactly
//
// There are two error classes and the split is drawn at input validation:
//
//   - LENGTH is checked first, before any key material is touched, and a wrong
//     length is reported as ErrKeySize. That is a structural bug in the caller
//     and says nothing about a secret.
//   - EVERYTHING after that — a point crypto/ecdh rejects, a low-order or
//     all-zero encapsulation, a shared secret that does not match, a key
//     schedule that will not derive, a forged tag — is exactly ErrOpen and
//     nothing more. No wrapping, no cause, no distinguishable message.
//
// The second half is the point: an attacker who can feed this function chosen
// bytes learns one bit, "it did not open", from every one of those failures.
// Reporting which step failed would turn the package into a decryption oracle —
// low-order encapsulations in particular are attacker-supplied, and a distinct
// error for them would confirm the recipient key reached ECDH at all.
func OpenAuth(recipientPriv, senderPub, enc, info, aad, ciphertext []byte) ([]byte, error) {
	// Pre-validation: structural, and the only failure reported distinctly.
	// authDecap re-checks these lengths for its own direct callers; doing it
	// here is what keeps the check ahead of the ErrOpen collapse below.
	if len(enc) != KeySize || len(recipientPriv) != KeySize || len(senderPub) != KeySize {
		return nil, fmt.Errorf("hpke: decap: %w", ErrKeySize)
	}
	sharedSecret, err := authDecap(enc, recipientPriv, senderPub)
	if err != nil {
		return nil, ErrOpen
	}
	key, baseNonce, _, err := keySchedule(sharedSecret, info)
	if err != nil {
		return nil, ErrOpen
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, ErrOpen
	}
	plaintext, err := aead.Open(nil, baseNonce, ciphertext, aad)
	if err != nil {
		return nil, ErrOpen
	}
	return plaintext, nil
}

// authEncap is RFC 9180 §7.1.2 AuthEncap for DHKEM(X25519, HKDF-SHA256): it
// mints an ephemeral keypair, runs two Diffie-Hellmans — ephemeral-to-static
// for secrecy and static-to-static for sender authentication — and derives the
// shared secret over both.
//
// The ephemeral scalar is read from rand rather than generated by crypto/ecdh
// so that a test can replay a published vector's skEm. crypto/ecdh clamps the
// scalar per RFC 7748 internally, so the raw vector scalar yields the vector's
// pkEm unchanged.
func authEncap(rand io.Reader, recipientPub, senderPriv []byte) (enc, sharedSecret []byte, err error) {
	if len(recipientPub) != KeySize || len(senderPriv) != KeySize {
		return nil, nil, fmt.Errorf("hpke: encap: %w", ErrKeySize)
	}
	curve := ecdh.X25519()
	pkR, err := curve.NewPublicKey(recipientPub)
	if err != nil {
		return nil, nil, fmt.Errorf("hpke: encap: recipient key: %w", err)
	}
	skS, err := curve.NewPrivateKey(senderPriv)
	if err != nil {
		return nil, nil, fmt.Errorf("hpke: encap: sender key: %w", err)
	}

	scalar := make([]byte, KeySize)
	if _, err := io.ReadFull(rand, scalar); err != nil {
		return nil, nil, fmt.Errorf("hpke: encap: read ephemeral: %w", err)
	}
	skE, err := curve.NewPrivateKey(scalar)
	// This scrubs OUR copy only. NewPrivateKey has already copied the scalar
	// into the returned key, and crypto/ecdh offers no way to scrub that copy —
	// it lives until the key is collected. So the clear reduces the window in
	// which the ephemeral scalar is resident, it does not eliminate it.
	clear(scalar)
	if err != nil {
		return nil, nil, fmt.Errorf("hpke: encap: ephemeral key: %w", err)
	}

	// dh = DH(skE, pkR) || DH(skS, pkR). crypto/ecdh's ECDH rejects
	// low-order points and all-zero outputs (RFC 7748 §6.1), which RFC 9180
	// requires; propagate that rather than reimplementing it.
	dhE, err := skE.ECDH(pkR)
	if err != nil {
		return nil, nil, fmt.Errorf("hpke: encap: ephemeral dh: %w", err)
	}
	dhS, err := skS.ECDH(pkR)
	if err != nil {
		return nil, nil, fmt.Errorf("hpke: encap: static dh: %w", err)
	}
	dh := make([]byte, 0, len(dhE)+len(dhS))
	dh = append(dh, dhE...)
	dh = append(dh, dhS...)

	enc = skE.PublicKey().Bytes()
	// kem_context = enc || pkRm || pkSm
	kemContext := make([]byte, 0, 3*KeySize)
	kemContext = append(kemContext, enc...)
	kemContext = append(kemContext, pkR.Bytes()...)
	kemContext = append(kemContext, skS.PublicKey().Bytes()...)

	sharedSecret, err = extractAndExpand(dh, kemContext)
	if err != nil {
		return nil, nil, err
	}
	return enc, sharedSecret, nil
}

// authDecap is RFC 9180 §7.1.2 AuthDecap: the mirror of authEncap, deriving the
// same shared secret from the recipient's private key and the sender's public
// key. A sender who does not hold senderPub's private key cannot make the
// static-to-static half agree, which is what authenticates the sender.
func authDecap(enc, recipientPriv, senderPub []byte) ([]byte, error) {
	if len(enc) != KeySize || len(recipientPriv) != KeySize || len(senderPub) != KeySize {
		return nil, fmt.Errorf("hpke: decap: %w", ErrKeySize)
	}
	curve := ecdh.X25519()
	pkE, err := curve.NewPublicKey(enc)
	if err != nil {
		return nil, fmt.Errorf("hpke: decap: encapsulated key: %w", err)
	}
	skR, err := curve.NewPrivateKey(recipientPriv)
	if err != nil {
		return nil, fmt.Errorf("hpke: decap: recipient key: %w", err)
	}
	pkS, err := curve.NewPublicKey(senderPub)
	if err != nil {
		return nil, fmt.Errorf("hpke: decap: sender key: %w", err)
	}

	// dh = DH(skR, pkE) || DH(skR, pkS)
	dhE, err := skR.ECDH(pkE)
	if err != nil {
		return nil, fmt.Errorf("hpke: decap: ephemeral dh: %w", err)
	}
	dhS, err := skR.ECDH(pkS)
	if err != nil {
		return nil, fmt.Errorf("hpke: decap: static dh: %w", err)
	}
	dh := make([]byte, 0, len(dhE)+len(dhS))
	dh = append(dh, dhE...)
	dh = append(dh, dhS...)

	// kem_context = enc || pkRm || pkSm — the same ordering as AuthEncap.
	kemContext := make([]byte, 0, 3*KeySize)
	kemContext = append(kemContext, enc...)
	kemContext = append(kemContext, skR.PublicKey().Bytes()...)
	kemContext = append(kemContext, pkS.Bytes()...)

	return extractAndExpand(dh, kemContext)
}

// extractAndExpand is RFC 9180 §4.1 ExtractAndExpand, run under the KEM's
// suite ID.
func extractAndExpand(dh, kemContext []byte) ([]byte, error) {
	suite := kemSuiteID()
	eaePRK, err := labeledExtract(suite, nil, "eae_prk", dh)
	if err != nil {
		return nil, fmt.Errorf("hpke: extract eae_prk: %w", err)
	}
	sharedSecret, err := labeledExpand(suite, eaePRK, "shared_secret", kemContext, nSecret)
	if err != nil {
		return nil, fmt.Errorf("hpke: expand shared_secret: %w", err)
	}
	return sharedSecret, nil
}

// keySchedule is RFC 9180 §5.1 KeySchedule for mode_auth with no PSK: psk and
// psk_id are both the empty string, so both hashes are over the label alone.
//
// It derives and returns exporterSecret even though the package exposes no
// exporter (see the package doc) and both callers discard it with _. That is
// deliberate, not dead code: exporter_secret is a published field of every CFRG
// vector, and TestKeyScheduleMatchesVector asserts it. Checking it proves the
// schedule agrees with the RFC at a third independent point, which is worth
// more than the line it costs — and derivation order does not affect key or
// base_nonce, so returning it changes nothing else.
func keySchedule(sharedSecret, info []byte) (key, baseNonce, exporterSecret []byte, err error) {
	suite := suiteID()
	var psk, pskID []byte

	pskIDHash, err := labeledExtract(suite, nil, "psk_id_hash", pskID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hpke: extract psk_id_hash: %w", err)
	}
	infoHash, err := labeledExtract(suite, nil, "info_hash", info)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hpke: extract info_hash: %w", err)
	}

	// key_schedule_context = I2OSP(mode, 1) || psk_id_hash || info_hash
	ksContext := make([]byte, 0, 1+len(pskIDHash)+len(infoHash))
	ksContext = append(ksContext, modeAuth)
	ksContext = append(ksContext, pskIDHash...)
	ksContext = append(ksContext, infoHash...)

	secret, err := labeledExtract(suite, sharedSecret, "secret", psk)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hpke: extract secret: %w", err)
	}
	if key, err = labeledExpand(suite, secret, "key", ksContext, nK); err != nil {
		return nil, nil, nil, fmt.Errorf("hpke: expand key: %w", err)
	}
	if baseNonce, err = labeledExpand(suite, secret, "base_nonce", ksContext, nN); err != nil {
		return nil, nil, nil, fmt.Errorf("hpke: expand base_nonce: %w", err)
	}
	if exporterSecret, err = labeledExpand(suite, secret, "exp", ksContext, sha256.Size); err != nil {
		return nil, nil, nil, fmt.Errorf("hpke: expand exporter_secret: %w", err)
	}
	return key, baseNonce, exporterSecret, nil
}

// labeledExtract is RFC 9180 §4.1:
//
//	LabeledExtract(salt, label, ikm) = Extract(salt, "HPKE-v1" || suite_id || label || ikm)
//
// Note crypto/hkdf.Extract takes the secret (the IKM) first and the salt
// second, the opposite order from the RFC's notation. An empty salt is
// RFC 5869's zeroed block of hash length, which is what HPKE's "" means.
func labeledExtract(suite, salt []byte, label string, ikm []byte) ([]byte, error) {
	labeledIKM := make([]byte, 0, 7+len(suite)+len(label)+len(ikm))
	labeledIKM = append(labeledIKM, "HPKE-v1"...)
	labeledIKM = append(labeledIKM, suite...)
	labeledIKM = append(labeledIKM, label...)
	labeledIKM = append(labeledIKM, ikm...)
	return hkdf.Extract(sha256.New, labeledIKM, salt)
}

// labeledExpand is RFC 9180 §4.1:
//
//	LabeledExpand(prk, label, info, L) = Expand(prk, I2OSP(L, 2) || "HPKE-v1" || suite_id || label || info, L)
func labeledExpand(suite, prk []byte, label string, info []byte, length int) ([]byte, error) {
	labeledInfo := make([]byte, 0, 2+7+len(suite)+len(label)+len(info))
	labeledInfo = append(labeledInfo, byte(length>>8), byte(length))
	labeledInfo = append(labeledInfo, "HPKE-v1"...)
	labeledInfo = append(labeledInfo, suite...)
	labeledInfo = append(labeledInfo, label...)
	labeledInfo = append(labeledInfo, info...)
	// hkdf.Expand takes info as a string; the labeled info is arbitrary bytes,
	// and a Go string carries arbitrary bytes unchanged.
	return hkdf.Expand(sha256.New, prk, string(labeledInfo), length)
}

// suiteID is RFC 9180 §5.1:
//
//	suite_id = "HPKE" || I2OSP(kem_id, 2) || I2OSP(kdf_id, 2) || I2OSP(aead_id, 2)
func suiteID() []byte {
	return []byte{'H', 'P', 'K', 'E', kemID >> 8, kemID & 0xff, kdfID >> 8, kdfID & 0xff, aeadID >> 8, aeadID & 0xff}
}

// kemSuiteID is RFC 9180 §4.1:
//
//	suite_id = "KEM" || I2OSP(kem_id, 2)
//
// KEM operations use this ID; key-schedule operations use suiteID.
func kemSuiteID() []byte {
	return []byte{'K', 'E', 'M', kemID >> 8, kemID & 0xff}
}
