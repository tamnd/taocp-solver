package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tamnd/taocp-solver/pricing"
	"github.com/tamnd/taocp-solver/result"
)

func TestParseTarget(t *testing.T) {
	t.Parallel()
	section, number, err := parseTarget([]string{"1.2.6", "10"})
	if err != nil || section != "1.2.6" || number != 10 {
		t.Fatalf("got %q, %d, %v", section, number, err)
	}
	section, number, err = parseTarget([]string{"1.2.6.10"})
	if err != nil || section != "1.2.6" || number != 10 {
		t.Fatalf("got %q, %d, %v", section, number, err)
	}
}

func TestPrintMetrics(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	metrics := result.MetricSet{
		Tokens:   result.TokenMetrics{Requests: 2, InputTokens: 100, UncachedInputTokens: 70, CachedInputTokens: 20, CacheWriteTokens: 10, OutputTokens: 30, ReasoningTokens: 12, TotalTokens: 130},
		ListCost: pricing.Cost{Available: true, TotalUSD: 0.001234}, PricedRequests: 2,
	}
	if err := printMetrics(&output, metrics); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "cached 20, cache write 10") || !strings.Contains(got, "list cost estimate: $0.001234 USD") {
		t.Fatalf("output = %q", got)
	}
}

func TestHelp(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "taocp solve") || !strings.Contains(output.String(), "taocp review") || !strings.Contains(output.String(), "taocp benchmark") || !strings.Contains(output.String(), "taocp matrix") {
		t.Fatalf("help = %q", output.String())
	}
}

func TestParseMode(t *testing.T) {
	t.Parallel()
	if mode, err := parseMode("FAST"); err != nil || mode != "fast" {
		t.Fatalf("mode = %q, err = %v", mode, err)
	}
	if _, err := parseMode("medium"); err == nil {
		t.Fatal("expected invalid mode")
	}
}
