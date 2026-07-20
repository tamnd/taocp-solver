package benchmark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tamnd/taocp-solver/pricing"
	"github.com/tamnd/taocp-solver/result"
)

func TestParseReversedJudgment(t *testing.T) {
	t.Parallel()
	review := "SCORE_A: 7/7\nSCORE_B: 5/7\nTRUTH_A: TRUE\nTRUTH_B: FALSE\nWINNER: A"
	got, err := parse(review, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.FastScore != 5 || got.SlowScore != 7 || got.FastTrue || !got.SlowTrue || got.Winner != "SLOW" {
		t.Fatalf("judgment = %+v", got)
	}
}

func TestSaveReport(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "report.json")
	if err := saveReport(path, Report{Section: "1.1", Number: 2, Winner: "TIE"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || data[len(data)-1] != '\n' {
		t.Fatalf("report = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestCompareMetrics(t *testing.T) {
	t.Parallel()
	got := compareMetrics(
		result.MetricSet{Tokens: result.TokenMetrics{TotalTokens: 100}, ListCost: pricing.Cost{TotalUSD: 2}},
		result.MetricSet{Tokens: result.TokenMetrics{TotalTokens: 250}, ListCost: pricing.Cost{TotalUSD: 5}},
	)
	if got.TotalTokens != 150 || got.ListCostUSD != 3 || got.TokenRatio != 2.5 || got.CostRatio != 2.5 {
		t.Fatalf("difference = %+v", got)
	}
}

func TestParseRejectsContradictoryJudgment(t *testing.T) {
	t.Parallel()
	review := "SCORE_A: 7/7\nSCORE_B: 7/7\nTRUTH_A: FALSE\nTRUTH_B: TRUE\nWINNER: A"
	if _, err := parse(review, false); err == nil {
		t.Fatal("expected contradictory judgment to fail")
	}
}
