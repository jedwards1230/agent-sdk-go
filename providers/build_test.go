package providers_test

import (
	"errors"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/providers"
)

func TestBuild(t *testing.T) {
	creds := provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "x"}}

	tests := []struct {
		name    string
		model   string
		want    string
		wantErr error
	}{
		{name: "registered anthropic", model: "claude-sonnet-5", want: "anthropic"},
		{name: "registered openai", model: "gpt-5", want: "openai"},
		// Unregistered ids build: the registry supplies pricing, it does not
		// gate admission. These would fail if the allowlist were restored.
		{name: "unregistered anthropic", model: "claude-opus-9-1", want: "anthropic"},
		{name: "unregistered openai", model: "gpt-5.5-turbo-2027", want: "openai"},
		{name: "unregistered openai reasoning", model: "o7-mini", want: "openai"},
		// An id belonging to no known family has no adapter to route to.
		{name: "uninferable", model: "not-a-real-model", wantErr: provider.ErrUnknownProvider},
		// Empty is a caller-side resolution failure, not a bad model name.
		{name: "empty", model: "", wantErr: provider.ErrNoModel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := providers.Build(tt.model, creds)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Build(%q) err = %v (result %v), want %v", tt.model, err, p, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build(%q): %v", tt.model, err)
			}
			if got := p.Info().Provider; got != tt.want {
				t.Errorf("Build(%q).Info().Provider = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}
