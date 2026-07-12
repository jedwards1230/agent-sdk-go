package compose

import (
	"context"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
)

// LoopDeps carries the runtime pieces the SDK cannot derive from a manifest: the
// event broker and session id to emit under, an optional tool registry, and an
// optional credential source overriding the one derived from provider.auth
// (required for oauth, which needs an auth.Store).
type LoopDeps struct {
	Broker    *event.Broker
	SessionID string
	Tools     loop.ToolRegistry
}

// LoopConfig assembles a [loop.Config] from a manifest and its runtime deps,
// constructing the provider named by the manifest and binding the model, tools,
// and event sink. The returned config is ready for [loop.Run].
//
// Only the faux provider is wired in the SDK today; the Anthropic and OpenAI
// adapters (which consume [CredentialSource]) register their own provider types
// as they land.
func LoopConfig(_ context.Context, m *Manifest, deps LoopDeps) (loop.Config, error) {
	p, err := buildProvider(m.Provider)
	if err != nil {
		return loop.Config{}, err
	}
	return loop.Config{
		Provider:  p,
		Model:     m.ModelID(),
		Tools:     deps.Tools,
		Broker:    deps.Broker,
		SessionID: deps.SessionID,
	}, nil
}
