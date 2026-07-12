package auth

import (
	"context"
	"encoding/json"
	"net/url"
)

// openaiProvider is the provider id for OpenAI credentials.
const openaiProvider = "openai"

// OpenAI ChatGPT-subscription (codex) OAuth endpoints and client. Public,
// hardcoded values clean-roomed from the Apache-2.0 openai/codex login crate:
//   - codex-rs/login/src/auth/manager.rs: CLIENT_ID.
//   - codex-rs/login/src/server.rs: DEFAULT_ISSUER, DEFAULT_PORT (1455),
//     redirect_uri "http://localhost:{port}/auth/callback", the authorize
//     query (scope, response_type, S256, id_token_add_organizations,
//     codex_cli_simplified_flow, originator), and the form-encoded
//     authorization_code / refresh_token exchanges against {issuer}/oauth/token.
const (
	openaiAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	openaiTokenURL     = "https://auth.openai.com/oauth/token"
	openaiClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	openaiScopes       = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	openaiCallbackAddr = "localhost:1455"
	openaiCallbackPath = "/auth/callback"
	openaiRedirectURI  = "http://localhost:1455/auth/callback"

	// openaiAccountIDKey is the Extra key holding the chatgpt account id a
	// provider adapter sends to the ChatGPT backend.
	openaiAccountIDKey = "chatgpt_account_id"
	// openaiIDTokenKey stores the id_token for later introspection.
	openaiIDTokenKey = "id_token"

	// OpenAIChatGPTBaseURL is the base URL for ChatGPT-subscription (OAuth)
	// requests — the Responses API lives at OpenAIChatGPTBaseURL+"/responses"
	// (codex-rs/http-client). Contrast the platform API (api.openai.com) used
	// with an API key. Exported for provider/openai.
	OpenAIChatGPTBaseURL = "https://chatgpt.com/backend-api/codex"
	// OpenAIAccountIDHeader carries the ChatGPT account id on subscription
	// requests (codex sends it as "ChatGPT-Account-ID"; HTTP header names are
	// case-insensitive).
	OpenAIAccountIDHeader = "ChatGPT-Account-ID"
)

// openaiFlow implements the OpenAI ChatGPT-subscription OAuth login via a local
// callback listener.
type openaiFlow struct {
	authorizeURL string
	tokenURL     string
	clientID     string
	scopes       string
	listenAddr   string
	cbPath       string
	redirectURI  string
}

func newOpenAIFlow() *openaiFlow {
	return &openaiFlow{
		authorizeURL: openaiAuthorizeURL,
		tokenURL:     openaiTokenURL,
		clientID:     openaiClientID,
		scopes:       openaiScopes,
		listenAddr:   openaiCallbackAddr,
		cbPath:       openaiCallbackPath,
		redirectURI:  openaiRedirectURI,
	}
}

func (f *openaiFlow) provider() string            { return openaiProvider }
func (f *openaiFlow) usesCallback() bool          { return true }
func (f *openaiFlow) callbackListenAddr() string  { return f.listenAddr }
func (f *openaiFlow) callbackPath() string        { return f.cbPath }
func (f *openaiFlow) callbackRedirectURI() string { return f.redirectURI }
func (f *openaiFlow) manualRedirectURI() string   { return "" }

// authorize builds the OpenAI authorize URL with PKCE and the codex CLI's
// extra parameters.
func (f *openaiFlow) authorize(redirectURI string) (string, pkce, error) {
	p, err := newPKCE(redirectURI)
	if err != nil {
		return "", pkce{}, err
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", f.clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", f.scopes)
	q.Set("code_challenge", p.challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", p.state)
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", "codex_cli_rs")
	return f.authorizeURL + "?" + q.Encode(), p, nil
}

// exchange trades the callback code for an OAuth entry, extracting the ChatGPT
// account id from the id_token claims.
func (f *openaiFlow) exchange(ctx context.Context, hc httpDoer, code string, p pkce) (Entry, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", p.redirectURI)
	form.Set("client_id", f.clientID)
	form.Set("code_verifier", p.verifier)
	tr, err := postForm(ctx, hc, f.tokenURL, form)
	if err != nil {
		return Entry{}, err
	}
	return openaiEntry(tr), nil
}

// refresh renews an expired OpenAI OAuth entry. Unlike the code exchange
// (form-encoded), codex's refresh is a JSON body carrying exactly client_id,
// grant_type, and refresh_token — no scope (codex-rs/login/src/auth/manager.rs
// RefreshRequest). All response fields are optional, so previously-known
// values are preserved when the response omits them.
func (f *openaiFlow) refresh(ctx context.Context, hc httpDoer, e Entry) (Entry, error) {
	body := map[string]string{
		"client_id":     f.clientID,
		"grant_type":    "refresh_token",
		"refresh_token": e.Refresh,
	}
	tr, err := postJSON(ctx, hc, f.tokenURL, body)
	if err != nil {
		return Entry{}, err
	}
	out := openaiEntry(tr)
	if out.Refresh == "" {
		out.Refresh = e.Refresh
	}
	// Preserve a previously-derived account id / expiry if the refresh omits
	// the id_token they came from.
	if out.Extra[openaiAccountIDKey] == "" && e.Extra[openaiAccountIDKey] != "" {
		if out.Extra == nil {
			out.Extra = map[string]string{}
		}
		out.Extra[openaiAccountIDKey] = e.Extra[openaiAccountIDKey]
	}
	if out.Expires == 0 {
		out.Expires = e.Expires
	}
	return out, nil
}

// openaiEntry projects a token response into a persisted entry. Codex's token
// endpoint returns no expires_in, so expiry is read from the id_token's `exp`
// claim (falling back to the access_token if it too is a JWT, then to any
// expires_in the server did send). The ChatGPT account id is pulled from the
// id_token when present.
func openaiEntry(tr tokenResponse) Entry {
	e := Entry{
		Kind:    KindOAuth,
		Access:  tr.AccessToken,
		Refresh: tr.RefreshToken,
		Expires: openaiExpiry(tr),
		Extra:   map[string]string{},
	}
	if tr.IDToken != "" {
		e.Extra[openaiIDTokenKey] = tr.IDToken
		if acct := accountIDFromIDToken(tr.IDToken); acct != "" {
			e.Extra[openaiAccountIDKey] = acct
		}
	}
	if len(e.Extra) == 0 {
		e.Extra = nil
	}
	return e
}

// openaiExpiry resolves the absolute unix-second expiry for a codex token
// response.
func openaiExpiry(tr tokenResponse) int64 {
	if exp := jwtExpUnix(tr.IDToken); exp > 0 {
		return exp
	}
	if exp := jwtExpUnix(tr.AccessToken); exp > 0 {
		return exp
	}
	return expiresAtNow(tr.ExpiresIn)
}

// accountIDFromIDToken returns the ChatGPT account id nested under the OpenAI
// auth claim of an id_token, if present.
func accountIDFromIDToken(idToken string) string {
	payload := jwtPayload(idToken)
	if payload == nil {
		return ""
	}
	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Auth.ChatGPTAccountID
}
