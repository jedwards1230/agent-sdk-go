package provider

import "context"

// ModelLister is an OPTIONAL capability: a provider that can ask its backend
// which models the caller's credential may use, live, over the network.
//
// It is deliberately not part of [Provider]. Listing is orthogonal to running a
// turn — a provider that only streams (a scripted test double, an in-process
// bridge, a third-party adapter) has nothing useful to say here, and requiring
// it would break every such implementation for no gain. Callers type-assert:
//
//	if l, ok := p.(provider.ModelLister); ok {
//		models, err := l.ListModels(ctx)
//	}
//
// The call belongs in the SDK rather than in the consuming application because
// the adapters already own everything it needs: the HTTP client, the credential
// source (including OAuth refresh), the base URL, and the vendor-specific auth
// headers and response shapes. Reimplementing it upstack would duplicate auth
// handling and drift from the streaming path.
type ModelLister interface {
	// ListModels reports the models the configured credential can reach.
	//
	// It is STATELESS: one call to the vendor, returning what the vendor said.
	// It does not cache, persist, retry beyond the adapter's own policy, or
	// merge with the embedded registry — those are application policy and live
	// in the consuming application. In particular the SDK writes nothing to
	// disk here.
	//
	// Nothing here is registry-sourced, so every returned record has
	// [ModelInfo.Unregistered] set, and the per-field rule documented on that
	// flag applies: a metadata field at its zero value is UNKNOWN, while a
	// non-zero one is vendor-supplied fact a caller may render. What each
	// endpoint actually supplies varies and is documented on the adapters.
	//
	// Pricing is the one field that is always zero: no listing endpoint reports
	// a price. A caller wanting pricing — or limits an endpoint omitted —
	// enriches each id itself via [Lookup].
	//
	// An empty result with a nil error means the vendor listed no models, which
	// is a successful answer and distinct from a failure. Implementations return
	// a non-nil empty slice in that case, never an error.
	ListModels(ctx context.Context) ([]ModelInfo, error)
}
