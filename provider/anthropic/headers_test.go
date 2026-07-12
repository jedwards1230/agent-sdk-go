package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// captureHandler records the request headers and body, then replays textTurn.
type capture struct {
	header http.Header
	body   messagesRequest
}

func captureProvider(t *testing.T, kind provider.CredKind, opts ...Option) (*Provider, *capture) {
	t.Helper()
	cap := &capture{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		cap.header = r.Header.Clone()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &cap.body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, textTurn)
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	cred := provider.StaticCredentialSource{Cred: provider.Credential{Kind: kind, Token: token(kind)}}
	allOpts := append([]Option{WithBaseURL(srv.URL)}, opts...)
	return New("claude-sonnet-5", cred, allOpts...), cap
}

func TestHeadersAPIKey(t *testing.T) {
	p, cap := captureProvider(t, provider.CredAPIKey)
	h, err := p.Stream(context.Background(), provider.Request{
		System:   "sys",
		Messages: []provider.Message{provider.UserText("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(t, h)
	_ = h.Close()

	if got := cap.header.Get("X-Api-Key"); got != token(provider.CredAPIKey) {
		t.Errorf("x-api-key = %q, want the api key", got)
	}
	if got := cap.header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty for api-key auth", got)
	}
	if got := cap.header.Get("Anthropic-Version"); got != anthropicVersion {
		t.Errorf("anthropic-version = %q", got)
	}
	// No OAuth-only headers.
	if cap.header.Get("X-App") != "" || cap.header.Get("Anthropic-Beta") != "" {
		t.Errorf("api-key request leaked oauth headers: x-app=%q beta=%q",
			cap.header.Get("X-App"), cap.header.Get("Anthropic-Beta"))
	}
	// No forced identity block for api-key auth.
	if len(cap.body.System) != 1 || cap.body.System[0].Text != "sys" {
		t.Errorf("system = %+v, want single caller block", cap.body.System)
	}
}

func TestHeadersOAuth(t *testing.T) {
	p, cap := captureProvider(t, provider.CredOAuth)
	h, err := p.Stream(context.Background(), provider.Request{
		System:   "sys",
		Messages: []provider.Message{provider.UserText("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(t, h)
	_ = h.Close()

	if got := cap.header.Get("Authorization"); got != "Bearer "+token(provider.CredOAuth) {
		t.Errorf("Authorization = %q, want bearer oauth token", got)
	}
	// x-api-key must NEVER be sent with OAuth.
	if got := cap.header.Get("X-Api-Key"); got != "" {
		t.Errorf("x-api-key = %q, must be absent for oauth", got)
	}
	beta := cap.header.Get("Anthropic-Beta")
	for _, want := range oauthBetas {
		if !strings.Contains(beta, want) {
			t.Errorf("anthropic-beta = %q, missing %q", beta, want)
		}
	}
	if got := cap.header.Get("X-App"); got != "cli" {
		t.Errorf("x-app = %q, want cli", got)
	}
	if got := cap.header.Get("User-Agent"); !strings.HasPrefix(got, "claude-cli/") {
		t.Errorf("user-agent = %q, want claude-cli/ prefix", got)
	}
	// The forced Claude Code identity block must lead the system prompt.
	if len(cap.body.System) != 2 || cap.body.System[0].Text != systemIdentity {
		t.Fatalf("system = %+v, want identity + caller", cap.body.System)
	}
}

func TestHeadersOAuthExtraBetas(t *testing.T) {
	p, cap := captureProvider(t, provider.CredOAuth, WithBetas("prompt-caching-2024-07-31"))
	h, _ := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{provider.UserText("hi")}})
	drain(t, h)
	_ = h.Close()

	beta := cap.header.Get("Anthropic-Beta")
	// Mandatory betas come first, extras appended.
	if !strings.HasPrefix(beta, oauthBetas[0]) {
		t.Errorf("anthropic-beta = %q, want mandatory betas first", beta)
	}
	if !strings.Contains(beta, "prompt-caching-2024-07-31") {
		t.Errorf("anthropic-beta = %q, missing extra beta", beta)
	}
}

func TestHeadersAPIKeyExtraBetas(t *testing.T) {
	p, cap := captureProvider(t, provider.CredAPIKey, WithBetas("prompt-caching-2024-07-31"))
	h, _ := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{provider.UserText("hi")}})
	drain(t, h)
	_ = h.Close()

	if got := cap.header.Get("Anthropic-Beta"); got != "prompt-caching-2024-07-31" {
		t.Errorf("anthropic-beta = %q, want just the extra beta", got)
	}
}
