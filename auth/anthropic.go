package auth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// anthropicProvider is the provider id for Anthropic (Claude) credentials.
const anthropicProvider = "anthropic"

// Anthropic subscription OAuth endpoints and client. Public, hardcoded values
// for the Claude Pro/Max `claude setup-token`-style PKCE flow, clean-roomed
// from the MIT reference implementations:
//   - opencode's anthropic-auth plugin (ex-machina-co/opencode-anthropic-auth,
//     src/constants.ts): CLIENT_ID, AUTHORIZE_URLS.max, TOKEN_URL,
//     CODE_CALLBACK_URL, OAUTH_SCOPES, REQUIRED_BETAS.
//   - the pi lineage (stencila/rust/auth/src/anthropic.rs "matching pi-mono"):
//     same client id, claude.ai authorize, JSON token exchange + refresh.
//
// platform.claude.com is Anthropic's current host for the token/callback
// endpoints per pi-mono's current source + tests (console.anthropic.com is the
// pre-rebrand alias opencode v0.1.0 used for the same service).
//
// UNVERIFIED AGAINST THE LIVE SERVICE: we never contact real auth in CI, so the
// first real `gofer login anthropic` is the actual test of these hosts. If the
// token exchange 400s, the console.anthropic.com forms
// (console.anthropic.com/v1/oauth/token and .../oauth/code/callback) are the
// documented fallback to try.
const (
	anthropicAuthorizeURL = "https://claude.ai/oauth/authorize"
	anthropicTokenURL     = "https://platform.claude.com/v1/oauth/token"
	anthropicClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	anthropicScopes       = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	// anthropicManualRedirect is the fixed redirect for the code-paste flow.
	anthropicManualRedirect = "https://platform.claude.com/oauth/code/callback"

	// AnthropicBetaOAuth is the anthropic-beta header value a provider adapter
	// must send alongside Authorization: Bearer when authenticating with an
	// OAuth token instead of x-api-key. Exported so provider/anthropic can
	// consume it. (opencode REQUIRED_BETAS[0]; unrelated feature betas such as
	// interleaved-thinking are the adapter's concern, not auth's.)
	AnthropicBetaOAuth = "oauth-2025-04-20"
)

// Adapter header mechanics (for provider/anthropic, wave 2 — recorded here so
// the facts stay with the flow that produced the token; source: pi-mono
// packages/ai/src/api/anthropic-messages.ts, the sole current implementation
// after opencode dropped inline subscription OAuth):
//
//   - An OAuth access token is sent as `Authorization: Bearer <token>`, and
//     `x-api-key` is NOT sent (OAuth tokens contain "sk-ant-oat"; API keys
//     "sk-ant-api" — that substring distinguishes the two credential kinds).
//   - `anthropic-beta` must include `claude-code-20250219,oauth-2025-04-20`
//     (AnthropicBetaOAuth is the load-bearing oauth flag), plus any feature
//     betas the request enables (e.g. interleaved-thinking-2025-05-14).
//   - Identity headers accompany OAuth calls: `user-agent: claude-cli/<ver>`,
//     `x-app: cli`, `anthropic-dangerous-direct-browser-access: true`.
//   - The server expects a forced first system block for OAuth requests:
//     "You are Claude Code, Anthropic's official CLI for Claude." prepended
//     before the real system prompt. Omitting it can get the request rejected.
//
// auth/ owns the flow, token store, refresh, and Credential{Kind: oauth}; the
// header assembly + system-prompt preamble live in the adapter.

// anthropicFlow implements the Anthropic subscription OAuth login. It is a
// code-paste flow: the user authorizes in the browser and pastes back a
// "code#state" string.
type anthropicFlow struct {
	authorizeURL string
	tokenURL     string
	clientID     string
	scopes       string
	redirect     string
}

func newAnthropicFlow() *anthropicFlow {
	return &anthropicFlow{
		authorizeURL: anthropicAuthorizeURL,
		tokenURL:     anthropicTokenURL,
		clientID:     anthropicClientID,
		scopes:       anthropicScopes,
		redirect:     anthropicManualRedirect,
	}
}

func (f *anthropicFlow) provider() string            { return anthropicProvider }
func (f *anthropicFlow) usesCallback() bool          { return false }
func (f *anthropicFlow) callbackListenAddr() string  { return "" }
func (f *anthropicFlow) callbackPath() string        { return "" }
func (f *anthropicFlow) callbackRedirectURI() string { return "" }
func (f *anthropicFlow) manualRedirectURI() string   { return f.redirect }

// authorize builds the Anthropic authorize URL with PKCE. Anthropic's flow has
// a deliberate quirk confirmed in both reference implementations (pi-mono,
// opencode): the OAuth `state` parameter IS the PKCE code_verifier itself, not
// a separate nonce. It is echoed back appended to the code as `code#state` and
// resent in the token-exchange body.
func (f *anthropicFlow) authorize(redirectURI string) (string, pkce, error) {
	p, err := newPKCE(redirectURI)
	if err != nil {
		return "", pkce{}, err
	}
	p.state = p.verifier
	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", f.clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", f.scopes)
	q.Set("code_challenge", p.challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", p.state)
	return f.authorizeURL + "?" + q.Encode(), p, nil
}

// exchange trades the pasted authorization value for an OAuth entry. The user
// may paste a bare `code#state`, or the full redirect URL / query Anthropic
// shows — all yield {code, state}.
func (f *anthropicFlow) exchange(ctx context.Context, hc httpDoer, pasted string, p pkce) (Entry, error) {
	code, state := parseAuthorizationInput(pasted, p.state)
	if state != p.state {
		return Entry{}, fmt.Errorf("auth: anthropic state mismatch")
	}
	body := map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"state":         state,
		"client_id":     f.clientID,
		"redirect_uri":  p.redirectURI,
		"code_verifier": p.verifier,
	}
	tr, err := postJSON(ctx, hc, f.tokenURL, body)
	if err != nil {
		return Entry{}, err
	}
	return anthropicEntry(tr), nil
}

// refresh renews an expired Anthropic OAuth entry.
func (f *anthropicFlow) refresh(ctx context.Context, hc httpDoer, e Entry) (Entry, error) {
	body := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": e.Refresh,
		"client_id":     f.clientID,
	}
	tr, err := postJSON(ctx, hc, f.tokenURL, body)
	if err != nil {
		return Entry{}, err
	}
	out := anthropicEntry(tr)
	if out.Refresh == "" {
		// Some refresh responses omit a new refresh token; keep the old one.
		out.Refresh = e.Refresh
	}
	return out, nil
}

// anthropicEntry projects a token response into a persisted entry. Expiry is
// absolute; the caller's clock is used via time.Now inside expiresAt at call
// time, which is fine for real use (tests drive the store clock, not this).
func anthropicEntry(tr tokenResponse) Entry {
	return Entry{
		Kind:    KindOAuth,
		Access:  tr.AccessToken,
		Refresh: tr.RefreshToken,
		Expires: expiresAtNow(tr.ExpiresIn),
	}
}

// parseAuthorizationInput extracts {code, state} from whatever the user pastes:
// a full redirect URL, a `?code=…&state=…` query, or a bare `code#state`. When
// no state is found, the fallback (the flow's own state) is returned so a
// bare-code paste still validates.
func parseAuthorizationInput(pasted, fallbackState string) (code, state string) {
	pasted = strings.TrimSpace(pasted)
	if strings.Contains(pasted, "://") || strings.HasPrefix(pasted, "?") || strings.Contains(pasted, "code=") {
		if u, err := url.Parse(pasted); err == nil {
			q := u.Query()
			if c := q.Get("code"); c != "" {
				st := q.Get("state")
				if st == "" {
					st = fallbackState
				}
				return c, st
			}
		}
	}
	if i := strings.Index(pasted, "#"); i >= 0 {
		return pasted[:i], pasted[i+1:]
	}
	return pasted, fallbackState
}
