// Package matrix runs a stratified, model-blind TAOCP capability evaluation.
package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/evaluation"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/pricing"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/solver"
)

type Runner struct {
	Manifest                 Manifest
	Repository               *exercise.Repository
	OutputRoot               string
	Timeout                  time.Duration
	MaxRetries               int
	MaxOutputTokens          int
	Parallel                 int
	Candidates               int
	MaxCorrections           int
	DeferredRateLimitRetries int
	Resume                   bool
	Progress                 func(string)
}

type Case struct {
	Model              ModelProfile      `json:"model"`
	Exercise           Exercise          `json:"exercise"`
	Mode               solver.Mode       `json:"mode"`
	Status             string            `json:"status"`
	Error              string            `json:"error,omitempty"`
	Generation         result.Result     `json:"generation"`
	IndependentAudit   evaluation.Report `json:"independent_audit"`
	GenerationCostUSD  float64           `json:"generation_cost_usd"`
	PublishedListPrice pricing.ListPrice `json:"published_list_price"`
	CostNote           string            `json:"cost_note"`
	RateLimitDeferrals int               `json:"rate_limit_deferrals"`
	Elapsed            time.Duration     `json:"elapsed"`
}

type Aggregate struct {
	Model              string            `json:"model"`
	Provider           string            `json:"provider"`
	Mode               solver.Mode       `json:"mode"`
	CostBasis          string            `json:"cost_basis"`
	Planned            int               `json:"planned"`
	Completed          int               `json:"completed"`
	ProviderErrors     int               `json:"provider_errors"`
	EvaluationErrors   int               `json:"evaluation_errors"`
	TrueSolutions      int               `json:"true_solutions"`
	Publishable        int               `json:"publishable_solutions"`
	MeanScore          float64           `json:"mean_score"`
	TruthRate          float64           `json:"truth_rate"`
	PublishableRate    float64           `json:"publishable_rate"`
	GenerationMetrics  result.MetricSet  `json:"generation_metrics"`
	EvaluationMetrics  result.MetricSet  `json:"evaluation_metrics"`
	GenerationCostUSD  float64           `json:"generation_cost_usd"`
	PublishedListPrice pricing.ListPrice `json:"published_list_price"`
	EvaluationCostUSD  float64           `json:"evaluation_cost_usd"`
	Elapsed            time.Duration     `json:"elapsed"`
}

type Report struct {
	SchemaVersion    int                             `json:"schema_version"`
	StartedAt        time.Time                       `json:"started_at"`
	CompletedAt      time.Time                       `json:"completed_at"`
	Manifest         Manifest                        `json:"manifest"`
	References       map[string]evaluation.Reference `json:"references"`
	ReferenceMetrics result.Metrics                  `json:"reference_metrics"`
	Cases            []Case                          `json:"cases"`
	Aggregates       []Aggregate                     `json:"aggregates"`
}

type job struct {
	profile  ModelProfile
	exercise Exercise
	mode     solver.Mode
}

func (r Runner) Run(ctx context.Context) (Report, error) {
	if err := r.Manifest.Validate(); err != nil {
		return Report{}, err
	}
	if r.Repository == nil || strings.TrimSpace(r.OutputRoot) == "" {
		return Report{}, errors.New("matrix runner is not configured")
	}
	if r.Parallel < 1 {
		r.Parallel = 1
	}
	if r.Timeout <= 0 {
		r.Timeout = 30 * time.Minute
	}
	report := Report{SchemaVersion: 1, StartedAt: time.Now().UTC(), Manifest: r.Manifest, References: map[string]evaluation.Reference{}}
	evaluatorClient := r.client(r.Manifest.Evaluator)
	auditor := evaluation.Auditor{Client: evaluatorClient, Model: r.Manifest.Evaluator.Model}
	for _, target := range r.Manifest.Exercises {
		key := exerciseKey(target)
		reference, err := r.loadReference(key)
		if err != nil && !os.IsNotExist(err) {
			return Report{}, err
		}
		if reference.Text == "" {
			ex, source, err := r.Repository.Load(target.Section, target.Number)
			if err != nil {
				return Report{}, err
			}
			r.log("building fixed evaluator reference for %s", key)
			reference, err = auditor.BuildReference(ctx, ex, source)
			if err != nil {
				return Report{}, err
			}
			if err := saveJSON(r.referencePath(key), reference); err != nil {
				return Report{}, err
			}
		}
		report.References[key] = reference
	}
	var referenceAttempts []result.Attempt
	for _, reference := range report.References {
		referenceAttempts = append(referenceAttempts, reference.Attempt)
	}
	report.ReferenceMetrics = result.BuildMetrics(referenceAttempts)

	var jobs []job
	for _, profile := range r.Manifest.Models {
		modes := profile.Modes
		if len(modes) == 0 {
			modes = []solver.Mode{solver.ModeFast}
		}
		for _, target := range r.Manifest.Exercises {
			for _, mode := range modes {
				jobs = append(jobs, job{profile: profile, exercise: target, mode: mode})
			}
		}
	}

	if r.DeferredRateLimitRetries < 0 {
		r.DeferredRateLimitRetries = 0
	}
	pending := jobs
	for pass := 0; len(pending) > 0; pass++ {
		var deferred []job
		jobIndex := make(map[string]job, len(pending))
		for _, item := range pending {
			jobIndex[jobKey(item)] = item
		}
		for item := range r.runJobs(ctx, auditor, report.References, pending) {
			if pass > item.RateLimitDeferrals {
				item.RateLimitDeferrals = pass
			}
			upsertCase(&report.Cases, item)
			sortCases(report.Cases)
			report.Aggregates = aggregate(report.Cases)
			report.CompletedAt = time.Now().UTC()
			if err := saveJSON(filepath.Join(r.OutputRoot, "report.json"), report); err != nil {
				return Report{}, err
			}
			r.log("%s %s %s: %s", item.Model.Name, exerciseKey(item.Exercise), item.Mode, item.Status)
			if pass < r.DeferredRateLimitRetries && isRateLimited(item) {
				deferred = append(deferred, jobIndex[caseKey(item)])
			}
		}
		pending = deferred
		if len(pending) > 0 {
			r.log("retrying %d rate-limited cases after completing pass %d", len(pending), pass+1)
		}
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	report.CompletedAt = time.Now().UTC()
	report.Aggregates = aggregate(report.Cases)
	if err := saveJSON(filepath.Join(r.OutputRoot, "report.json"), report); err != nil {
		return Report{}, err
	}
	if err := saveMarkdown(filepath.Join(r.OutputRoot, "REPORT.md"), report); err != nil {
		return Report{}, err
	}
	return report, nil
}

func (r Runner) runJobs(ctx context.Context, auditor evaluation.Auditor, references map[string]evaluation.Reference, jobs []job) <-chan Case {
	jobCh := make(chan job)
	caseCh := make(chan Case)
	var workers sync.WaitGroup
	for range r.Parallel {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobCh {
				caseCh <- r.runCase(ctx, auditor, references[exerciseKey(item.exercise)], item)
			}
		}()
	}
	go func() {
		defer close(jobCh)
		for _, item := range jobs {
			select {
			case jobCh <- item:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(caseCh)
	}()
	return caseCh
}

func isRateLimited(item Case) bool {
	if item.Status != "provider_error" {
		return false
	}
	message := strings.ToLower(item.Error)
	return strings.Contains(message, "429") || strings.Contains(message, "rate limit") || strings.Contains(message, "freeusagelimiterror")
}

func upsertCase(cases *[]Case, item Case) {
	key := caseKey(item)
	for index := range *cases {
		if caseKey((*cases)[index]) == key {
			(*cases)[index] = item
			return
		}
	}
	*cases = append(*cases, item)
}

func caseKey(item Case) string {
	return item.Model.Name + "\x00" + string(item.Mode) + "\x00" + exerciseKey(item.Exercise)
}

func jobKey(item job) string {
	return item.profile.Name + "\x00" + string(item.mode) + "\x00" + exerciseKey(item.exercise)
}

func (r Runner) runCase(ctx context.Context, auditor evaluation.Auditor, reference evaluation.Reference, item job) Case {
	started := time.Now()
	path := r.casePath(item)
	var value Case
	if r.Resume {
		var cached Case
		if loadJSON(path, &cached) == nil {
			if cached.Status == "completed" {
				return cached
			}
			if cached.Generation.Solution != "" {
				value = cached
			}
		}
	}
	elapsedBefore := value.Elapsed
	if value.Generation.Solution == "" {
		_, card, costNote := profileCost(item.profile, result.MetricSet{})
		value = Case{
			Model: item.profile, Exercise: item.exercise, Mode: item.mode, Status: "provider_error",
			PublishedListPrice: card, CostNote: costNote,
		}
		client := r.client(item.profile)
		engine := &solver.Engine{
			Repository: r.Repository, Client: client,
			Store: result.Store{Root: filepath.Join(r.OutputRoot, "solutions", safe(item.profile.Name), string(item.mode))},
		}
		generation, err := engine.Solve(ctx, item.exercise.Section, item.exercise.Number, solver.Options{
			Mode: item.mode, Model: item.profile.Model, Force: true,
			Candidates: r.Candidates, MaxCorrections: r.MaxCorrections,
		})
		if err != nil {
			value.Error = err.Error()
			value.Elapsed = time.Since(started).Round(time.Millisecond)
			_ = saveJSON(path, value)
			return value
		}
		value.Generation = generation
		value.GenerationCostUSD, value.PublishedListPrice, value.CostNote = profileCost(item.profile, generation.Metrics.CurrentRun)
		value.Status = "evaluation_pending"
		value.Error = ""
		value.Elapsed = time.Since(started).Round(time.Millisecond)
		if err := saveJSON(path, value); err != nil {
			value.Status = "evaluation_error"
			value.Error = fmt.Sprintf("save generation checkpoint: %v", err)
			return value
		}
		elapsedBefore = value.Elapsed
		started = time.Now()
	}
	ex, source, err := r.Repository.Load(item.exercise.Section, item.exercise.Number)
	if err != nil {
		value.Status = "evaluation_error"
		value.Error = err.Error()
		value.Elapsed = elapsedBefore + time.Since(started).Round(time.Millisecond)
		_ = saveJSON(path, value)
		return value
	}
	audit, err := auditor.Evaluate(ctx, ex, source, reference.Text, value.Generation.Solution)
	value.IndependentAudit = audit
	if err != nil {
		value.Status = "evaluation_error"
		value.Error = err.Error()
	} else {
		value.Status = "completed"
	}
	value.Elapsed = elapsedBefore + time.Since(started).Round(time.Millisecond)
	_ = saveJSON(path, value)
	return value
}

func (r Runner) client(profile ModelProfile) api.Completer {
	key := ""
	if profile.APIKeyEnv != "" {
		key = os.Getenv(profile.APIKeyEnv)
	}
	httpClient := &http.Client{Timeout: r.Timeout}
	maxRetries := r.MaxRetries
	if profile.MaxRetries != nil {
		maxRetries = *profile.MaxRetries
	}
	maxRetryDelay := time.Duration(profile.MaxRetryDelaySeconds) * time.Second
	base := strings.TrimRight(profile.BaseURL, "/")
	if profile.Protocol == "responses" {
		url := base + "/responses"
		if !strings.HasSuffix(base, "/v1") {
			url = base + "/v1/responses"
		}
		return &api.Client{URL: url, APIKey: key, HTTPClient: httpClient, MaxRetries: maxRetries, MaxRetryDelay: maxRetryDelay, MaxOutputTokens: r.MaxOutputTokens, UserAgent: "taocp-matrix"}
	}
	url := base + "/chat/completions"
	if !strings.HasSuffix(base, "/v1") {
		url = base + "/v1/chat/completions"
	}
	return &api.ChatClient{URL: url, APIKey: key, HTTPClient: httpClient, MaxRetries: maxRetries, MaxRetryDelay: maxRetryDelay, MaxOutputTokens: r.MaxOutputTokens, UserAgent: "taocp-matrix"}
}

func profileCost(profile ModelProfile, metrics result.MetricSet) (float64, pricing.ListPrice, string) {
	card, _ := pricing.PublishedListPrice(profile.Model)
	switch profile.CostBasis {
	case "free":
		if !card.Available {
			return 0, card, "The route name indicates free access, but no authoritative published list price was found."
		}
		if profile.Model == "hy3-free" {
			return 0, card, "Current upstream list price is zero during Tencent Cloud's promotion. Zen does not publish this route in its price table."
		}
		return 0, card, "OpenCode Zen's published list price is zero."
	case "local":
		return 0, card, "No published API list price. Hardware depreciation and energy cost were not measured."
	default:
		if metrics.UnpricedRequests > 0 {
			return 0, card, "Official list cost is unavailable for at least one request."
		}
		return metrics.ListCost.TotalUSD, card, "Official standard API list-cost estimate."
	}
}

func aggregate(cases []Case) []Aggregate {
	byKey := map[string]*Aggregate{}
	var scores = map[string]int{}
	for _, item := range cases {
		key := item.Model.Name + "\x00" + string(item.Mode)
		value := byKey[key]
		if value == nil {
			value = &Aggregate{Model: item.Model.Name, Provider: item.Model.Provider, Mode: item.Mode, CostBasis: item.Model.CostBasis, PublishedListPrice: item.PublishedListPrice}
			byKey[key] = value
		}
		value.Planned++
		value.Elapsed += item.Elapsed
		switch item.Status {
		case "provider_error":
			value.ProviderErrors++
		case "evaluation_error":
			value.EvaluationErrors++
		default:
			value.Completed++
			if item.IndependentAudit.Truth {
				value.TrueSolutions++
			}
			if item.IndependentAudit.Publishable {
				value.Publishable++
			}
			scores[key] += item.IndependentAudit.Score
			addMetrics(&value.GenerationMetrics, item.Generation.Metrics.CurrentRun)
			addMetrics(&value.EvaluationMetrics, item.IndependentAudit.Metrics.CurrentRun)
			value.GenerationCostUSD += item.GenerationCostUSD
			value.EvaluationCostUSD += item.IndependentAudit.Metrics.CurrentRun.ListCost.TotalUSD
		}
	}
	out := make([]Aggregate, 0, len(byKey))
	for key, value := range byKey {
		if value.Completed > 0 {
			value.MeanScore = float64(scores[key]) / float64(value.Completed)
			value.TruthRate = float64(value.TrueSolutions) / float64(value.Completed)
			value.PublishableRate = float64(value.Publishable) / float64(value.Completed)
		}
		out = append(out, *value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PublishableRate != out[j].PublishableRate {
			return out[i].PublishableRate > out[j].PublishableRate
		}
		if out[i].TruthRate != out[j].TruthRate {
			return out[i].TruthRate > out[j].TruthRate
		}
		if out[i].MeanScore != out[j].MeanScore {
			return out[i].MeanScore > out[j].MeanScore
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func addMetrics(target *result.MetricSet, source result.MetricSet) {
	target.Tokens.Requests += source.Tokens.Requests
	target.Tokens.InputTokens += source.Tokens.InputTokens
	target.Tokens.UncachedInputTokens += source.Tokens.UncachedInputTokens
	target.Tokens.CachedInputTokens += source.Tokens.CachedInputTokens
	target.Tokens.CacheWriteTokens += source.Tokens.CacheWriteTokens
	target.Tokens.OutputTokens += source.Tokens.OutputTokens
	target.Tokens.ReasoningTokens += source.Tokens.ReasoningTokens
	target.Tokens.TotalTokens += source.Tokens.TotalTokens
	target.PricedRequests += source.PricedRequests
	target.UnpricedRequests += source.UnpricedRequests
	if source.ListCost.Available {
		target.ListCost = pricing.Add(target.ListCost, source.ListCost)
	}
}

func (r Runner) loadReference(key string) (evaluation.Reference, error) {
	if !r.Resume {
		return evaluation.Reference{}, os.ErrNotExist
	}
	var value evaluation.Reference
	return value, loadJSON(r.referencePath(key), &value)
}

func (r Runner) referencePath(key string) string {
	return filepath.Join(r.OutputRoot, "references", safe(key)+".json")
}
func (r Runner) casePath(item job) string {
	return filepath.Join(r.OutputRoot, "cases", safe(item.profile.Name), string(item.mode), safe(exerciseKey(item.exercise))+".json")
}

func exerciseKey(target Exercise) string { return fmt.Sprintf("%s.%d", target.Section, target.Number) }

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safe(value string) string { return strings.Trim(unsafeName.ReplaceAllString(value, "_"), "._-") }

func sortCases(values []Case) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Model.Name != values[j].Model.Name {
			return values[i].Model.Name < values[j].Model.Name
		}
		if values[i].Mode != values[j].Mode {
			return values[i].Mode < values[j].Mode
		}
		if values[i].Exercise.Section != values[j].Exercise.Section {
			return values[i].Exercise.Section < values[j].Exercise.Section
		}
		return values[i].Exercise.Number < values[j].Exercise.Number
	})
}

func saveJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".matrix-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func loadJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func (r Runner) log(format string, args ...any) {
	if r.Progress != nil {
		r.Progress(fmt.Sprintf(format, args...))
	}
}
