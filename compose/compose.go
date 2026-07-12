// Package compose turns a declarative agent manifest into a wired session. At
// M0 the manifest names a model and a provider (only faux); Load reads a YAML
// file, constructs the provider, and returns a ready [session.Session].
package compose

import (
	"context"
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/provider/faux"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// Manifest is the declarative description of an agent.
type Manifest struct {
	// Name identifies the agent. Required.
	Name string `yaml:"name"`
	// Model is the model the session binds, or empty for the provider default.
	Model string `yaml:"model,omitempty"`
	// Provider selects and configures the LLM backend. Required.
	Provider ProviderConfig `yaml:"provider"`
}

// ProviderConfig selects a provider implementation.
type ProviderConfig struct {
	// Type is the provider kind. Only "faux" is supported at M0.
	Type string `yaml:"type"`
}

// supportedProviders lists the provider types Build understands, for error
// messages.
var supportedProviders = []string{"faux"}

// Parse decodes and validates a manifest from YAML.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("compose: parse manifest: %w", err)
	}
	if m.Name == "" {
		return nil, errors.New("compose: manifest name is required")
	}
	if m.Provider.Type == "" {
		return nil, errors.New("compose: manifest provider.type is required")
	}
	return &m, nil
}

// Load reads a manifest file and returns a wired session. Options are forwarded
// to [session.New] (e.g. deterministic id/clock seams in tests).
func Load(ctx context.Context, path string, opts ...session.Option) (*session.Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("compose: read manifest %q: %w", path, err)
	}
	m, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return Build(ctx, m, opts...)
}

// Build constructs the provider named by the manifest and wires a session.
// Options are appended after the manifest-derived options, so a caller can
// override the model if needed.
func Build(_ context.Context, m *Manifest, opts ...session.Option) (*session.Session, error) {
	p, err := buildProvider(m.Provider)
	if err != nil {
		return nil, err
	}
	full := append([]session.Option{session.WithModel(m.Model)}, opts...)
	return session.New(p, full...), nil
}

func buildProvider(pc ProviderConfig) (provider.Provider, error) {
	switch pc.Type {
	case "faux":
		return faux.New(faux.Default()), nil
	default:
		return nil, fmt.Errorf("compose: unsupported provider type %q (supported: %v)", pc.Type, supportedProviders)
	}
}
