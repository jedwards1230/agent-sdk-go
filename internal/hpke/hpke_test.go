package hpke

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// vectorFile is the CFRG HPKE test-vector set (published as RFC 9180), filtered
// to mode_auth over DHKEM(X25519, HKDF-SHA256). Its values are authoritative:
// if an assertion below fails, this implementation is wrong. Never edit the
// file to make a test pass.
const vectorFile = "testdata/rfc9180-auth-x25519.json"

// kemVector is one DHKEM(X25519) mode_auth vector. shared_secret depends only
// on the KEM, so every kdf/aead combination in the file exercises authEncap and
// authDecap identically.
type kemVector struct {
	Mode         int    `json:"mode"`
	KEMID        int    `json:"kem_id"`
	KDFID        int    `json:"kdf_id"`
	AEADID       int    `json:"aead_id"`
	SkEm         string `json:"skEm"`
	SkRm         string `json:"skRm"`
	SkSm         string `json:"skSm"`
	PkEm         string `json:"pkEm"`
	PkRm         string `json:"pkRm"`
	PkSm         string `json:"pkSm"`
	Enc          string `json:"enc"`
	SharedSecret string `json:"shared_secret"`
}

// fullVector is the single vector for the exact ciphersuite in use, carrying
// every key-schedule intermediate so a mismatch names the failing step.
type fullVector struct {
	kemVector
	Info               string `json:"info"`
	KeyScheduleContext string `json:"key_schedule_context"`
	Secret             string `json:"secret"`
	Key                string `json:"key"`
	BaseNonce          string `json:"base_nonce"`
	ExporterSecret     string `json:"exporter_secret"`
	Encryptions        []struct {
		AAD   string `json:"aad"`
		CT    string `json:"ct"`
		Nonce string `json:"nonce"`
		PT    string `json:"pt"`
	} `json:"encryptions"`
	Exports []struct {
		ExporterContext string `json:"exporter_context"`
		L               int    `json:"L"`
		ExportedValue   string `json:"exported_value"`
	} `json:"exports"`
}

type vectors struct {
	Source      string       `json:"_source"`
	KEMVectors  []kemVector  `json:"kem_vectors"`
	FullVectors []fullVector `json:"full_vectors"`
}

// loadVectors reads the vector file, checking that it still describes the mode
// and KEM this package implements — a swapped or re-filtered file must not
// silently weaken the tests below.
func loadVectors(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile(vectorFile)
	if err != nil {
		t.Fatalf("read %s: %v", vectorFile, err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("parse %s: %v", vectorFile, err)
	}
	if v.Source == "" {
		t.Fatalf("%s: missing _source provenance", vectorFile)
	}
	if len(v.KEMVectors) == 0 || len(v.FullVectors) == 0 {
		t.Fatalf("%s: got %d kem and %d full vectors, want at least 1 of each", vectorFile, len(v.KEMVectors), len(v.FullVectors))
	}
	all := append([]kemVector(nil), v.KEMVectors...)
	for _, f := range v.FullVectors {
		all = append(all, f.kemVector)
	}
	for i, kv := range all {
		if kv.Mode != modeAuth || kv.KEMID != kemID {
			t.Fatalf("%s: vector %d is mode %d kem %d, want mode %d kem %d", vectorFile, i, kv.Mode, kv.KEMID, modeAuth, kemID)
		}
	}
	return v
}

// unhex decodes a vector's hex field.
func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex %q: %v", s, err)
	}
	return b
}

// TestAuthEncapMatchesVectors drives authEncap deterministically by feeding the
// vector's skEm through the rand seam, and checks both that crypto/ecdh's
// clamping reproduces the vector's pkEm and that the derived shared secret
// matches.
func TestAuthEncapMatchesVectors(t *testing.T) {
	v := loadVectors(t)
	for i, kv := range v.KEMVectors {
		t.Run(kv.Enc[:8], func(t *testing.T) {
			enc, shared, err := authEncap(bytes.NewReader(unhex(t, kv.SkEm)), unhex(t, kv.PkRm), unhex(t, kv.SkSm))
			if err != nil {
				t.Fatalf("authEncap: %v", err)
			}
			if got, want := hex.EncodeToString(enc), kv.Enc; got != want {
				t.Errorf("vector %d: enc = %s, want %s", i, got, want)
			}
			if got, want := hex.EncodeToString(enc), kv.PkEm; got != want {
				t.Errorf("vector %d: enc = %s, want pkEm %s (crypto/ecdh clamped the scalar differently than RFC 7748)", i, got, want)
			}
			if got, want := hex.EncodeToString(shared), kv.SharedSecret; got != want {
				t.Errorf("vector %d: shared_secret = %s, want %s", i, got, want)
			}
		})
	}
}

// TestAuthDecapMatchesVectors checks the mirror direction: the recipient
// reproduces the same shared secret from enc, its own private key, and the
// sender's public key.
func TestAuthDecapMatchesVectors(t *testing.T) {
	v := loadVectors(t)
	for i, kv := range v.KEMVectors {
		t.Run(kv.Enc[:8], func(t *testing.T) {
			shared, err := authDecap(unhex(t, kv.Enc), unhex(t, kv.SkRm), unhex(t, kv.PkSm))
			if err != nil {
				t.Fatalf("authDecap: %v", err)
			}
			if got, want := hex.EncodeToString(shared), kv.SharedSecret; got != want {
				t.Errorf("vector %d: shared_secret = %s, want %s", i, got, want)
			}
		})
	}
}

// TestAuthDecapRejectsWrongSender is the authentication property: the same
// encapsulation decapsulated against a different sender public key must not
// reproduce the shared secret.
func TestAuthDecapRejectsWrongSender(t *testing.T) {
	v := loadVectors(t)
	kv := v.KEMVectors[0]
	other := v.KEMVectors[1]
	shared, err := authDecap(unhex(t, kv.Enc), unhex(t, kv.SkRm), unhex(t, other.PkSm))
	if err != nil {
		t.Fatalf("authDecap: %v", err)
	}
	if got := hex.EncodeToString(shared); got == kv.SharedSecret {
		t.Error("a substituted sender public key reproduced the shared secret")
	}
}

// TestKeyScheduleMatchesVector asserts every published key-schedule
// intermediate, not just the final key: key_schedule_context and secret are
// recomputed here from the same helpers the schedule uses, so a mismatch points
// at the exact step.
func TestKeyScheduleMatchesVector(t *testing.T) {
	fv := loadVectors(t).FullVectors[0]
	if fv.KDFID != kdfID || fv.AEADID != aeadID {
		t.Fatalf("full vector is kdf %d aead %d, want %d/%d", fv.KDFID, fv.AEADID, kdfID, aeadID)
	}
	shared, info := unhex(t, fv.SharedSecret), unhex(t, fv.Info)
	suite := suiteID()

	pskIDHash, err := labeledExtract(suite, nil, "psk_id_hash", nil)
	if err != nil {
		t.Fatalf("labeledExtract psk_id_hash: %v", err)
	}
	infoHash, err := labeledExtract(suite, nil, "info_hash", info)
	if err != nil {
		t.Fatalf("labeledExtract info_hash: %v", err)
	}
	// Built the way the production code builds it — a fresh slice appended to,
	// never `append(pskIDHash, ...)`, which can write through pskIDHash's
	// backing array. Harmless at this call site, but this is exactly the
	// aliasing pattern keySchedule deliberately avoids, and a test that models
	// the code differently is one refactor away from proving the wrong thing.
	ksContext := make([]byte, 0, 1+len(pskIDHash)+len(infoHash))
	ksContext = append(ksContext, modeAuth)
	ksContext = append(ksContext, pskIDHash...)
	ksContext = append(ksContext, infoHash...)
	if got, want := hex.EncodeToString(ksContext), fv.KeyScheduleContext; got != want {
		t.Errorf("key_schedule_context = %s, want %s", got, want)
	}

	secret, err := labeledExtract(suite, shared, "secret", nil)
	if err != nil {
		t.Fatalf("labeledExtract secret: %v", err)
	}
	if got, want := hex.EncodeToString(secret), fv.Secret; got != want {
		t.Errorf("secret = %s, want %s", got, want)
	}

	key, baseNonce, exporterSecret, err := keySchedule(shared, info)
	if err != nil {
		t.Fatalf("keySchedule: %v", err)
	}
	for _, tt := range []struct {
		name string
		got  []byte
		want string
	}{
		{name: "key", got: key, want: fv.Key},
		{name: "base_nonce", got: baseNonce, want: fv.BaseNonce},
		{name: "exporter_secret", got: exporterSecret, want: fv.ExporterSecret},
	} {
		if got := hex.EncodeToString(tt.got); got != tt.want {
			t.Errorf("%s = %s, want %s", tt.name, got, tt.want)
		}
	}

	// The sequence-0 nonce is the base nonce; the vector's first encryption
	// confirms it, and its second and third confirm the sequence advances (which
	// this single-shot package deliberately does not implement).
	if got, want := hex.EncodeToString(baseNonce), fv.Encryptions[0].Nonce; got != want {
		t.Errorf("seq-0 nonce = %s, want %s", got, want)
	}

	// Exports are LabeledExpand(exporter_secret, "sec", context, L) under the
	// HPKE suite ID; asserting them checks the exporter secret is not merely
	// self-consistent.
	for i, ex := range fv.Exports {
		got, err := labeledExpand(suite, exporterSecret, "sec", unhex(t, ex.ExporterContext), ex.L)
		if err != nil {
			t.Fatalf("export %d: %v", i, err)
		}
		if hex.EncodeToString(got) != ex.ExportedValue {
			t.Errorf("export %d = %s, want %s", i, hex.EncodeToString(got), ex.ExportedValue)
		}
	}
}

// TestSealAuthMatchesVector runs the exported single-shot path end to end
// against the published ciphertext, with the ephemeral pinned to the vector's
// skEm through the rand seam.
func TestSealAuthMatchesVector(t *testing.T) {
	fv := loadVectors(t).FullVectors[0]
	first := fv.Encryptions[0]
	aad := unhex(t, first.AAD)

	enc, ct, err := SealAuth(
		bytes.NewReader(unhex(t, fv.SkEm)),
		unhex(t, fv.PkRm),
		unhex(t, fv.SkSm),
		unhex(t, fv.Info),
		func([]byte) []byte { return aad },
		unhex(t, first.PT),
	)
	if err != nil {
		t.Fatalf("SealAuth: %v", err)
	}
	if got, want := hex.EncodeToString(enc), fv.Enc; got != want {
		t.Errorf("enc = %s, want %s", got, want)
	}
	if got, want := hex.EncodeToString(ct), first.CT; got != want {
		t.Errorf("ciphertext = %s, want %s", got, want)
	}
}

// TestOpenAuthMatchesVector opens the published sequence-0 ciphertext, and
// confirms the later-sequence ciphertexts do NOT open at sequence 0 — this
// package seals exactly one message per encapsulation.
func TestOpenAuthMatchesVector(t *testing.T) {
	fv := loadVectors(t).FullVectors[0]
	skRm, pkSm, enc, info := unhex(t, fv.SkRm), unhex(t, fv.PkSm), unhex(t, fv.Enc), unhex(t, fv.Info)

	for i, e := range fv.Encryptions {
		t.Run(e.Nonce, func(t *testing.T) {
			pt, err := OpenAuth(skRm, pkSm, enc, info, unhex(t, e.AAD), unhex(t, e.CT))
			if i == 0 {
				if err != nil {
					t.Fatalf("OpenAuth: %v", err)
				}
				if got, want := hex.EncodeToString(pt), e.PT; got != want {
					t.Errorf("plaintext = %s, want %s", got, want)
				}
				return
			}
			if !errors.Is(err, ErrOpen) {
				t.Fatalf("OpenAuth of sequence %d at sequence 0: err = %v, want %v", i, err, ErrOpen)
			}
			if pt != nil {
				t.Errorf("OpenAuth returned plaintext %x alongside an error", pt)
			}
		})
	}
}

func TestSealAuthOpenAuthRoundTrip(t *testing.T) {
	recipient, sender := newKey(t), newKey(t)
	info, aad := []byte("info"), []byte("aad")
	plaintext := []byte("session content")

	enc, ct, err := SealAuth(rand.Reader, recipient.pub, sender.priv, info, func([]byte) []byte { return aad }, plaintext)
	if err != nil {
		t.Fatalf("SealAuth: %v", err)
	}
	got, err := OpenAuth(recipient.priv, sender.pub, enc, info, aad, ct)
	if err != nil {
		t.Fatalf("OpenAuth: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("plaintext = %q, want %q", got, plaintext)
	}
}

func TestOpenAuthRejectsMismatch(t *testing.T) {
	recipient, sender, other := newKey(t), newKey(t), newKey(t)
	info, aad := []byte("info"), []byte("aad")

	enc, ct, err := SealAuth(rand.Reader, recipient.pub, sender.priv, info, func([]byte) []byte { return aad }, []byte("secret"))
	if err != nil {
		t.Fatalf("SealAuth: %v", err)
	}

	tests := []struct {
		name    string
		open    func() ([]byte, error)
		wantErr error
	}{
		{
			name: "tampered ciphertext",
			open: func() ([]byte, error) {
				bad := bytes.Clone(ct)
				bad[0] ^= 1
				return OpenAuth(recipient.priv, sender.pub, enc, info, aad, bad)
			},
			wantErr: ErrOpen,
		},
		{
			name:    "tampered aad",
			open:    func() ([]byte, error) { return OpenAuth(recipient.priv, sender.pub, enc, info, []byte("aae"), ct) },
			wantErr: ErrOpen,
		},
		{
			name:    "different info",
			open:    func() ([]byte, error) { return OpenAuth(recipient.priv, sender.pub, enc, []byte("other"), aad, ct) },
			wantErr: ErrOpen,
		},
		{
			name:    "substituted sender",
			open:    func() ([]byte, error) { return OpenAuth(recipient.priv, other.pub, enc, info, aad, ct) },
			wantErr: ErrOpen,
		},
		{
			name:    "wrong recipient",
			open:    func() ([]byte, error) { return OpenAuth(other.priv, sender.pub, enc, info, aad, ct) },
			wantErr: ErrOpen,
		},
		{
			name:    "substituted encapsulation",
			open:    func() ([]byte, error) { return OpenAuth(recipient.priv, sender.pub, other.pub, info, aad, ct) },
			wantErr: ErrOpen,
		},
		{
			name:    "short encapsulation",
			open:    func() ([]byte, error) { return OpenAuth(recipient.priv, sender.pub, enc[:KeySize-1], info, aad, ct) },
			wantErr: ErrKeySize,
		},
		{
			name:    "short recipient key",
			open:    func() ([]byte, error) { return OpenAuth(recipient.priv[:1], sender.pub, enc, info, aad, ct) },
			wantErr: ErrKeySize,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pt, err := tt.open()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if pt != nil {
				t.Errorf("returned plaintext %x alongside an error", pt)
			}
		})
	}
}

// TestOpenAuthCollapsesPostValidationFailuresToErrOpen is the gate on OpenAuth's
// error contract: everything that fails AFTER the length check must be exactly
// ErrOpen, so the package is not a decryption oracle.
//
// The degenerate keys are the cases that used to escape. An all-zero enc or an
// all-zero senderPub is the right LENGTH, so it sails past validation and dies
// inside crypto/ecdh, which rejects low-order points per RFC 7748 §6.1. Before
// this contract was enforced, that surfaced verbatim as
//
//	hpke: decap: ephemeral dh: crypto/ecdh: bad X25519 remote ECDH input: low order point
//
// — not errors.Is(err, ErrOpen), and a distinguishable answer to bytes an
// attacker chose.
func TestOpenAuthCollapsesPostValidationFailuresToErrOpen(t *testing.T) {
	recipient, sender := newKey(t), newKey(t)
	info, aad := []byte("info"), []byte("aad")

	enc, ct, err := SealAuth(rand.Reader, recipient.pub, sender.priv, info, func([]byte) []byte { return aad }, []byte("secret"))
	if err != nil {
		t.Fatalf("SealAuth: %v", err)
	}

	zero := make([]byte, KeySize)
	tests := []struct {
		name string
		open func() ([]byte, error)
	}{
		{
			name: "all-zero encapsulation",
			open: func() ([]byte, error) { return OpenAuth(recipient.priv, sender.pub, zero, info, aad, ct) },
		},
		{
			name: "all-zero sender key",
			open: func() ([]byte, error) { return OpenAuth(recipient.priv, zero, enc, info, aad, ct) },
		},
		{
			name: "all-zero recipient key",
			open: func() ([]byte, error) { return OpenAuth(zero, sender.pub, enc, info, aad, ct) },
		},
		{
			name: "all-zero everything",
			open: func() ([]byte, error) { return OpenAuth(zero, zero, zero, info, aad, ct) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pt, err := tt.open()
			if !errors.Is(err, ErrOpen) {
				t.Fatalf("err = %v, want %v", err, ErrOpen)
			}
			// Nothing more than ErrOpen: a wrapped cause would still satisfy
			// errors.Is while leaking which step failed into the message.
			if err.Error() != ErrOpen.Error() {
				t.Errorf("err message = %q, want exactly %q — no cause may ride along", err, ErrOpen)
			}
			if pt != nil {
				t.Errorf("returned plaintext %x alongside an error", pt)
			}
		})
	}

	// The structural half of the contract stays distinct and stays reachable:
	// a wrong LENGTH is still ErrKeySize, checked before any of the above.
	if _, err := OpenAuth(recipient.priv, sender.pub, enc[:KeySize-1], info, aad, ct); !errors.Is(err, ErrKeySize) {
		t.Errorf("short encapsulation: err = %v, want %v", err, ErrKeySize)
	}
}

func TestSealAuthRejectsBadInput(t *testing.T) {
	recipient, sender := newKey(t), newKey(t)
	errNoEntropy := errors.New("no entropy")

	tests := []struct {
		name       string
		rand       *stubReader
		recipient  []byte
		senderPriv []byte
		wantErr    error
	}{
		{name: "short recipient key", rand: &stubReader{data: bytes.Repeat([]byte{1}, KeySize)}, recipient: recipient.pub[:1], senderPriv: sender.priv, wantErr: ErrKeySize},
		{name: "short sender key", rand: &stubReader{data: bytes.Repeat([]byte{1}, KeySize)}, recipient: recipient.pub, senderPriv: sender.priv[:1], wantErr: ErrKeySize},
		{name: "failing rand", rand: &stubReader{err: errNoEntropy}, recipient: recipient.pub, senderPriv: sender.priv, wantErr: errNoEntropy},
		{name: "short rand", rand: &stubReader{data: bytes.Repeat([]byte{1}, KeySize-1), err: errNoEntropy}, recipient: recipient.pub, senderPriv: sender.priv, wantErr: errNoEntropy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, ct, err := SealAuth(tt.rand, tt.recipient, tt.senderPriv, nil, nil, []byte("payload"))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if enc != nil || ct != nil {
				t.Errorf("SealAuth returned material alongside an error: enc=%x ct=%x", enc, ct)
			}
		})
	}
}

// TestSuiteIDs pins the two suite identifiers RFC 9180 §4.1 and §5.1 define, so
// a change to a ciphersuite constant cannot silently alter every derivation.
func TestSuiteIDs(t *testing.T) {
	if got, want := hex.EncodeToString(kemSuiteID()), "4b454d0020"; got != want { // "KEM" || 0x0020
		t.Errorf("kemSuiteID = %s, want %s", got, want)
	}
	if got, want := hex.EncodeToString(suiteID()), "48504b45002000010003"; got != want { // "HPKE" || 0x0020 || 0x0001 || 0x0003
		t.Errorf("suiteID = %s, want %s", got, want)
	}
}

// stubReader hands out data and then reports err, so a test can exercise a
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
		return 0, errors.New("stub reader exhausted")
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// testKey is a freshly generated X25519 keypair in raw byte form.
type testKey struct{ priv, pub []byte }

func newKey(t *testing.T) testKey {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return testKey{priv: k.Bytes(), pub: k.PublicKey().Bytes()}
}
