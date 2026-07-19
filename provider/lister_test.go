package provider_test

import (
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/provider/anthropic"
	"github.com/jedwards1230/agent-sdk-go/provider/faux"
	"github.com/jedwards1230/agent-sdk-go/provider/openai"
)

// TestModelListerContract pins the optional-capability contract callers rely
// on: the HTTP adapters advertise ModelLister, and a provider that only
// streams does NOT. Nothing in this test performs any I/O — it constructs
// adapters but only inspects the type assertion, never calling ListModels, so
// it cannot reach the network.
func TestModelListerContract(t *testing.T) {
	creds := provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk-test"}}

	for _, tc := range []struct {
		name string
		p    provider.Provider
	}{
		{"anthropic", anthropic.New("claude-sonnet-5", creds)},
		{"openai", openai.New("gpt-5", creds)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.p.(provider.ModelLister); !ok {
				t.Errorf("%s.Provider must satisfy provider.ModelLister", tc.name)
			}
		})
	}

	// faux only streams. Requiring listing of every Provider would break it and
	// every third-party adapter, which is why ModelLister is opt-in.
	var scripted provider.Provider = faux.New(faux.Default())
	if _, ok := scripted.(provider.ModelLister); ok {
		t.Error("faux.Provider must NOT satisfy provider.ModelLister; listing is opt-in")
	}
}
