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
		fmt.Fprintf(&out, "| %s | %s | %d/%d | %d (%.0f%%) | %d (%.0f%%) | %.2f/7 | %d | $%.6f | %d | $%.6f | %d provider, %d eval |\n",
			item.Model, item.Mode, item.Completed, item.Planned, item.TrueSolutions, 100*item.TruthRate,
			item.Publishable, 100*item.PublishableRate, item.MeanScore,
			item.GenerationMetrics.Tokens.TotalTokens, item.GenerationCostUSD,
			item.EvaluationMetrics.Tokens.TotalTokens, item.EvaluationCostUSD,
			item.ProviderErrors, item.EvaluationErrors)
	}
	out.WriteString("\n## Cost interpretation\n\n")
	out.WriteString("Official model rows use standard API list prices applied to provider-reported token usage. Four Zen free routes use Zen's published zero list prices. Hy3 uses Tencent Cloud's upstream promotional list price, which is zero through 2026-07-22; Zen does not publish this route in its own price table. Its announced post-promotion prices are CNY 1 input, CNY 0.25 cached input, and CNY 4 output per million tokens. Local rows have no published API list price; hardware depreciation and energy were not measured. Evaluation cost is separate from solution-generation cost.\n\n")
	out.WriteString("## Evaluation protocol\n\n")
	out.WriteString("Each exercise receives one independently generated reference from the fixed evaluator. Every solution is then checked by two different model-blind prompts: a reference-grounded, criteria-decomposed truth judge and a reference-blind adversarial judge that searches for the earliest error, counterexamples, boundary failures, and invalid final conclusions. A solution is publishable only when both checks pass and the criteria judge scores it at least 6/7 on truth, completeness, self-containment, readability, and verifiability.\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out.String()), 0o644)
}
