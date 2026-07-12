// Package providers is the factory apps use to construct a [provider.Provider]
// from a model id, so embedders stop importing the vendor adapter packages
// directly. It resolves the backend from the model registry and dispatches to
// the matching adapter's constructor.
package providers

import (
	"fmt"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/provider/anthropic"
	"github.com/jedwards1230/agent-sdk-go/provider/openai"
)

// Build returns a [provider.Provider] for the given model id, resolving the
// backend from the model registry and constructing the matching adapter with
// creds. Unknown models and unsupported providers return an error.
func Build(model string, creds provider.CredentialSource) (provider.Provider, error) {
	if model == "" {
		return nil, fmt.Errorf("providers: model is required")
	}
	info, ok := provider.Lookup(model)
	if !ok {
		return nil, fmt.Errorf("providers: unknown model %q", model)
	}
	switch info.Provider {
	case "anthropic":
		return anthropic.New(model, creds), nil
	case "openai":
		return openai.New(model, creds), nil
	default:
		return nil, fmt.Errorf("providers: unsupported provider %q for model %q", info.Provider, model)
	}
}
