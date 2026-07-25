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

func TestLookupPrefersExactMiniModel(t *testing.T) {
	t.Parallel()
	rates, ok := Lookup("gpt-5.4-mini")
	if !ok || rates.Model != "gpt-5.4-mini" || rates.InputPerMillion != 0.75 || rates.LongContextThreshold != 0 {
		t.Fatalf("Lookup(gpt-5.4-mini) = %+v, %v", rates, ok)
	}
}

func TestPublishedPaidEquivalentPrices(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"deepseek-v4-flash-free", "mimo-v2.5-free", "nemotron-3-ultra-free"} {
		card, ok := PublishedListPrice(model)
		if !ok || !card.Available || card.Currency != "USD" || card.InputPerMillion == 0 || card.OutputPerMillion == 0 {
			t.Errorf("PublishedListPrice(%q) = %+v, %v", model, card, ok)
		}
		cost := Calculate(model, api.Usage{InputTokens: 100, OutputTokens: 50})
		if !cost.Available || cost.TotalUSD == 0 || cost.Source == ZenSourceURL {
			t.Errorf("Calculate(%q) = %+v", model, cost)
		}
	}
	card, ok := PublishedListPrice("north-mini-code-free")
	if !ok || card.Available || card.Source != CohereNorthSourceURL {
		t.Fatalf("North Mini Code price = %+v, %v", card, ok)
	}
}

func TestHy3UsesPaidUSDRoute(t *testing.T) {
	t.Parallel()
	card, ok := PublishedListPrice("hy3-free")
	if !ok || !card.Available || card.Currency != "USD" || card.InputPerMillion != 0.20 || card.CachedInputPerMillion != 0.05 || card.OutputPerMillion != 0.80 {
		t.Fatalf("Hy3 price = %+v, %v", card, ok)
	}
	if cost := Calculate("hy3-free", api.Usage{InputTokens: 100}); !cost.Available || cost.TotalUSD == 0 {
		t.Fatalf("Hy3 paid equivalent = %+v", cost)
	}
}
