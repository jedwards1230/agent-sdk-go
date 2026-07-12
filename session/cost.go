package session

import "github.com/jedwards1230/agent-sdk-go/provider"

// PriceLookup resolves per-model pricing for cost aggregation. The provider
// model registry satisfies it via [RegistryPricing]; tests substitute their own
// rates. Keeping [Journal.Cost] injectable (rather than hard-wired to the
// global registry) keeps its tests hermetic and lets callers price against
// negotiated rates.
type PriceLookup interface {
	// Pricing returns the pricing for model, and whether it is known. An
	// unknown model still has its tokens summed; its cost stays zero.
	Pricing(model string) (provider.Pricing, bool)
}

// RegistryPricing adapts the embedded provider model registry
// ([provider.Lookup]) to [PriceLookup]. It is the production lookup to pass to
// [Journal.Cost].
type RegistryPricing struct{}

// Pricing resolves model against the provider registry.
func (RegistryPricing) Pricing(model string) (provider.Pricing, bool) {
	m, ok := provider.Lookup(model)
	if !ok {
		return provider.Pricing{}, false
	}
	return m.Pricing, true
}

// ModelCost is the token/cost tally for one model within a session.
type ModelCost struct {
	Model string
	Usage provider.Usage // summed tokens for this model
	Cost  provider.Cost  // priced cost, with per-bucket USD breakdown
}

// CostReport is a journal's total cost plus a per-model breakdown.
type CostReport struct {
	// Usage is the summed token usage across every entry.
	Usage provider.Usage
	// Cost is the total priced cost across every model.
	Cost provider.Cost
	// ByModel keys on Entry.Model; entries with an empty model bucket under "".
	ByModel map[string]ModelCost
}

// cost aggregates usage across entries — every branch, including ones dropped
// from Fold by a fork — pricing each model's summed usage via reg. reg may be
// nil (all costs zero; tokens still summed). Because pricing is linear in token
// counts, summing usage per model and pricing once equals pricing each turn and
// summing, with less float accumulation.
func cost(entries []Entry, reg PriceLookup) CostReport {
	usageByModel := make(map[string]provider.Usage)
	var totalUsage provider.Usage
	for _, e := range entries {
		if e.Usage == nil {
			continue
		}
		usageByModel[e.Model] = addUsage(usageByModel[e.Model], *e.Usage)
		totalUsage = addUsage(totalUsage, *e.Usage)
	}

	report := CostReport{Usage: totalUsage, ByModel: make(map[string]ModelCost, len(usageByModel))}
	for model, u := range usageByModel {
		var c provider.Cost
		if reg != nil {
			if p, ok := reg.Pricing(model); ok {
				c = p.Cost(u)
			}
		}
		report.ByModel[model] = ModelCost{Model: model, Usage: u, Cost: c}
		report.Cost = addCost(report.Cost, c)
	}
	return report
}

// addUsage sums two usages field-wise. The per-turn Raw audit map is not
// merged — it is provider-reported detail, not a normalized counter.
func addUsage(a, b provider.Usage) provider.Usage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.CacheWriteTokens += b.CacheWriteTokens
	return a
}

// addCost sums two costs field-wise across the total and every USD bucket.
func addCost(a, b provider.Cost) provider.Cost {
	a.USD += b.USD
	a.InputUSD += b.InputUSD
	a.OutputUSD += b.OutputUSD
	a.CacheReadUSD += b.CacheReadUSD
	a.CacheWriteUSD += b.CacheWriteUSD
	return a
}
