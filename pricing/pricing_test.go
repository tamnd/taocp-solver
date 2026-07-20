package pricing

import (
	"math"
	"testing"

	"github.com/tamnd/taocp-solver/api"
)

func TestCalculateGPT56Sol(t *testing.T) {
	t.Parallel()
	cost := Calculate("gpt-5.6", api.Usage{
		InputTokens: 1000000, CachedInputTokens: 200000, CacheWriteTokens: 100000,
		OutputTokens: 100000,
	})
	// The request exceeds 272K input, so all input components use 2x rates and
	// output uses 1.5x. Fresh: $7, cached: $0.20, write: $1.25, output: $4.50.
	if !cost.Available || cost.PricingModel != "gpt-5.6-sol" || !cost.LongContext || math.Abs(cost.TotalUSD-12.95) > 1e-12 {
		t.Fatalf("cost = %+v", cost)
	}
}

func TestLookupAllGPT56Tiers(t *testing.T) {
	t.Parallel()
	tests := map[string]float64{
		"gpt-5.6-sol":            5,
		"gpt-5.6-terra":          2.5,
		"gpt-5.6-luna":           1,
		"gpt-5.6-sol-2026-07-01": 5,
	}
	for model, want := range tests {
		rates, ok := Lookup(model)
		if !ok || rates.InputPerMillion != want || rates.CacheWritePerMillion != want*1.25 {
			t.Errorf("Lookup(%q) = %+v, %v", model, rates, ok)
		}
	}
	if cost := Calculate("local", api.Usage{InputTokens: 10}); cost.Available {
		t.Fatalf("unknown model cost = %+v", cost)
	}
}
