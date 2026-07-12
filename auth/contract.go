package auth

import "github.com/jedwards1230/agent-sdk-go/provider"

// The credential contract is owned by provider core. auth reuses those types
// directly (aliases) so auth.Store is a provider.CredentialSource with no
// adapter, and provider adapters depend only on provider — never on auth.
type (
	// CredKind aliases provider.CredKind (api_key | oauth).
	CredKind = provider.CredKind
	// Credential aliases provider.Credential: the resolved auth material a
	// provider adapter presents (Kind selects the header convention).
	Credential = provider.Credential
	// CredentialSource aliases provider.CredentialSource. Store implements it.
	CredentialSource = provider.CredentialSource
)

const (
	// KindAPIKey is a static API key (aliases provider.CredAPIKey).
	KindAPIKey = provider.CredAPIKey
	// KindOAuth is a subscription OAuth access token (aliases provider.CredOAuth).
	KindOAuth = provider.CredOAuth
)

// Store satisfies the provider-core credential contract.
var _ CredentialSource = (*Store)(nil)
