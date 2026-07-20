package solver

import (
	"context"
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

func (s *scriptedCompleter) Complete(_ context.Context, request api.Request) (api.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	text := s.responses[0]
	s.responses = s.responses[1:]
	return api.Response{ID: "id", Model: request.Model, Text: text}, nil
}

func TestSolveUsesTwoJudgesAndCorrects(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	client := &scriptedCompleter{responses: []string{
		"First solution.",
		"## Score\nSCORE: 4/7\n## Verdict\nVERDICT: FAIL",
		"## Score\nSCORE: 7/7\n## Verdict\nVERDICT: PASS",
		"Corrected solution.",
		"## Score\nSCORE: 7/7\n## Verdict\nVERDICT: PASS",
		"## Verdict\nVERDICT: PASS",
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
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.requests) != 6 {
		t.Fatalf("request count = %d", len(client.requests))
	}
	wantPhases := []string{"solve", "review-correctness", "review-process", "correct", "review-correctness", "review-process"}
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
		"Solution.",
		"SCORE: 7/7\nVERDICT: PASS",
		"VERDICT: FAIL",
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
	client := &scriptedCompleter{responses: []string{"SCORE: 7/7\nVERDICT: PASS", "VERDICT: PASS"}}
	engine := &Engine{Repository: exercise.NewRepository(root), Client: client, Store: store}
	value, err := engine.Solve(context.Background(), "1.1", 1, Options{Model: "test", Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if value.Solution != "Cached." || !value.Verified || len(client.requests) != 2 {
		t.Fatalf("result = %+v, requests = %d", value, len(client.requests))
	}
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
