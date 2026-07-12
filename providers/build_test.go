package providers_test

import (
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/providers"
)

func TestBuild(t *testing.T) {
	creds := provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "x"}}

	tests := []struct {
		model   string
		want    string
		wantErr bool
	}{
		{model: "claude-sonnet-5", want: "anthropic"},
		{model: "gpt-5", want: "openai"},
		{model: "not-a-real-model", wantErr: true},
		{model: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p, err := providers.Build(tt.model, creds)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Build(%q) = %v, want error", tt.model, p)
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
