package result

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/exercise"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()
	store := Store{Root: t.TempDir()}
	value := Result{
		ID: "1.1-1", Exercise: exercise.Exercise{SectionID: "1.1", Number: 1},
		Solution: "Proof.", Verdict: "PASS", Verified: true,
	}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("1.1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Solution != "Proof." || !loaded.Verified {
		t.Fatalf("loaded = %+v", loaded)
	}
	markdown, err := os.ReadFile(filepath.Join(store.Root, "1.1", "01.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "# TAOCP 1.1 Exercise 1\n\nProof.") {
		t.Fatalf("markdown = %q", markdown)
	}
}

func TestBuildMetricsSeparatesCurrentRun(t *testing.T) {
	t.Parallel()
	attempts := []Attempt{
		{Model: "gpt-5.6-sol", Usage: api.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}},
		{Model: "gpt-5.6-sol", CurrentRun: true, Usage: api.Usage{InputTokens: 20, CachedInputTokens: 8, CacheWriteTokens: 4, OutputTokens: 10, ReasoningTokens: 3, TotalTokens: 30}},
	}
	metrics := BuildMetrics(attempts)
	if metrics.CurrentRun.Tokens.Requests != 1 || metrics.CurrentRun.Tokens.UncachedInputTokens != 8 || metrics.CurrentRun.Tokens.TotalTokens != 30 {
		t.Fatalf("current = %+v", metrics.CurrentRun)
	}
	if metrics.Cumulative.Tokens.Requests != 2 || metrics.Cumulative.Tokens.InputTokens != 30 || metrics.Cumulative.Tokens.TotalTokens != 45 {
		t.Fatalf("cumulative = %+v", metrics.Cumulative)
	}
}
