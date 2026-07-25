package matrix

import (
	"testing"

	"github.com/tamnd/taocp-solver/pricing"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/solver"
)

func TestDefaultManifestIsStratifiedAndPriced(t *testing.T) {
	t.Parallel()
	manifest := DefaultManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Exercises) != 5 || len(manifest.Models) != 23 {
		t.Fatalf("manifest has %d exercises and %d models", len(manifest.Exercises), len(manifest.Models))
	}
	levels := []int{5, 15, 25, 30, 35}
	for index, want := range levels {
		if manifest.Exercises[index].Level != want {
			t.Fatalf("exercise %d level = %d", index, manifest.Exercises[index].Level)
		}
	}
	for _, profile := range manifest.Models[:5] {
		card, ok := pricing.PublishedListPrice(profile.Model)
		if !ok || card.Source == "" {
			t.Errorf("free profile %q has no price provenance", profile.Model)
		}
		if profile.MaxRetries != nil || profile.MaxRetryDelaySeconds != 60 {
			t.Errorf("free profile %q retry policy = retries %v, max delay %d", profile.Model, profile.MaxRetries, profile.MaxRetryDelaySeconds)
		}
	}
	sol := manifest.Models[len(manifest.Models)-6]
	if len(sol.Modes) != 2 || sol.Modes[0] != solver.ModeFast || sol.Modes[1] != solver.ModeSlow {
		t.Fatalf("gpt-5.6-sol modes = %v", sol.Modes)
	}
	if manifest.Evaluator.MaxRetries == nil || *manifest.Evaluator.MaxRetries != 1 {
		t.Fatalf("evaluator retries = %v", manifest.Evaluator.MaxRetries)
	}
}

func TestProfileCostDistinguishesFreeLocalAndOfficial(t *testing.T) {
	t.Parallel()
	free := ModelProfile{Model: "deepseek-v4-flash-free", CostBasis: "free"}
	cost, card, note := profileCost(free, result.MetricSet{Tokens: result.TokenMetrics{InputTokens: 1000000}})
	if !cost.Available || cost.TotalUSD != 0.14 || !card.Available || card.Provider != "DeepSeek" || note == "" {
		t.Fatalf("free cost = %+v, %+v, %q", cost, card, note)
	}
	hy3 := ModelProfile{Model: "hy3-free", CostBasis: "free"}
	_, card, _ = profileCost(hy3, result.MetricSet{})
	if card.Currency != "USD" || card.OutputPerMillion != 0.80 {
		t.Fatalf("Hy3 card = %+v", card)
	}
	local := ModelProfile{Model: "qwen3:8b", CostBasis: "local"}
	_, card, note = profileCost(local, result.MetricSet{})
	if card.Available || note == "" {
		t.Fatalf("local price = %+v, %q", card, note)
	}
	official := ModelProfile{Model: "gpt-5.4-mini", CostBasis: "official-list"}
	metrics := result.MetricSet{Tokens: result.TokenMetrics{InputTokens: 1000000}}
	cost, card, _ = profileCost(official, metrics)
	if cost.TotalUSD != 0.75 || card.Provider != "OpenAI" {
		t.Fatalf("official cost = %+v, %+v", cost, card)
	}
}

func TestPublishedPriceCanBeRecordedBeforeProviderCall(t *testing.T) {
	t.Parallel()
	_, card, note := profileCost(ModelProfile{Model: "north-mini-code-free", CostBasis: "free"}, result.MetricSet{})
	if card.Available || card.Source != pricing.CohereNorthSourceURL || note == "" {
		t.Fatalf("pre-call price = %+v, %q", card, note)
	}
}

func TestManifestRejectsDuplicateNamesAndInvalidModes(t *testing.T) {
	t.Parallel()
	manifest := DefaultManifest()
	manifest.Models[1].Name = manifest.Models[0].Name
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected duplicate model error")
	}
	manifest = DefaultManifest()
	manifest.Models[0].Modes = []solver.Mode{"medium"}
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected invalid mode error")
	}
}

func TestRateLimitedCasesAreDeferredAndReplaced(t *testing.T) {
	t.Parallel()
	first := Case{Model: ModelProfile{Name: "free"}, Exercise: Exercise{Section: "1.2.1", Number: 1}, Mode: solver.ModeFast, Status: "provider_error", Error: "HTTP 429: FreeUsageLimitError"}
	if !isRateLimited(first) {
		t.Fatal("expected rate-limit classification")
	}
	cases := []Case{first}
	completed := first
	completed.Status = "completed"
	completed.Error = ""
	completed.RateLimitDeferrals = 1
	upsertCase(&cases, completed)
	if len(cases) != 1 || cases[0].Status != "completed" || cases[0].RateLimitDeferrals != 1 {
		t.Fatalf("cases = %+v", cases)
	}
	ordinary := first
	ordinary.Error = "connection reset"
	if isRateLimited(ordinary) {
		t.Fatal("ordinary provider error was classified as rate limited")
	}
	evaluation := first
	evaluation.Status = "evaluation_error"
	if !isRateLimited(evaluation) {
		t.Fatal("evaluator rate limit was not deferred")
	}
	retried := first
	retried.Error = "connect: connection refused"
	preserveErrorHistory([]Case{first}, &retried)
	if len(retried.ErrorHistory) != 1 || retried.ErrorHistory[0] != first.Error {
		t.Fatalf("error history = %q", retried.ErrorHistory)
	}
}
