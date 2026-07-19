package provider_test

import (
	"errors"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// TestResolveRegistered asserts a registered id resolves to its full record,
// with Unregistered clear so consumers trust its pricing and limits.
func TestResolveRegistered(t *testing.T) {
	got, err := provider.Resolve("claude-sonnet-5")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Unregistered {
		t.Error("Unregistered = true for a registered model, want false")
	}
	if got.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", got.Provider, "anthropic")
	}
	if got.ContextWindow == 0 || got.Pricing.Input == 0 {
		t.Errorf("registered model resolved without metadata: %+v", got)
	}
}

// TestResolveUnregisteredRuns is the regression test for the allowlist bug: an
// id the registry does not carry must still resolve, so it can be run. If the
// registry is ever restored as a gate, this fails.
func TestResolveUnregisteredRuns(t *testing.T) {
	tests := []struct{ id, wantProvider string }{
		{"gpt-5.5-turbo-2027", "openai"},
		{"o7-mini", "openai"},
		{"claude-opus-9-1", "anthropic"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if _, ok := provider.Lookup(tt.id); ok {
				t.Fatalf("test premise broken: %q is registered", tt.id)
			}
			got, err := provider.Resolve(tt.id)
			if err != nil {
				t.Fatalf("Resolve(%q) = %v, want an unregistered model to resolve", tt.id, err)
			}
			if got.Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", got.Provider, tt.wantProvider)
			}
			if got.ID != tt.id {
				t.Errorf("ID = %q, want %q", got.ID, tt.id)
			}
			if !got.Unregistered {
				t.Error("Unregistered = false, want true — degradation must be visible to consumers")
			}
		})
	}
}

// TestResolveUnregisteredMetadataIsUnknown pins the degraded record's shape:
// every metadata field is the zero value MEANING UNKNOWN. This is the contract
// a consumer relies on to render "unavailable" rather than a fabricated
// $0.00 price or an exhausted context budget.
func TestResolveUnregisteredMetadataIsUnknown(t *testing.T) {
	got, err := provider.Resolve("gpt-5.5-turbo-2027")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 (unknown); a fabricated window misreports the budget", got.ContextWindow)
	}
	if got.MaxOutput != 0 {
		t.Errorf("MaxOutput = %d, want 0 so adapters fall back to their own default", got.MaxOutput)
	}
	if got.Pricing != (provider.Pricing{}) {
		t.Errorf("Pricing = %+v, want zero value", got.Pricing)
	}
	if got.Reasoning {
		t.Error("Reasoning = true, want the conservative false for an unknown model")
	}
}

// TestResolveEmptyModel asserts the empty id reports "no model was resolved" —
// a caller-side bug — and NOT "your model name is unknown", which would send
// the user looking for a typo that does not exist.
func TestResolveEmptyModel(t *testing.T) {
	_, err := provider.Resolve("")
	if !errors.Is(err, provider.ErrNoModel) {
		t.Fatalf("Resolve(\"\") = %v, want ErrNoModel", err)
	}
	if errors.Is(err, provider.ErrUnknownProvider) {
		t.Error("empty model reported as an unknown provider; the two cases must stay distinct")
	}
}

// TestResolveUninferableProvider asserts an id belonging to no known family
// still errors — there is no adapter to route it to — and does so with its own
// sentinel, distinct from the empty case.
func TestResolveUninferableProvider(t *testing.T) {
	_, err := provider.Resolve("not-a-real-model")
	if !errors.Is(err, provider.ErrUnknownProvider) {
		t.Fatalf("Resolve = %v, want ErrUnknownProvider", err)
	}
	if errors.Is(err, provider.ErrNoModel) {
		t.Error("uninferable model reported as ErrNoModel; the two cases must stay distinct")
	}
}

// TestCostOfUnregisteredReportsUnknown asserts the cost path refuses to price
// an unregistered model rather than returning a zero cost as if it were free.
func TestCostOfUnregisteredReportsUnknown(t *testing.T) {
	u := provider.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	if _, ok := provider.CostOf("gpt-5.5-turbo-2027", u); ok {
		t.Error("CostOf ok = true for an unregistered model; a $0.00 cost would be reported as fact")
	}
}
