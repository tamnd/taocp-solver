package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/taocp-solver/codex"
	"github.com/tamnd/taocp-solver/coverage"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/publish"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/route"
	"github.com/tamnd/taocp-solver/solver"
)

// tree builds a source repository, a result store, and a content checkout in a
// temporary directory, and returns their roots in that order.
func tree(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	return filepath.Join(root, "source"), filepath.Join(root, "solutions"), filepath.Join(root, "brain")
}

func writeExercise(t *testing.T, source, section string, number int) {
	t.Helper()
	dir := filepath.Join(source, "content", exercise.VolumeDir(section), "exercises", section)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := "---\ntitle: \"Exercise\"\nrating: 20\n---\n\nProve it.\n"
	write(t, filepath.Join(dir, fmt.Sprintf("%02d.md", number)), page)
}

func writeResult(t *testing.T, output, section string, number int, solution string) {
	t.Helper()
	store := result.Store{Root: output}
	raw, err := json.Marshal(result.Result{
		Exercise: exercise.Exercise{SectionID: section, Number: number},
		Solution: solution,
	})
	if err != nil {
		t.Fatal(err)
	}
	write(t, store.JSONPath(section, number), string(raw))
}

// fakeSolver stands in for the engine. It writes to the same store the real one
// does, because the runner recomputes its queue from disk between passes and a
// solver that only returned a value in memory would loop forever.
type fakeSolver struct {
	store  result.Store
	answer func(section string, number int) (string, error)

	mu   sync.Mutex
	seen []coverage.Target
	hold chan struct{}
}

func (f *fakeSolver) Solve(ctx context.Context, section string, number int, options solver.Options) (result.Result, error) {
	f.mu.Lock()
	f.seen = append(f.seen, coverage.Target{Section: section, Number: number})
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return result.Result{}, ctx.Err()
		}
	}
	solution := "A proof.\n"
	var err error
	if f.answer != nil {
		solution, err = f.answer(section, number)
	}
	value := result.Result{
		Exercise: exercise.Exercise{SectionID: section, Number: number},
		Solution: solution, Verdict: "PASS", SolveTime: time.Minute,
		Attempts: []result.Attempt{{Phase: "solve", Route: "test-route", CurrentRun: true}},
	}
	value.Evaluation.True = solution != ""
	if err != nil {
		return result.Result{}, err
	}
	if saveErr := f.store.Save(value); saveErr != nil {
		return result.Result{}, saveErr
	}
	return value, nil
}

func (f *fakeSolver) targets() []coverage.Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]coverage.Target(nil), f.seen...)
}

// fakePublisher records what it was asked to render without touching disk.
type fakePublisher struct {
	mu   sync.Mutex
	seen []publish.Target
}

func (p *fakePublisher) Run(targets []publish.Target, check bool) (publish.Report, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, targets...)
	return publish.Report{Written: len(targets), Sections: 1}, nil
}

func (p *fakePublisher) targets() []publish.Target {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]publish.Target(nil), p.seen...)
}

func newRunner(t *testing.T, options Options) (*Runner, *fakeSolver, *fakePublisher) {
	t.Helper()
	store := result.Store{Root: options.Output}
	engine := &fakeSolver{store: store}
	publisher := &fakePublisher{}
	return &Runner{
		Options: options, Store: store, Engine: engine, Publisher: publisher,
		Log:   func(Event) {},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}, engine, publisher
}

func TestTheQueueIsWhatCoverageCallsMissing(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	for _, number := range []int{1, 2, 3} {
		writeExercise(t, source, "1.1", number)
	}
	writeResult(t, output, "1.1", 1, "solved already")

	run, _, _ := newRunner(t, Options{Source: source, Output: output, Brain: brain, NoCommit: true})
	queue, err := run.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if got := targetText(queue); got != "1.1/2 1.1/3" {
		t.Fatalf("queue = %q, want the two unsolved exercises", got)
	}
}

func TestAPublishedExerciseWithNoStoredResultStaysOutOfTheQueue(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	writeExercise(t, source, "1.1", 1)
	writeExercise(t, source, "1.1", 2)
	// Thousands of proofs predate the store. Queueing them would spend a model
	// on work that is already published.
	write(t, filepath.Join(brain, "content/en/practice/maths/taocp/vol1/1.1/01.md"), "---\ntitle: x\n---\n\nA proof.\n")

	run, _, _ := newRunner(t, Options{Source: source, Output: output, Brain: brain, NoCommit: true})
	queue, err := run.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if got := targetText(queue); got != "1.1/2" {
		t.Fatalf("queue = %q, want only the unpublished exercise", got)
	}
}

func TestARecordedFailureIsSkippedUnlessRetryEmptyAsksForIt(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	writeExercise(t, source, "1.1", 1)
	writeExercise(t, source, "1.1", 2)
	// An empty solution is a failed attempt. Coverage still calls it missing,
	// but a week-long run must not spend every pass on it.
	writeResult(t, output, "1.1", 1, "")

	options := Options{Source: source, Output: output, Brain: brain, NoCommit: true}
	run, _, _ := newRunner(t, options)
	queue, err := run.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if got := targetText(queue); got != "1.1/2" {
		t.Fatalf("queue = %q, want the failure skipped", got)
	}

	options.RetryEmpty = true
	retry, _, _ := newRunner(t, options)
	queue, err = retry.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if got := targetText(queue); got != "1.1/1 1.1/2" {
		t.Fatalf("queue with --retry-empty = %q, want the failure back", got)
	}
}

func TestVolumeSectionAndLimitNarrowTheQueue(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	for _, number := range []int{1, 2, 3} {
		writeExercise(t, source, "1.1", number)
		writeExercise(t, source, "5.1", number)
	}

	base := Options{Source: source, Output: output, Brain: brain, NoCommit: true}

	byVolume := base
	byVolume.Volume = "vol1"
	run, _, _ := newRunner(t, byVolume)
	queue, err := run.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if got := targetText(queue); got != "1.1/1 1.1/2 1.1/3" {
		t.Fatalf("--volume vol1 queue = %q", got)
	}

	bySection := base
	bySection.Sections = []string{"5.1"}
	run, _, _ = newRunner(t, bySection)
	queue, err = run.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if got := targetText(queue); got != "5.1/1 5.1/2 5.1/3" {
		t.Fatalf("--section 5.1 queue = %q", got)
	}

	limited := base
	limited.Limit = 2
	run, _, _ = newRunner(t, limited)
	queue, err = run.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 2 {
		t.Fatalf("--limit 2 gave %d targets", len(queue))
	}
}

func TestADryRunWritesNothing(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	writeExercise(t, source, "1.1", 1)

	var events []Event
	run, engine, publisher := newRunner(t, Options{Source: source, Output: output, Brain: brain, DryRun: true, NoCommit: true})
	run.Log = func(event Event) { events = append(events, event) }

	summary, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Queued != 1 || summary.Attempted != 0 {
		t.Fatalf("summary = %+v, want one queued and nothing attempted", summary)
	}
	if len(engine.targets()) != 0 || len(publisher.targets()) != 0 {
		t.Fatal("a dry run solved or published something")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("a dry run created the result store: %v", err)
	}
	if len(events) != 1 || events[0].Kind != KindQueue {
		t.Fatalf("events = %+v, want one queue line", events)
	}
}

func TestARunSolvesEveryMissingExerciseAndPublishesEachOne(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	for _, number := range []int{1, 2, 3} {
		writeExercise(t, source, "1.1", number)
	}

	run, engine, publisher := newRunner(t, Options{
		Source: source, Output: output, Brain: brain, Parallel: 2, NoCommit: true,
	})
	summary, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Attempted != 3 || summary.Passed != 3 || summary.Published != 3 {
		t.Fatalf("summary = %+v, want three solved and three published", summary)
	}
	if len(engine.targets()) != 3 {
		t.Fatalf("solved %d exercises, want 3", len(engine.targets()))
	}
	if len(publisher.targets()) != 3 {
		t.Fatalf("published %d exercises, want 3", len(publisher.targets()))
	}
	if summary.Routes["test-route"] != 3 {
		t.Fatalf("routes = %v, want three on test-route", summary.Routes)
	}
}

func TestAPassThatSolvesNothingStopsRatherThanSpinning(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	writeExercise(t, source, "1.1", 1)

	run, engine, _ := newRunner(t, Options{
		Source: source, Output: output, Brain: brain, RetryEmpty: true, NoCommit: true,
	})
	// Every attempt fails, and --retry-empty keeps the exercise in the queue, so
	// a loop that trusted the recomputed queue alone would never end.
	engine.answer = func(string, int) (string, error) { return "", fmt.Errorf("the route is broken") }

	done := make(chan Summary, 1)
	go func() {
		summary, err := run.Run(context.Background())
		if err != nil {
			t.Error(err)
		}
		done <- summary
	}()
	select {
	case summary := <-done:
		if summary.Attempted != 1 || summary.Failed != 1 {
			t.Fatalf("summary = %+v, want one attempt and one failure", summary)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run never stopped")
	}
}

func TestEveryRouteColdSleepsNoLongerThanMaxSleep(t *testing.T) {
	t.Parallel()
	source, output, brain := tree(t)
	writeExercise(t, source, "1.1", 1)

	run, engine, _ := newRunner(t, Options{
		Source: source, Output: output, Brain: brain, NoCommit: true,
		MaxSleep: 30 * time.Minute,
	})
	engine.answer = func(string, int) (string, error) { return "", fmt.Errorf("every route is cold") }
	run.Pool = coldPool(t, 4*time.Hour)

	var waited time.Duration
	var events []Event
	var mu sync.Mutex
	run.Log = func(event Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	run.Sleep = func(_ context.Context, d time.Duration) error {
		waited = d
		return nil
	}

	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waited != 30*time.Minute {
		t.Fatalf("slept %s, want the 30m cap rather than the 4h reset", waited)
	}
	if !hasKind(events, KindSleep) {
		t.Fatalf("no sleep line in %+v", events)
	}
}

func TestAStopSignalDrainsTheWorkInFlightAndCommitsOnce(t *testing.T) {
	t.Parallel()
	source, output, _ := tree(t)
	repo, remote := scratch(t)
	for _, number := range []int{1, 2, 3, 4} {
		writeExercise(t, source, "1.1", number)
	}

	run, engine, _ := newRunner(t, Options{
		Source: source, Output: output, Brain: repo, Parallel: 1,
		Drain: 5 * time.Second, CommitInterval: time.Hour,
	})
	// The publisher writes into the same working copy the committer commits, so
	// the drain has something real to save.
	run.Publisher = publish.New(repo, source, result.Store{Root: output})
	run.Committer = committerFor(t, repo)

	held := make(chan struct{})
	engine.hold = held

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan Summary, 1)
	go func() {
		summary, err := run.Run(ctx)
		if err != nil {
			t.Error(err)
		}
		done <- summary
	}()

	// Wait until the first exercise is in flight, then stop the run.
	for len(engine.targets()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	close(held)

	var summary Summary
	select {
	case summary = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the run did not drain")
	}
	if summary.Attempted == 0 {
		t.Fatal("nothing was attempted")
	}
	if summary.Commits != 1 {
		t.Fatalf("commits = %d, want exactly one final commit", summary.Commits)
	}
	log := mustGit(t, context.Background(), remote, "log", "--format=%s", "-n", "1")
	if !strings.Contains(log, "[auto]") {
		t.Fatalf("the drained work never reached the remote: %q", strings.TrimSpace(log))
	}
}

func targetText(targets []coverage.Target) string {
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		parts = append(parts, fmt.Sprintf("%s/%d", target.Section, target.Number))
	}
	return strings.Join(parts, " ")
}

func hasKind(events []Event, kind string) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

// coldPool builds a pool whose only route is out of quota for the given window,
// which is what the runner has to wait out.
func coldPool(t *testing.T, window time.Duration) *route.Pool {
	t.Helper()
	pool := route.NewPool(route.Registry{Routes: []route.Route{{
		Name: "test-route", Wire: route.WireChat, BaseURL: "http://127.0.0.1:1/v1", Model: "test",
	}}})
	pool.Fail("test-route", &codex.QuotaError{ResetsAt: time.Now().Add(window), Message: "usage limit reached"})
	return pool
}
