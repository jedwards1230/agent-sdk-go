package session

import "github.com/jedwards1230/agent-sdk-go/provider"

// PriceLookup resolves per-model pricing for cost aggregation. This is a
// minimal LOCAL interface so session/ compiles standalone; the provider-core
// model registry will implement or adapt to it once it lands.
//
// RECONCILIATION POINT: when the model registry ships, wire it in as the
// [PriceLookup] passed to [Journal.Cost] instead of a bespoke adapter.
type PriceLookup interface {
	// Price returns the pricing for model, and whether it is known. An
	// unknown model still has its tokens summed; its USD contribution is 0.
	Price(model string) (ModelPrice, bool)
}

// ModelPrice is USD per 1,000,000 tokens.
type ModelPrice struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheReadPerMTok  float64
	CacheWritePerMTok float64
}

// Cost is a token/USD tally.
type Cost struct {
	Usage provider.Usage // summed tokens
	USD   float64        // priced; 0 contribution when the model price is unknown
}

// CostReport is a full journal's cost, split by model.
type CostReport struct {
	Total Cost
	// ByModel keys on Entry.Model; entries with an empty model bucket under "".
	ByModel map[string]Cost
}

// add folds u, priced at price (only if known), into c. The per-turn Raw audit
// map on u is not summed — it is provider-reported detail, not a normalized
// counter.
func (c Cost) add(u provider.Usage, price ModelPrice, known bool) Cost {
	c.Usage.InputTokens += u.InputTokens
	c.Usage.OutputTokens += u.OutputTokens
	c.Usage.CacheReadTokens += u.CacheReadTokens
	c.Usage.CacheWriteTokens += u.CacheWriteTokens
	if known {
		c.USD += float64(u.InputTokens) / 1_000_000 * price.InputPerMTok
		c.USD += float64(u.OutputTokens) / 1_000_000 * price.OutputPerMTok
		c.USD += float64(u.CacheReadTokens) / 1_000_000 * price.CacheReadPerMTok
		c.USD += float64(u.CacheWriteTokens) / 1_000_000 * price.CacheWritePerMTok
	}
	return c
}

// cost aggregates usage across entries, pricing via reg. reg may be nil (all
// USD contributions are 0; tokens are still summed). Cost sums usage over
// every entry passed in, regardless of branch — callers that want cost over
// only the folded path must filter first.
func cost(entries []Entry, reg PriceLookup) CostReport {
	report := CostReport{ByModel: make(map[string]Cost)}
	for _, e := range entries {
		if e.Usage == nil {
			continue
		}
		var (
			price ModelPrice
			known bool
		)
		if reg != nil {
			price, known = reg.Price(e.Model)
		}
		report.Total = report.Total.add(*e.Usage, price, known)
		report.ByModel[e.Model] = report.ByModel[e.Model].add(*e.Usage, price, known)
	}
	return report
}
