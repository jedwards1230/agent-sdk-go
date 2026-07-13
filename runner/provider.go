package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/jedwards1230/agent-sdk-go/auth"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/providers"
)

// ErrNoCredential marks a run that cannot start because no credential is
// configured for its provider (neither a stored login credential nor the
// provider's API-key environment variable). Callers can errors.Is against
// it; the wrapped cause carries the underlying resolution failures for
// structured consumers.
var ErrNoCredential = errors.New("no credential configured")

// NoCredentialError is a missing-credential error with a single short,
// actionable message. It keeps the underlying resolution errors reachable
// via Unwrap (so structured consumers lose nothing) while Error() stays one
// sentence rather than the redundant wrapped chain.
type NoCredentialError struct {
	// Provider is the id of the provider with no configured credential.
	Provider string
	// EnvVar is the API-key environment variable this provider falls back
	// to, or "" if none is known.
	EnvVar string
	cause  error
}

// Error returns a single actionable sentence naming the provider and, when
// known, the environment variable a caller can set instead.
func (e *NoCredentialError) Error() string {
	if e.EnvVar == "" {
		return fmt.Sprintf("no credential configured for %s", e.Provider)
	}
	return fmt.Sprintf("no credential configured for %s (set %s)", e.Provider, e.EnvVar)
}

// Unwrap returns the underlying resolution failures this error wraps.
func (e *NoCredentialError) Unwrap() error { return e.cause }

// Is reports a match against the ErrNoCredential sentinel; the wrapped cause
// still satisfies errors.Is for the SDK-level errors (e.g. auth.ErrNoCredential).
func (e *NoCredentialError) Is(target error) bool { return target == ErrNoCredential }

// envVars maps a provider id to the API-key environment variable this
// package's composite credential source falls back to. It drives both the
// env-var fallback lookup and the "(set X)" hint in NoCredentialError, so
// the checked variable and the message can never drift.
var envVars = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
}

// newProvider resolves model's backend from the SDK's model registry, builds a
// compositeCredSource (a stored login credential first, an environment
// variable second), PRE-FLIGHTS the credential (a store/env lookup — not a
// live model API call — so a missing credential fails before any session
// journal is created), and returns a real provider.Provider over it.
func newProvider(ctx context.Context, model, root string) (provider.Provider, error) {
	info, ok := provider.Lookup(model)
	if !ok {
		return nil, fmt.Errorf("runner: unknown model %q", model)
	}

	var authOpts []auth.Option
	if root != "" {
		authOpts = append(authOpts, auth.WithRoot(root))
	}
	store, err := auth.New(authOpts...)
	if err != nil {
		return nil, fmt.Errorf("runner: open credential store: %w", err)
	}
	creds := compositeCredSource{store: store, envVars: envVars}

	// Pre-flight the credential so a misconfiguration fails fast with no orphan
	// journal. A credential that resolves here but is rejected by the live API
	// still runs as a real errored session (that resolution succeeds).
	if _, err := creds.Credential(ctx, info.Provider); err != nil {
		return nil, err
	}

	return providers.Build(model, creds)
}

// compositeCredSource resolves a provider credential from the persisted
// auth.Store first — the material a stored login credential would have
// written — and falls back to the provider's conventional API-key
// environment variable (from the envVars map) when the store has no entry
// for it. This lets a caller run with either path with no extra
// configuration.
type compositeCredSource struct {
	store   *auth.Store
	envVars map[string]string
}

var _ provider.CredentialSource = compositeCredSource{}

// Credential implements provider.CredentialSource.
func (c compositeCredSource) Credential(ctx context.Context, providerID string) (provider.Credential, error) {
	cred, err := c.store.Credential(ctx, providerID)
	if err == nil {
		return cred, nil
	}
	if !errors.Is(err, auth.ErrNoCredential) {
		// A credential exists in the store but could not be resolved (e.g. an
		// expired OAuth token that failed to refresh) — surface it verbatim; it
		// is a resolution failure, not a missing credential.
		return provider.Credential{}, fmt.Errorf("runner: resolve %s credential: %w", providerID, err)
	}

	// Fall back to the API-key environment variable via an EnvCredentialSource
	// built from our own mapping — the same map that names the var in the hint
	// below, so the checked variable and the message never diverge.
	env := provider.EnvCredentialSource{Vars: c.envVars}
	cred, envErr := env.Credential(ctx, providerID)
	if envErr == nil {
		return cred, nil
	}
	return provider.Credential{}, &NoCredentialError{
		Provider: providerID,
		EnvVar:   c.envVars[providerID],
		cause:    errors.Join(err, envErr),
	}
}
