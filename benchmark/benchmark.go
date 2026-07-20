// Package benchmark compares fast and slow solver modes on the same exercise.
package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/pricing"
	"github.com/tamnd/taocp-solver/prompt"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/solver"
	"github.com/tamnd/taocp-solver/textguard"
)

var (
	winnerPattern = regexp.MustCompile(`(?m)^WINNER:\s*(A|B|TIE)\s*$`)
	scoreAPattern = regexp.MustCompile(`(?m)^SCORE_A:\s*([0-7])/7\s*$`)
	scoreBPattern = regexp.MustCompile(`(?m)^SCORE_B:\s*([0-7])/7\s*$`)
	truthAPattern = regexp.MustCompile(`(?m)^TRUTH_A:\s*(TRUE|FALSE)\s*$`)
	truthBPattern = regexp.MustCompile(`(?m)^TRUTH_B:\s*(TRUE|FALSE)\s*$`)
)

type Judgment struct {
	Order     string `json:"order"`
	FastScore int    `json:"fast_score"`
	SlowScore int    `json:"slow_score"`
	FastTrue  bool   `json:"fast_true"`
	SlowTrue  bool   `json:"slow_true"`
	Winner    string `json:"winner"`
	Review    string `json:"review_md"`
}

type Report struct {
	Section           string         `json:"section"`
	Number            int            `json:"number"`
	Winner            string         `json:"winner"`
	Fast              result.Result  `json:"fast"`
	Slow              result.Result  `json:"slow"`
	Judgments         []Judgment     `json:"judgments"`
	EvaluationMetrics result.Metrics `json:"evaluation_metrics"`
	Difference        Difference     `json:"slow_minus_fast"`
}

type Difference struct {
	TotalTokens int     `json:"total_tokens"`
	ListCostUSD float64 `json:"list_cost_usd"`
	TokenRatio  float64 `json:"token_ratio"`
	CostRatio   float64 `json:"cost_ratio"`
}

type Runner struct {
	Repository     *exercise.Repository
	Client         api.Completer
	Prompts        prompt.Builder
	OutputRoot     string
	Model          string
	Candidates     int
	MaxCorrections int
	Progress       func(string)
}

func (r *Runner) Run(ctx context.Context, section string, number int) (Report, error) {
	if r.Repository == nil || r.Client == nil || strings.TrimSpace(r.OutputRoot) == "" {
		return Report{}, errors.New("benchmark runner is not configured")
	}
	fastEngine := r.engine(filepath.Join(r.OutputRoot, "fast"))
	slowEngine := r.engine(filepath.Join(r.OutputRoot, "slow"))
	fast, err := fastEngine.Solve(ctx, section, number, solver.Options{
		Mode: solver.ModeFast, Model: r.Model, Force: true,
	})
	if err != nil {
		return Report{}, fmt.Errorf("fast mode: %w", err)
	}
	slow, err := slowEngine.Solve(ctx, section, number, solver.Options{
		Mode: solver.ModeSlow, Model: r.Model, Force: true,
		Candidates: r.Candidates, MaxCorrections: r.MaxCorrections,
	})
	if err != nil {
		return Report{}, fmt.Errorf("slow mode: %w", err)
	}
	return r.Compare(ctx, section, number, fast, slow)
}

// Compare audits existing fast and slow results without regenerating them.
func (r *Runner) Compare(ctx context.Context, section string, number int, fast, slow result.Result) (Report, error) {
	if r.Repository == nil || r.Client == nil || strings.TrimSpace(r.OutputRoot) == "" {
		return Report{}, errors.New("benchmark runner is not configured")
	}
	ex, sourceContext, err := r.Repository.Load(section, number)
	if err != nil {
		return Report{}, err
	}
	first, firstAttempt, err := r.judge(ctx, ex, sourceContext, fast.Solution, slow.Solution, false)
	if err != nil {
		return Report{}, err
	}
	second, secondAttempt, err := r.judge(ctx, ex, sourceContext, slow.Solution, fast.Solution, true)
	if err != nil {
		return Report{}, err
	}
	winner := "INCONCLUSIVE"
	if first.Winner == second.Winner {
		winner = first.Winner
	}
	attempts := []result.Attempt{firstAttempt, secondAttempt}
	difference := compareMetrics(fast.Metrics.CurrentRun, slow.Metrics.CurrentRun)
	report := Report{
		Section: section, Number: number, Winner: winner, Fast: fast, Slow: slow,
		Judgments: []Judgment{first, second}, EvaluationMetrics: result.BuildMetrics(attempts),
		Difference: difference,
	}
	if err := saveReport(filepath.Join(r.OutputRoot, "report.json"), report); err != nil {
		return Report{}, err
	}
	return report, nil
}

func saveReport(path string, report Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create benchmark directory: %w", err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode benchmark report: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".report-*")
	if err != nil {
		return fmt.Errorf("create temporary benchmark report: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set benchmark report permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write benchmark report: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync benchmark report: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close benchmark report: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace benchmark report: %w", err)
	}
	return nil
}

func compareMetrics(fast, slow result.MetricSet) Difference {
	difference := Difference{
		TotalTokens: slow.Tokens.TotalTokens - fast.Tokens.TotalTokens,
		ListCostUSD: slow.ListCost.TotalUSD - fast.ListCost.TotalUSD,
	}
	if fast.Tokens.TotalTokens > 0 {
		difference.TokenRatio = float64(slow.Tokens.TotalTokens) / float64(fast.Tokens.TotalTokens)
	}
	if fast.ListCost.TotalUSD > 0 {
		difference.CostRatio = slow.ListCost.TotalUSD / fast.ListCost.TotalUSD
	}
	return difference
}

func (r *Runner) engine(root string) *solver.Engine {
	return &solver.Engine{
		Repository: r.Repository, Client: r.Client, Prompts: r.Prompts,
		Store: result.Store{Root: root}, Progress: r.Progress,
	}
}

func (r *Runner) judge(ctx context.Context, ex exercise.Exercise, sourceContext exercise.Context, a, b string, reversed bool) (Judgment, result.Attempt, error) {
	instructions, input := r.Prompts.CompareQuality(ex, sourceContext, a, b)
	response, err := r.Client.Complete(ctx, api.Request{
		Model: r.Model, Instructions: instructions, Input: input, Effort: "high",
		Metadata: map[string]string{"task": "taocp-mode-benchmark", "exercise": fmt.Sprintf("%s.%d", ex.SectionID, ex.Number)},
	})
	if err != nil {
		return Judgment{}, result.Attempt{}, fmt.Errorf("quality judgment: %w", err)
	}
	review := textguard.CleanGeneratedText(response.Text)
	parsed, err := parse(review, reversed)
	if err != nil {
		return Judgment{}, result.Attempt{}, err
	}
	parsed.Review = review
	usage := response.Usage.Normalized()
	model := response.Model
	if model == "" {
		model = r.Model
	}
	attempt := result.Attempt{
		Phase: "benchmark-quality", CurrentRun: true, ResponseID: response.ID,
		Model: model, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		Usage: usage, ListCost: pricing.Calculate(model, usage),
	}
	return parsed, attempt, nil
}

func parse(review string, reversed bool) (Judgment, error) {
	winner, ok := last(winnerPattern, review)
	aScore, okA := score(scoreAPattern, review)
	bScore, okB := score(scoreBPattern, review)
	aTruth, okAT := truth(truthAPattern, review)
	bTruth, okBT := truth(truthBPattern, review)
	if !ok || !okA || !okB || !okAT || !okBT {
		return Judgment{}, errors.New("quality judgment has incomplete final fields")
	}
	if (aScore >= 6 && !aTruth) || (bScore >= 6 && !bTruth) {
		return Judgment{}, errors.New("quality judgment has inconsistent score and truth fields")
	}
	if aTruth != bTruth {
		truthWinner := "A"
		if bTruth {
			truthWinner = "B"
		}
		if winner != truthWinner {
			return Judgment{}, errors.New("quality judgment selects a false solution over a true solution")
		}
	}
	judgment := Judgment{FastScore: aScore, SlowScore: bScore, FastTrue: aTruth, SlowTrue: bTruth, Winner: mapWinner(winner, false)}
	if reversed {
		judgment.Order = "slow,fast"
		judgment.FastScore, judgment.SlowScore = bScore, aScore
		judgment.FastTrue, judgment.SlowTrue = bTruth, aTruth
		judgment.Winner = mapWinner(winner, true)
	} else {
		judgment.Order = "fast,slow"
	}
	return judgment, nil
}

func mapWinner(value string, reversed bool) string {
	if value == "TIE" {
		return "TIE"
	}
	if (value == "A") != reversed {
		return "FAST"
	}
	return "SLOW"
}

func last(pattern *regexp.Regexp, value string) (string, bool) {
	matches := pattern.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return "", false
	}
	return matches[len(matches)-1][1], true
}

func score(pattern *regexp.Regexp, value string) (int, bool) {
	field, ok := last(pattern, value)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(field)
	return n, err == nil
}

func truth(pattern *regexp.Regexp, value string) (bool, bool) {
	field, ok := last(pattern, value)
	return field == "TRUE", ok
}
