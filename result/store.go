package result

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/pricing"
)

type Attempt struct {
	Phase        string       `json:"phase"`
	Iteration    int          `json:"iteration"`
	ResponseID   string       `json:"response_id,omitempty"`
	Model        string       `json:"model,omitempty"`
	Route        string       `json:"route,omitempty"`
	CurrentRun   bool         `json:"current_run"`
	InputTokens  int          `json:"input_tokens,omitempty"`
	OutputTokens int          `json:"output_tokens,omitempty"`
	Usage        api.Usage    `json:"usage"`
	ListCost     pricing.Cost `json:"list_cost"`
}

type Review struct {
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Verdict string `json:"verdict"`
	Score   int    `json:"score,omitempty"`
}

type Candidate struct {
	Number   int    `json:"number"`
	Solution string `json:"solution_md"`
}

type Evaluation struct {
	Verdict          string `json:"verdict"`
	True             bool   `json:"true"`
	Complete         bool   `json:"complete"`
	SelfContained    bool   `json:"self_contained"`
	HumanReadable    bool   `json:"human_readable"`
	Verifiable       bool   `json:"verifiable"`
	TruthJudgePassed bool   `json:"truth_judge_passed"`
	AuditJudgePassed bool   `json:"audit_judge_passed"`
}

type TokenMetrics struct {
	Requests            int `json:"requests"`
	InputTokens         int `json:"input_tokens"`
	UncachedInputTokens int `json:"uncached_input_tokens"`
	CachedInputTokens   int `json:"cached_input_tokens"`
	CacheWriteTokens    int `json:"cache_write_tokens"`
	OutputTokens        int `json:"output_tokens"`
	ReasoningTokens     int `json:"reasoning_tokens"`
	TotalTokens         int `json:"total_tokens"`
}

type MetricSet struct {
	Tokens           TokenMetrics `json:"tokens"`
	ListCost         pricing.Cost `json:"list_cost"`
	PricedRequests   int          `json:"priced_requests"`
	UnpricedRequests int          `json:"unpriced_requests"`
}

type Metrics struct {
	CurrentRun MetricSet `json:"current_run"`
	Cumulative MetricSet `json:"cumulative"`
	// ByRoute breaks the current run down by the endpoint that served each
	// call. It is absent when every call went to the same place, and present
	// the moment a run failed over, because a mixed-route solution priced
	// entirely at the first route's rate would be a fiction.
	ByRoute map[string]MetricSet `json:"by_route,omitempty"`
}

type Result struct {
	ID          string            `json:"id"`
	Exercise    exercise.Exercise `json:"exercise"`
	Solution    string            `json:"solution_md"`
	Candidates  []Candidate       `json:"candidates,omitempty"`
	Reference   string            `json:"reference_md,omitempty"`
	Selection   string            `json:"selection_md,omitempty"`
	Selected    int               `json:"selected_candidate,omitempty"`
	Review      string            `json:"review_md"`
	Reviews     []Review          `json:"reviews"`
	Verdict     string            `json:"verdict"`
	Verified    bool              `json:"verified"`
	Evaluation  Evaluation        `json:"evaluation"`
	Model       string            `json:"model"`
	SolveTime   time.Duration     `json:"solve_time"`
	CompletedAt time.Time         `json:"completed_at"`
	Attempts    []Attempt         `json:"attempts"`
	Metrics     Metrics           `json:"metrics"`
}

func BuildMetrics(attempts []Attempt) Metrics {
	var metrics Metrics
	byRoute := map[string]*MetricSet{}
	for i := range attempts {
		addAttempt(&metrics.Cumulative, attempts[i])
		if !attempts[i].CurrentRun {
			continue
		}
		addAttempt(&metrics.CurrentRun, attempts[i])
		if name := attempts[i].Route; name != "" {
			if byRoute[name] == nil {
				byRoute[name] = &MetricSet{}
			}
			addAttempt(byRoute[name], attempts[i])
		}
	}
	if len(byRoute) > 1 {
		metrics.ByRoute = make(map[string]MetricSet, len(byRoute))
		for name, set := range byRoute {
			metrics.ByRoute[name] = *set
		}
	}
	return metrics
}

func addAttempt(metrics *MetricSet, attempt Attempt) {
	usage := attempt.Usage
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		usage.InputTokens = attempt.InputTokens
		usage.OutputTokens = attempt.OutputTokens
	}
	usage = usage.Normalized()
	metrics.Tokens.Requests++
	metrics.Tokens.InputTokens += usage.InputTokens
	metrics.Tokens.UncachedInputTokens += usage.UncachedInputTokens()
	metrics.Tokens.CachedInputTokens += usage.CachedInputTokens
	metrics.Tokens.CacheWriteTokens += usage.CacheWriteTokens
	metrics.Tokens.OutputTokens += usage.OutputTokens
	metrics.Tokens.ReasoningTokens += usage.ReasoningTokens
	metrics.Tokens.TotalTokens += usage.TotalTokens
	cost := attempt.ListCost
	if !cost.Available {
		cost = pricing.Calculate(attempt.Model, usage)
	}
	if cost.Available {
		metrics.PricedRequests++
		metrics.ListCost = pricing.Add(metrics.ListCost, cost)
	} else {
		metrics.UnpricedRequests++
	}
}

type Store struct {
	Root string
}

func (s Store) Load(section string, number int) (Result, error) {
	path := s.JSONPath(section, number)
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	var value Result
	if err := json.Unmarshal(data, &value); err != nil {
		return Result{}, fmt.Errorf("decode cached result %s: %w", path, err)
	}
	return value, nil
}

func (s Store) Exists(section string, number int) bool {
	_, err := os.Stat(s.JSONPath(section, number))
	return err == nil
}

func (s Store) Save(value Result) error {
	if s.Root == "" {
		return errors.New("output root is empty")
	}
	dir := filepath.Dir(s.JSONPath(value.Exercise.SectionID, value.Exercise.Number))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create result directory: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	data = append(data, '\n')
	if err := atomicWrite(s.JSONPath(value.Exercise.SectionID, value.Exercise.Number), data, 0o644); err != nil {
		return err
	}
	header := fmt.Sprintf("# TAOCP %s Exercise %d\n\n", value.Exercise.SectionID, value.Exercise.Number)
	if err := atomicWrite(s.MarkdownPath(value.Exercise.SectionID, value.Exercise.Number), []byte(header+value.Solution+"\n"), 0o644); err != nil {
		return err
	}
	return nil
}

func (s Store) JSONPath(section string, number int) string {
	return filepath.Join(s.Root, section, fmt.Sprintf("%02d.json", number))
}

func (s Store) MarkdownPath(section string, number int) string {
	return filepath.Join(s.Root, section, fmt.Sprintf("%02d.md", number))
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary result: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary result: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary result: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary result: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace result %s: %w", path, err)
	}
	return nil
}
