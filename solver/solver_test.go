package solver

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/result"
)

type scriptedCompleter struct {
	mu        sync.Mutex
	responses []string
	requests  []api.Request
}

const passingTruthReview = "SCORE: 7/7\nTRUTH: TRUE\nCOMPLETE: YES\nSELF_CONTAINED: YES\nHUMAN_READABLE: YES\nVERIFIABLE: YES\nVERDICT: PASS"

const failingTruthReview = "SCORE: 4/7\nTRUTH: FALSE\nCOMPLETE: NO\nSELF_CONTAINED: YES\nHUMAN_READABLE: YES\nVERIFIABLE: NO\nVERDICT: FAIL"

func (s *scriptedCompleter) Complete(_ context.Context, request api.Request) (api.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	text := s.responses[0]
	s.responses = s.responses[1:]
	return api.Response{
		ID: "id", Model: request.Model, Text: text,
		Usage: api.Usage{InputTokens: 100, CachedInputTokens: 20, CacheWriteTokens: 10, OutputTokens: 30, ReasoningTokens: 5, TotalTokens: 130},
	}, nil
}

func TestSolveUsesTwoJudgesAndCorrects(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	client := &scriptedCompleter{responses: []string{
		"Independent reference.",
		"First solution.",
		failingTruthReview,
		"TRUTH: TRUE\nVERDICT: PASS",
		"Corrected solution.",
		passingTruthReview,
		"TRUTH: TRUE\nVERDICT: PASS",
	}}
	store := result.Store{Root: filepath.Join(t.TempDir(), "out")}
	engine := &Engine{Repository: exercise.NewRepository(root), Client: client, Store: store}
	value, err := engine.Solve(context.Background(), "1.1", 1, Options{Model: "test", Verify: true, MaxCorrections: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !value.Verified || value.Solution != "Corrected solution." || len(value.Reviews) != 2 {
		t.Fatalf("result = %+v", value)
	}
	if !value.Evaluation.True || value.Evaluation.Verdict != "TRUE" || value.Metrics.CurrentRun.Tokens.TotalTokens != 7*130 {
		t.Fatalf("evaluation or metrics = %+v, %+v", value.Evaluation, value.Metrics)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.requests) != 7 {
		t.Fatalf("request count = %d", len(client.requests))
	}
	wantPhases := []string{"reference", "solve-candidate", "review-correctness", "review-process", "correct", "review-correctness", "review-process"}
	for i, want := range wantPhases {
		if got := client.requests[i].Metadata["phase"]; got != want {
			t.Errorf("phase %d = %q, want %q", i, got, want)
		}
	}
}

func TestSolveRequiresBothJudges(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	client := &scriptedCompleter{responses: []string{
		"Independent reference.",
		"Solution.",
		passingTruthReview,
		"TRUTH: FALSE\nVERDICT: FAIL",
	}}
	engine := &Engine{
		Repository: exercise.NewRepository(root), Client: client,
		Store: result.Store{Root: filepath.Join(t.TempDir(), "out")},
	}
	value, err := engine.Solve(context.Background(), "1.1", 1, Options{Model: "test", Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if value.Verified || value.Verdict != "FAIL" {
		t.Fatalf("result = %+v", value)
	}
}

func TestSolveReusesCachedSolution(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	store := result.Store{Root: filepath.Join(t.TempDir(), "out")}
	cached := result.Result{Exercise: exercise.Exercise{SectionID: "1.1", Number: 1}, Solution: "Cached.", Verdict: "SKIP"}
	if err := store.Save(cached); err != nil {
		t.Fatal(err)
	}
	client := &scriptedCompleter{responses: []string{"Independent reference.", passingTruthReview, "TRUTH: TRUE\nVERDICT: PASS"}}
	engine := &Engine{Repository: exercise.NewRepository(root), Client: client, Store: store}
	value, err := engine.Solve(context.Background(), "1.1", 1, Options{Model: "test", Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if value.Solution != "Cached." || !value.Verified || len(client.requests) != 3 {
		t.Fatalf("result = %+v, requests = %d", value, len(client.requests))
	}
	if value.Metrics.CurrentRun.Tokens.Requests != 3 || value.Metrics.Cumulative.Tokens.Requests != 3 {
		t.Fatalf("metrics = %+v", value.Metrics)
	}
}

func TestSolveSelectsFromCandidatePopulation(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	client := &scriptedCompleter{responses: []string{
		"Independent reference.", "Candidate one.", "Candidate two.",
		"## Selection\nSELECTED: 2", passingTruthReview, "TRUTH: TRUE\nVERDICT: PASS",
	}}
	engine := &Engine{
		Repository: exercise.NewRepository(root), Client: client,
		Store: result.Store{Root: filepath.Join(t.TempDir(), "out")},
	}
	value, err := engine.Solve(context.Background(), "1.1", 1, Options{Model: "test", Verify: true, Candidates: 2})
	if err != nil {
		t.Fatal(err)
	}
	if value.Solution != "Candidate two." || value.Selected != 2 || len(value.Candidates) != 2 || !strings.Contains(value.Selection, "SELECTED: 2") {
		t.Fatalf("result = %+v", value)
	}
}

func TestSolveCalculatesOfficialListCost(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	client := &scriptedCompleter{responses: []string{"Solution."}}
	engine := &Engine{
		Repository: exercise.NewRepository(root), Client: client,
		Store: result.Store{Root: filepath.Join(t.TempDir(), "out")},
	}
	value, err := engine.Solve(context.Background(), "1.1", 1, Options{Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	metrics := value.Metrics.CurrentRun
	// 70 fresh input, 20 cached input, 10 cache-write input, and 30 output.
	want := 70*5.0/1e6 + 20*0.5/1e6 + 10*6.25/1e6 + 30*30.0/1e6
	if metrics.PricedRequests != 1 || math.Abs(metrics.ListCost.TotalUSD-want) > 1e-12 {
		t.Fatalf("metrics = %+v, want cost %.9f", metrics, want)
	}
}

func TestFastModeUsesOneCallAndSkipsVerification(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	client := &scriptedCompleter{responses: []string{"Fast solution."}}
	engine := &Engine{
		Repository: exercise.NewRepository(root), Client: client,
		Store: result.Store{Root: filepath.Join(t.TempDir(), "out")},
	}
	value, err := engine.Solve(context.Background(), "1.1", 1, Options{
		Mode: ModeFast, Model: "test", Verify: true, Candidates: 5, MaxCorrections: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || value.Evaluation.Verdict != "SKIPPED" || value.Verified || len(value.Candidates) != 1 {
		t.Fatalf("result = %+v, requests = %d", value, len(client.requests))
	}
}

// A run that failed over records what answered, not what it asked for.
func TestSolveRecordsTheModelsThatAnswered(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	client := &substituteCompleter{
		texts:  []string{"Reference.", "Solution.", passingTruthReview, "TRUTH: TRUE\nVERDICT: PASS"},
		models: []string{"nemotron-3-ultra-free", "gpt-5.6-luna"},
	}
	engine := &Engine{
		Repository: exercise.NewRepository(root), Client: client,
		Store: result.Store{Root: filepath.Join(t.TempDir(), "out")},
	}
	value, err := engine.Solve(context.Background(), "1.1", 1,
		Options{Model: "asked-for", Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if value.Model != "nemotron-3-ultra-free, gpt-5.6-luna" {
		t.Errorf("model = %q, want both models that answered", value.Model)
	}
}

func TestSolveFallsBackToTheRequestedModel(t *testing.T) {
	t.Parallel()
	if got := servedModels(nil, "asked-for"); got != "asked-for" {
		t.Errorf("model = %q", got)
	}
}

// substituteCompleter answers with a different model each time, the way a route
// pool does when it fails over mid-run.
type substituteCompleter struct {
	mu     sync.Mutex
	texts  []string
	models []string
	calls  int
}

func (s *substituteCompleter) Complete(_ context.Context, request api.Request) (api.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	model := request.Model
	text := s.texts[min(s.calls, len(s.texts)-1)]
	s.calls++
	if len(s.models) > 0 {
		// Once the substitutes run out the last one keeps answering, the way a
		// pool stays on the route it failed over to.
		model = s.models[min(s.calls-1, len(s.models)-1)]
	}
	return api.Response{ID: "id", Model: model, Text: text}, nil
}

func fixtureRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "content", "vol1", "exercises", "1.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01.md"), []byte("---\nsection: 1.1\nexercise: 1\nrating: 10\n---\nProve the claim."), 0o644); err != nil {
		t.Fatal(err)
	}
	section := filepath.Join(root, "content", "vol1", "01_1_1_test.md")
	if err := os.WriteFile(section, []byte(strings.Repeat("Context. ", 4)), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
