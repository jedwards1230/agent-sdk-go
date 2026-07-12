package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// providerID is the registry/credential key for OpenAI.
const providerID = "openai"

// Endpoint routing. The credential kind chooses the base URL; both post to
// responsesPath and speak the same SSE shape.
//
// oauthBaseURL and headerAccountID intentionally duplicate auth's exported
// auth.OpenAIChatGPTBaseURL / auth.OpenAIAccountIDHeader as literals: the
// provider must stay importable without depending on the auth package.
const (
	apiBaseURL    = "https://api.openai.com/v1"
	oauthBaseURL  = "https://chatgpt.com/backend-api/codex"
	responsesPath = "/responses"

	headerAccountID = "ChatGPT-Account-ID"
)

// Provider is an OpenAI Responses-API [provider.Provider]. It is safe for
// concurrent use: Stream holds no per-call state on the Provider.
type Provider struct {
	model   string
	creds   provider.CredentialSource
	httpc   *http.Client
	baseURL string // test override; empty derives the base URL from the credential kind
}

// Option configures a [Provider].
type Option func(*Provider)

// WithHTTPClient sets the HTTP client used for requests. A nil client is
// ignored.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) {
		if c != nil {
			p.httpc = c
		}
	}
}

// WithBaseURL overrides the request base URL (for tests against an
// httptest.Server). Credential-kind header logic is preserved; only the host
// is redirected.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Provider that calls model, resolving credentials from creds
// under the provider id "openai".
func New(model string, creds provider.CredentialSource, opts ...Option) *Provider {
	p := &Provider{model: model, creds: creds, httpc: http.DefaultClient}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Info returns the configured model's metadata from the registry, or a minimal
// record naming the provider when the model is unregistered.
func (p *Provider) Info() provider.ModelInfo {
	if m, ok := provider.Lookup(p.model); ok {
		return m
	}
	return provider.ModelInfo{ID: p.model, Provider: providerID}
}

// Stream starts one Responses-API call and returns a handle over the
// normalized event stream.
func (p *Provider) Stream(ctx context.Context, req provider.Request) (provider.StreamHandle, error) {
	cred, err := p.creds.Credential(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("openai: resolve credential: %w", err)
	}

	base, headers, err := p.route(cred)
	if err != nil {
		return nil, err
	}

	model := req.Model
	if model == "" {
		model = p.model
	}
	body, err := buildRequest(model, req, p.reasoningSupported(model))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+responsesPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.httpc.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("openai: request: %w", ctxErr)
		}
		return nil, fmt.Errorf("openai: request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, &APIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	return newStream(ctx, resp.Body), nil
}

// route derives the base URL and auth headers from the credential kind. A
// configured baseURL override replaces the host while preserving the header
// logic, so tests exercise the real oauth-vs-api-key routing.
func (p *Provider) route(cred provider.Credential) (string, map[string]string, error) {
	headers := map[string]string{}
	var base string
	switch cred.Kind {
	case provider.CredOAuth:
		token, account := splitOAuth(cred)
		if token == "" {
			return "", nil, errors.New("openai: empty oauth token")
		}
		base = oauthBaseURL
		headers["Authorization"] = "Bearer " + token
		// The account id populates the ChatGPT-Account-ID header. When it is
		// empty the header is omitted rather than sent blank — and crucially the
		// bearer is never corrupted (see splitOAuth). It becomes reliably present
		// once provider.Credential.Account lands.
		if account != "" {
			headers[headerAccountID] = account
		}
	case provider.CredAPIKey, provider.CredKind(""):
		if cred.Token == "" {
			return "", nil, errors.New("openai: empty api key")
		}
		base = apiBaseURL
		headers["Authorization"] = "Bearer " + cred.Token
	default:
		return "", nil, fmt.Errorf("openai: unsupported credential kind %q", cred.Kind)
	}
	if p.baseURL != "" {
		base = p.baseURL
	}
	return base, headers, nil
}

// reasoningSupported reports whether the model supports reasoning, per the
// registry. Unregistered models are treated as non-reasoning.
func (p *Provider) reasoningSupported(model string) bool {
	m, ok := provider.Lookup(model)
	return ok && m.Reasoning
}

// splitOAuth extracts the bearer token and ChatGPT account id from an oauth
// credential.
//
// End state: return (cred.Token, cred.Account) once provider.Credential gains an
// Account field (landing via sdk-core's loop PR). Until then this is a
// transitional seam. auth.Store carries chatgpt_account_id out of band and does
// NOT pack it into the token, so in practice the token is unpacked and the
// account is empty — it must pass through untouched (never corrupt the bearer).
// A "<account_id>\x00<access_token>" packed form is additionally tolerated so
// tests can exercise the account-present path; nothing emits it in production.
func splitOAuth(cred provider.Credential) (token, account string) {
	if i := strings.IndexByte(cred.Token, 0); i >= 0 {
		return cred.Token[i+1:], cred.Token[:i]
	}
	return cred.Token, ""
}
