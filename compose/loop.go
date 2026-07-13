package compose

import (
	"context"
	"fmt"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/provider/faux"
	"github.com/jedwards1230/agent-sdk-go/providers"
)

// LoopDeps carries the runtime pieces the SDK cannot derive from a manifest: the
// event broker and session id to emit under, an optional tool registry, and an
// optional credential source overriding the one derived from provider.auth
// (required for oauth, which needs an auth.Store).
type LoopDeps struct {
	Broker    *event.Broker
	SessionID string
	Tools     loop.ToolRegistry
	// CredentialSource, when set, overrides the manifest-derived credential
	// source ([CredentialSource]). Required for oauth auth, which needs an
	// auth.Store.
	CredentialSource provider.CredentialSource
}

// LoopConfig assembles a [loop.Config] from a manifest and its runtime deps,
// constructing the provider named by the manifest and binding the model, tools,
// and event sink. The returned config is ready for [loop.Run].
//
// The faux provider type wires directly; every other type resolves via
// [providers.Build] using deps.CredentialSource if set, else the credential
// source derived from provider.auth ([CredentialSource]).
func LoopConfig(_ context.Context, m *Manifest, deps LoopDeps) (loop.Config, error) {
	p, err := loopProvider(m, deps)
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

// loopProvider resolves the provider for LoopConfig: the faux provider wires
// directly, everything else goes through [providers.Build] with a resolved
// credential source.
func loopProvider(m *Manifest, deps LoopDeps) (provider.Provider, error) {
	if m.Provider.Type == "faux" {
		return faux.New(faux.Default()), nil
	}
	creds := deps.CredentialSource
	if creds == nil {
		var err error
		creds, err = CredentialSource(m)
		if err != nil {
			return nil, fmt.Errorf("compose: resolve credentials: %w", err)
		}
	}
	p, err := providers.Build(m.ModelID(), creds)
	if err != nil {
		return nil, fmt.Errorf("compose: build provider: %w", err)
	}
	return p, nil
}
