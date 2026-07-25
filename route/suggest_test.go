package route

import (
	"strings"
	"testing"
)

// The catalogue as GET /zen/v1/models reported it on 2026-07-25, free models
// only. hy3-free is absent, which is the whole reason this exists.
var zenFreeCatalogue = []string{
	"deepseek-v4-flash-free", "mimo-v2.5-free", "ling-3.0-flash-free",
	"nemotron-3-ultra-free", "north-mini-code-free", "laguna-s-2.1-free",
}

func TestSuggestKeepsRanksAndDisablesWhatDisappeared(t *testing.T) {
	t.Parallel()
	registry := Registry{Routes: []Route{
		{Name: "zen-free-nemotron", Wire: WireChat, BaseURL: "http://zen/v1", Model: "nemotron-3-ultra-free", Rank: 30},
		{Name: "zen-free-hy3", Wire: WireChat, BaseURL: "http://zen/v1", Model: "hy3-free", Rank: 31},
	}}
	suggested := Suggest(registry, map[string][]string{
		"zen-free-nemotron": zenFreeCatalogue,
		"zen-free-hy3":      zenFreeCatalogue,
	})

	nemotron, _ := suggested.Find("zen-free-nemotron")
	if nemotron.Rank != 30 || nemotron.Disabled {
		t.Errorf("a route that still exists changed: %+v", nemotron)
	}
	hy3, ok := suggested.Find("zen-free-hy3")
	if !ok {
		t.Fatal("the missing route was deleted; it should be disabled with the reason")
	}
	if !hy3.Disabled {
		t.Error("a model that is no longer served is still enabled")
	}
	if !strings.Contains(hy3.Note, "not in the") {
		t.Errorf("note = %q, want the reason", hy3.Note)
	}
	if hy3.Rank != 31 {
		t.Errorf("rank = %d, want the original rank kept", hy3.Rank)
	}
}

func TestSuggestAppendsUnknownFreeModelsDisabled(t *testing.T) {
	t.Parallel()
	registry := Registry{Routes: []Route{
		{Name: "zen-free-nemotron", Wire: WireChat, BaseURL: "http://zen/v1",
			Model: "nemotron-3-ultra-free", APIKeyEnv: "OPENCODE_API_KEY", Rank: 30},
	}}
	suggested := Suggest(registry, map[string][]string{"zen-free-nemotron": zenFreeCatalogue})

	if len(suggested.Routes) != len(zenFreeCatalogue) {
		t.Fatalf("got %d routes, want one per free model", len(suggested.Routes))
	}
	for _, value := range suggested.Routes {
		if value.Model == "nemotron-3-ultra-free" {
			continue
		}
		// A model no solve has ever exercised must not silently start
		// producing published proofs.
		if !value.Disabled {
			t.Errorf("newly discovered model %s is enabled", value.Model)
		}
		if value.Rank <= 30 {
			t.Errorf("model %s landed at rank %d, ahead of a route with evidence", value.Model, value.Rank)
		}
		if value.APIKeyEnv != "OPENCODE_API_KEY" || value.BaseURL != "http://zen/v1" {
			t.Errorf("new route did not inherit the endpoint: %+v", value)
		}
		if value.Note == "" {
			t.Errorf("new route %s has no note explaining why it is off", value.Name)
		}
	}
	if _, ok := suggested.Find("zen-free-mimo-v2.5"); !ok {
		t.Errorf("names = %v, want one derived from the model", suggested.Names())
	}
	if err := suggested.Validate(); err != nil {
		t.Errorf("suggested registry does not validate: %v", err)
	}
}

func TestSuggestIsQuietWhenNothingDrifted(t *testing.T) {
	t.Parallel()
	registry := Registry{Routes: []Route{
		{Name: "zen-free-nemotron", Wire: WireChat, BaseURL: "http://zen/v1", Model: "nemotron-3-ultra-free", Rank: 30},
	}}
	suggested := Suggest(registry, map[string][]string{
		"zen-free-nemotron": {"nemotron-3-ultra-free"},
	})
	if len(suggested.Routes) != 1 {
		t.Fatalf("got %d routes, want the registry unchanged", len(suggested.Routes))
	}
	if suggested.Routes[0].Disabled || suggested.Routes[0].Note != "" {
		t.Errorf("a route that is fine was modified: %+v", suggested.Routes[0])
	}
}

// A route that never answered its catalogue call must not be condemned on the
// strength of a call that did not happen.
func TestSuggestLeavesUnprobedRoutesAlone(t *testing.T) {
	t.Parallel()
	registry := Registry{Routes: []Route{
		{Name: "gamingpc", Wire: WireChat, BaseURL: "http://local/v1", Model: "gpt-oss:20b", Rank: 40},
	}}
	suggested := Suggest(registry, map[string][]string{})
	if suggested.Routes[0].Disabled {
		t.Error("a route with no catalogue result was disabled")
	}
}

func TestDriftReportsOneLinePerStaleRoute(t *testing.T) {
	t.Parallel()
	lines := Drift([]Health{
		{Route: "zen-free-nemotron", State: StateLive},
		{Route: "zen-free-hy3", State: StateGone, Drift: "model hy3-free is not in the catalogue"},
	})
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "zen-free-hy3") || !strings.Contains(lines[0], "hy3-free") {
		t.Errorf("line = %q", lines[0])
	}
}
