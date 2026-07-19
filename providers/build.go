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
// backend via [provider.Resolve] and constructing the matching adapter with
// creds. A model the registry does not carry still builds, so long as its
// backend can be determined from its id; only an empty id, an id belonging to
// no known provider family, or a provider with no adapter returns an error.
func Build(model string, creds provider.CredentialSource) (provider.Provider, error) {
	info, err := provider.Resolve(model)
	if err != nil {
		return nil, fmt.Errorf("providers: %w", err)
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
