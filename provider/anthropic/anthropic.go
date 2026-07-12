// Package anthropic implements the provider.Provider interface against the
// Anthropic Messages API. It streams Server-Sent Events into the normalized
// provider stream union (text/reasoning deltas, tool-call start/delta/end, and
// a terminal finished event carrying the stop reason and usage), and supports
// both credential kinds: an API key (x-api-key) and a subscription OAuth bearer
// token (the Claude Code header convention).
package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// providerID is the credential-source key and ModelInfo.Provider value.
const providerID = "anthropic"

// Default endpoint and protocol constants.
const (
	defaultBaseURL    = "https://api.anthropic.com"
	messagesPath      = "/v1/messages"
	anthropicVersion  = "2023-06-01"
	defaultMaxTokens  = 4096
	defaultCLIVersion = "1.0.60"

	// systemIdentity is the forced first system block the Claude Code CLI
	// prepends when authenticating with a subscription OAuth token. It must be
	// sent verbatim — the OAuth grant is scoped to this client identity.
	systemIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

	// metaSignatureKey is the provider-namespaced ContentBlock.Meta key under
	// which a reasoning block's Anthropic thinking signature is carried. The API
	// requires this signature replayed when extended thinking and tool use
	// combine across turns.
	metaSignatureKey = "anthropic.signature"
)

// oauthBetas are the anthropic-beta feature flags the Claude Code CLI sends with
// OAuth credentials. Any caller-configured betas are appended after these.
//
// These literals intentionally duplicate the auth package's OAuth header
// material rather than importing it: the adapter stays decoupled from auth/ and
// depends only on the provider.CredentialSource contract.
var oauthBetas = []string{"claude-code-20250219", "oauth-2025-04-20"}

// Provider is an Anthropic Messages API backend for a single model. It is safe
// for concurrent use; each Stream call is independent.
type Provider struct {
	model      string
	creds      provider.CredentialSource
	httpClient *http.Client
	baseURL    string
	version    string
	cliVersion string
	betas      []string
}

// Option configures a [Provider].
type Option func(*Provider)

// WithHTTPClient sets the HTTP client used for requests. The default is
// http.DefaultClient. A per-request context governs cancellation regardless of
// the client's own timeout, so callers streaming long turns should avoid a
// short client Timeout.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.httpClient = c }
}

// WithBaseURL overrides the API base URL (no trailing slash). Intended for
// tests and proxies.
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(url, "/") }
}

// WithBetas appends additional anthropic-beta feature flags to every request
// (e.g. prompt-caching or interleaved-thinking betas). For OAuth requests they
// are appended after the mandatory Claude Code betas.
func WithBetas(betas ...string) Option {
	return func(p *Provider) { p.betas = append(p.betas, betas...) }
}

// WithCLIVersion sets the claude-cli version reported in the user-agent header
// for OAuth requests.
func WithCLIVersion(v string) Option {
	return func(p *Provider) { p.cliVersion = v }
}

// New returns a Provider for the given model, resolving credentials from creds
// under the "anthropic" provider id.
func New(model string, creds provider.CredentialSource, opts ...Option) *Provider {
	p := &Provider{
		model:      model,
		creds:      creds,
		httpClient: http.DefaultClient,
		baseURL:    defaultBaseURL,
		version:    anthropicVersion,
		cliVersion: defaultCLIVersion,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Info returns the model metadata from the shared registry, or a minimal record
// (id + provider) when the model is not registered.
func (p *Provider) Info() provider.ModelInfo {
	if info, ok := provider.Lookup(p.model); ok {
		return info
	}
	return provider.ModelInfo{ID: p.model, Provider: providerID}
}

// Stream starts one Messages API call and returns a handle over the normalized
// event stream. The request context governs the whole stream: cancelling it
// aborts the in-flight read and surfaces a terminal finished event with
// StopReason "cancelled". The caller must Close the returned handle.
func (p *Provider) Stream(ctx context.Context, req provider.Request) (provider.StreamHandle, error) {
	cred, err := p.creds.Credential(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("anthropic: resolve credential: %w", err)
	}

	body, err := p.buildBody(req, cred.Kind)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+messagesPath, body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: new request: %w", err)
	}
	p.applyHeaders(httpReq, cred)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: do request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiError(resp)
	}

	return newStreamHandle(ctx, resp.Body), nil
}

// applyHeaders sets the auth and protocol headers for the credential kind.
// OAuth uses a bearer token with the Claude Code header convention and never
// sends x-api-key; API keys use x-api-key.
func (p *Provider) applyHeaders(req *http.Request, cred provider.Credential) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", p.version)

	if cred.Kind == provider.CredOAuth {
		req.Header.Set("Authorization", "Bearer "+cred.Token)
		req.Header.Set("Anthropic-Beta", strings.Join(append(append([]string{}, oauthBetas...), p.betas...), ","))
		req.Header.Set("User-Agent", "claude-cli/"+p.cliVersion)
		req.Header.Set("X-App", "cli")
		return
	}

	req.Header.Set("X-Api-Key", cred.Token)
	if len(p.betas) > 0 {
		req.Header.Set("Anthropic-Beta", strings.Join(p.betas, ","))
	}
}
