package matrix

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func saveMarkdown(path string, report Report) error {
	var out strings.Builder
	out.WriteString("# TAOCP solver model matrix\n\n")
	fmt.Fprintf(&out, "Completed %s. The fixed evaluator was `%s`. Provider failures are reported separately and are never scored as false solutions.\n\n",
		report.CompletedAt.Format("2006-01-02 15:04:05 UTC"), report.Manifest.Evaluator.Model)
	out.WriteString("## Capability and efficiency\n\n")
	out.WriteString("| Model | Mode | Complete | True | Publishable | Mean score | Generation tokens | Generation cost | Evaluation tokens | Evaluation cost | Failures |\n")
	out.WriteString("|:--|:--|--:|--:|--:|--:|--:|--:|--:|--:|--:|\n")
	for _, item := range report.Aggregates {
		fmt.Fprintf(&out, "| %s | %s | %d/%d | %d (%.0f%%) | %d (%.0f%%) | %.2f/7 | %d | %s | %d | $%.6f | %d provider, %d eval |\n",
			item.Model, item.Mode, item.Completed, item.Planned, item.TrueSolutions, 100*item.TruthRate,
			item.Publishable, 100*item.PublishableRate, item.MeanScore,
			item.GenerationMetrics.Tokens.TotalTokens, formatAggregateCost(item),
			item.EvaluationMetrics.Tokens.TotalTokens, item.EvaluationCostUSD,
			item.ProviderErrors, item.EvaluationErrors)
	}
	out.WriteString("\n## Generation list-cost breakdown\n\n")
	out.WriteString("Paid-equivalent standard token rates are applied even when execution used a free route. Unavailable means no paid per-token rate is published.\n\n")
	out.WriteString("| Model | Mode | Uncached input | Cached input | Cache write | Output | Total | Price source |\n")
	out.WriteString("|:--|:--|--:|--:|--:|--:|--:|:--|\n")
	for _, item := range report.Aggregates {
		cost := item.GenerationListCost
		if !cost.Available {
			fmt.Fprintf(&out, "| %s | %s | n/a | n/a | n/a | n/a | n/a | %s |\n", item.Model, item.Mode, item.PublishedListPrice.Source)
			continue
		}
		card := item.PublishedListPrice
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %s | %s | %s |\n",
			item.Model, item.Mode,
			formatCostComponent(card.Available, cost.UncachedInputUSD),
			formatCostComponent(card.CachedInputPriceAvailable, cost.CachedInputUSD),
			formatCostComponent(card.CacheWritePriceAvailable, cost.CacheWriteUSD),
			formatCostComponent(card.Available, cost.OutputUSD), formatAggregateCost(item), cost.Source)
	}
	out.WriteString("\n## Paid token rate cards\n\n")
	out.WriteString("Prices are USD per million tokens. A missing cache-write price is shown as unavailable, not zero.\n\n")
	out.WriteString("| Model | Provider | Input | Cached input | Cache write | Output | Source |\n")
	out.WriteString("|:--|:--|--:|--:|--:|--:|:--|\n")
	seenPrices := map[string]bool{}
	for _, item := range report.Aggregates {
		if seenPrices[item.Model] {
			continue
		}
		seenPrices[item.Model] = true
		card := item.PublishedListPrice
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %s | %s |\n", item.Model, card.Provider,
			formatRate(card.Available, card.InputPerMillion),
			formatRate(card.Available && card.CachedInputPriceAvailable, card.CachedInputPerMillion),
			formatRate(card.Available && card.CacheWritePriceAvailable, card.CacheWritePerMillion),
			formatRate(card.Available, card.OutputPerMillion), card.Source)
	}
	out.WriteString("\n## Cost interpretation\n\n")
	out.WriteString("Official GPT rows use OpenAI standard API list prices applied to provider-reported token usage. Zen executions are valued at paid token rates for the same underlying model. DeepSeek and Xiaomi use their direct API rates. Hy3 and Nemotron use published paid inference-route rates. Cohere publishes no paid per-token rate for North Mini Code, so its value remains unavailable. Local rows have no published API list price; hardware depreciation and energy were not measured. Evaluation cost is separate from solution-generation cost.\n\n")
	out.WriteString("## Evaluation protocol\n\n")
	out.WriteString("Each exercise receives one independently generated reference from the fixed evaluator. Every solution is then checked by two different model-blind prompts: a reference-grounded, criteria-decomposed truth judge and a reference-blind adversarial judge that searches for the earliest error, counterexamples, boundary failures, and invalid final conclusions. A solution is publishable only when both checks pass and the criteria judge scores it at least 6/7 on truth, completeness, self-containment, readability, and verifiability.\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out.String()), 0o644)
}

func formatAggregateCost(item Aggregate) string {
	if !item.GenerationListCost.Available ||
		(item.GenerationMetrics.Tokens.CachedInputTokens > 0 && !item.PublishedListPrice.CachedInputPriceAvailable) ||
		(item.GenerationMetrics.Tokens.CacheWriteTokens > 0 && !item.PublishedListPrice.CacheWritePriceAvailable) {
		return "n/a"
	}
	return fmt.Sprintf("$%.6f", item.GenerationListCost.TotalUSD)
}

func formatRate(available bool, rate float64) string {
	if !available {
		return "n/a"
	}
	return fmt.Sprintf("$%.6g", rate)
}

func formatCostComponent(available bool, value float64) string {
	if !available {
		return "n/a"
	}
	return fmt.Sprintf("$%.6f", value)
}
