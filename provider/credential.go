package provider

import (
	"context"
	"fmt"
	"os"
)

// CredKind distinguishes how a [Credential]'s token is used.
type CredKind string

// The credential kinds.
const (
	// CredAPIKey is a long-lived API key.
	CredAPIKey CredKind = "api_key"
	// CredOAuth is a bearer token obtained via an OAuth flow.
	CredOAuth CredKind = "oauth"
)

// Credential is the auth material for a provider request: a token and the kind
// that tells the provider how to present it (API-key header vs OAuth bearer).
type Credential struct {
	Kind  CredKind
	Token string
}

// CredentialSource resolves auth material for a provider without the provider
// importing an auth package. auth.Store implements this; [EnvCredentialSource]
// and [StaticCredentialSource] are the built-in static implementations.
type CredentialSource interface {
	// Credential returns the current material for a provider id, refreshing it
	// if expired. It errors when no credential is available.
	Credential(ctx context.Context, providerID string) (Credential, error)
}

// EnvCredentialSource reads API keys from environment variables, one per
// provider id. It is the zero-config default for API-key auth.
type EnvCredentialSource struct {
	// Vars maps a provider id to the environment variable holding its API key.
	Vars map[string]string
}

// StaticEnv returns an [EnvCredentialSource] with the conventional variable
// names for the built-in providers.
func StaticEnv() EnvCredentialSource {
	return EnvCredentialSource{Vars: map[string]string{
		"anthropic": "ANTHROPIC_API_KEY",
		"openai":    "OPENAI_API_KEY",
	}}
}

// Credential returns the API key for providerID from its environment variable.
func (e EnvCredentialSource) Credential(_ context.Context, providerID string) (Credential, error) {
	name, ok := e.Vars[providerID]
	if !ok {
		return Credential{}, fmt.Errorf("provider: no env var configured for provider %q", providerID)
	}
	token := os.Getenv(name)
	if token == "" {
		return Credential{}, fmt.Errorf("provider: env var %s for provider %q is empty", name, providerID)
	}
	return Credential{Kind: CredAPIKey, Token: token}, nil
}

// StaticCredentialSource returns a fixed credential regardless of provider id.
// It exists for tests and simple embeddings.
type StaticCredentialSource struct {
	Cred Credential
}

// Credential returns the fixed credential.
func (s StaticCredentialSource) Credential(_ context.Context, _ string) (Credential, error) {
	if s.Cred.Token == "" {
		return Credential{}, fmt.Errorf("provider: static credential has empty token")
	}
	return s.Cred, nil
}
