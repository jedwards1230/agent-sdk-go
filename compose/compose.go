// Package compose turns a declarative agent manifest into a wired session or a
// wired agent loop. The manifest names a model, a provider, and its credentials;
// Load reads a YAML file, constructs the provider, and returns a ready
// [session.Session], while [LoopConfig] assembles a [loop.Config] for the M1
// agent loop.
package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

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
	// A provider.model entry overrides it.
	Model string `yaml:"model,omitempty"`
	// Provider selects and configures the LLM backend. Required.
	Provider ProviderConfig `yaml:"provider"`
}

// ProviderConfig selects and configures a provider implementation.
type ProviderConfig struct {
	// Type is the provider kind. Only "faux" is wired in the SDK today; the
	// Anthropic and OpenAI adapters register additional types.
	Type string `yaml:"type"`
	// Model overrides the top-level Manifest.Model for this provider.
	Model string `yaml:"model,omitempty"`
	// Auth names the credential source: "env:VAR" for an API key from an
	// environment variable, or "oauth:<provider>" for a stored OAuth token.
	Auth string `yaml:"auth,omitempty"`
}

// AuthKind distinguishes how credentials are resolved.
type AuthKind string

// The credential resolution kinds.
const (
	// AuthEnv resolves an API key from an environment variable (Ref = var name).
	AuthEnv AuthKind = "env"
	// AuthOAuth resolves a stored OAuth token for a provider (Ref = provider id).
	AuthOAuth AuthKind = "oauth"
)

// AuthSpec is a parsed provider.auth value.
type AuthSpec struct {
	Kind AuthKind
	Ref  string
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
	if m.Provider.Auth != "" {
		if _, err := ParseAuth(m.Provider.Auth); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// ParseAuth parses a provider.auth value of the form "env:VAR" or
// "oauth:<provider>".
func ParseAuth(s string) (AuthSpec, error) {
	kind, ref, ok := strings.Cut(s, ":")
	if !ok || ref == "" {
		return AuthSpec{}, fmt.Errorf("compose: invalid auth %q (want env:VAR or oauth:<provider>)", s)
	}
	switch AuthKind(kind) {
	case AuthEnv:
		return AuthSpec{Kind: AuthEnv, Ref: ref}, nil
	case AuthOAuth:
		return AuthSpec{Kind: AuthOAuth, Ref: ref}, nil
	default:
		return AuthSpec{}, fmt.Errorf("compose: unknown auth kind %q (want env or oauth)", kind)
	}
}

// ModelID returns the effective model: the provider.model override if set, else
// the top-level Manifest.Model.
func (m *Manifest) ModelID() string {
	if m.Provider.Model != "" {
		return m.Provider.Model
	}
	return m.Model
}

// CredentialSource builds a [provider.CredentialSource] from the manifest's
// provider.auth. An "env:VAR" spec yields an [provider.EnvCredentialSource]
// keyed by the provider id derived from the model registry (falling back to the
// provider type). An "oauth:*" spec requires an auth.Store and is not wired here
// — pass a CredentialSource in [LoopDeps] instead. A blank auth yields the
// zero-config env default.
func CredentialSource(m *Manifest) (provider.CredentialSource, error) {
	if m.Provider.Auth == "" {
		return provider.StaticEnv(), nil
	}
	spec, err := ParseAuth(m.Provider.Auth)
	if err != nil {
		return nil, err
	}
	switch spec.Kind {
	case AuthEnv:
		return provider.EnvCredentialSource{Vars: map[string]string{m.providerID(): spec.Ref}}, nil
	case AuthOAuth:
		return nil, fmt.Errorf("compose: oauth auth requires an auth.Store CredentialSource; pass one in LoopDeps")
	default:
		return nil, fmt.Errorf("compose: unhandled auth kind %q", spec.Kind)
	}
}

// providerID resolves the provider id credentials are keyed by: the model
// registry's provider for the manifest model, else the provider type.
func (m *Manifest) providerID() string {
	if info, ok := provider.Lookup(m.ModelID()); ok {
		return info.Provider
	}
	return m.Provider.Type
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
	full := append([]session.Option{session.WithModel(m.ModelID())}, opts...)
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
