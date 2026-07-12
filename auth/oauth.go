package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpDoer is the subset of *http.Client the flows use, so tests can inject a
// fake transport or an httptest.Server-backed client.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// defaultHTTPClient returns the client used for token exchange when the caller
// supplies none. A bounded timeout keeps a hung auth server from wedging a
// login or a mid-request refresh.
func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// pkce carries one login attempt's PKCE material and the redirect it was built
// with. It is opaque to callers and threaded from authorize to exchange.
type pkce struct {
	verifier    string
	challenge   string
	state       string
	redirectURI string
}

// newPKCE generates a fresh PKCE verifier/challenge (S256) and an anti-forgery
// state value for the given redirect URI.
func newPKCE(redirectURI string) (pkce, error) {
	verifier, err := randomURLSafe(32)
	if err != nil {
		return pkce{}, err
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return pkce{}, err
	}
	return pkce{
		verifier:    verifier,
		challenge:   s256Challenge(verifier),
		state:       state,
		redirectURI: redirectURI,
	}, nil
}

// s256Challenge is the PKCE S256 code challenge for a verifier.
func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomURLSafe returns n random bytes as an unpadded base64url string.
func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: read randomness: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// tokenResponse is the union of fields the vendors return from the token and
// refresh endpoints. Unused fields stay zero.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// postForm posts application/x-www-form-urlencoded values to a token endpoint
// and decodes the JSON token response.
func postForm(ctx context.Context, hc httpDoer, endpoint string, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("auth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return doToken(hc, req)
}

// postJSON posts a JSON body to a token endpoint and decodes the JSON token
// response.
func postJSON(ctx context.Context, hc httpDoer, endpoint string, body any) (tokenResponse, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("auth: encode token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(b)))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("auth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return doToken(hc, req)
}

// doToken executes a token request and decodes the response, surfacing a
// non-2xx status with a bounded snippet of the body for diagnosis (the body
// may contain an error description but never a usable secret).
func doToken(hc httpDoer, req *http.Request) (tokenResponse, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("auth: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("auth: read token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("auth: token endpoint status %d: %s", resp.StatusCode, snippet(body))
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return tokenResponse{}, fmt.Errorf("auth: decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("auth: token response missing access_token")
	}
	return tr, nil
}

// snippet trims a response body to a short single line for error messages.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// expiresAt converts an expires_in (seconds from now) into a unix-second
// absolute expiry, using now for testability. A zero or negative expires_in
// yields 0 (no expiry recorded).
func expiresAt(now time.Time, expiresIn int64) int64 {
	if expiresIn <= 0 {
		return 0
	}
	return now.Add(time.Duration(expiresIn) * time.Second).Unix()
}

// expiresAtNow is expiresAt against the wall clock, used when projecting a
// fresh token response into an entry.
func expiresAtNow(expiresIn int64) int64 { return expiresAt(time.Now(), expiresIn) }

// jwtPayload base64url-decodes a JWT's payload segment (middle of the three
// dot-separated parts) without verifying the signature — the token arrives
// straight from the token endpoint over TLS, so it is already trusted. Returns
// nil for anything that is not a well-formed three-segment JWT.
func jwtPayload(token string) []byte {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	return payload
}

// jwtExpUnix returns the `exp` (unix seconds) claim of a JWT, or 0 if the token
// is not a JWT or carries no numeric exp.
func jwtExpUnix(token string) int64 {
	payload := jwtPayload(token)
	if payload == nil {
		return 0
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return 0
	}
	return claims.Exp
}
