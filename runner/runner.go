// Package runner turns the coverage queue into a campaign. It solves what is
// missing, publishes each proof the moment it lands, and commits the content
// repository on its own timer, for days at a time, with nobody at the terminal.
//
// The design rule throughout is that an interrupted run must leave the world in
// a state the next run can pick up from. Nothing is batched until the end: a
// solution is stored and published before the next one starts, and the queue is
// recomputed from the three directories rather than carried in memory, so a
// crash costs at most the exercise that was in flight.
package runner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tamnd/taocp-solver/coverage"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/publish"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/route"
	"github.com/tamnd/taocp-solver/solver"
)

// Defaults for the knobs a long run is actually steered by.
const (
	DefaultCommitInterval = 10 * time.Minute
	DefaultMaxSleep       = time.Hour
	// CommitTimeout bounds one whole git sequence: status, add, commit, fetch,
	// merge, push.
	CommitTimeout = 5 * time.Minute
	DefaultDrain  = 5 * time.Minute
)

// Solver is the part of the engine a run needs. It is an interface so the loop
// can be tested without a network, not because a second implementation is
// expected.
type Solver interface {
	Solve(ctx context.Context, section string, number int, options solver.Options) (result.Result, error)
}

// Publisher renders stored results into the content tree.
type Publisher interface {
	Run(targets []publish.Target, check bool) (publish.Report, error)
}

// Options is one run's configuration, already resolved from flags and
// environment.
type Options struct {
	Source string
	Output string
	Brain  string

	Volume   string
	Sections []string
	Limit    int

	Parallel       int
	Mode           solver.Mode
	Candidates     int
	MaxCorrections int

	// RetryEmpty puts exercises that already failed back in the queue. Without
	// it a stored result with no solution is a tombstone, which is what keeps a
	// week-long run from spending every cycle on the same unsolvable exercise.
	RetryEmpty bool

	NoPublish bool
	NoCommit  bool
	DryRun    bool

	CommitInterval time.Duration
	MaxSleep       time.Duration
	Drain          time.Duration
}

// Normalize fills in the defaults so a zero Options is still a working run.
func (o *Options) Normalize() {
	if o.Parallel < 1 {
		o.Parallel = 1
	}
	if o.Mode == "" {
		o.Mode = solver.ModeSlow
	}
	if o.CommitInterval <= 0 {
		o.CommitInterval = DefaultCommitInterval
	}
	if o.MaxSleep <= 0 {
		o.MaxSleep = DefaultMaxSleep
	}
	if o.Drain <= 0 {
		o.Drain = DefaultDrain
	}
}

// Summary is what the run did, printed once at the end and the only thing a
// person catching up on a weekend of output has to read.
type Summary struct {
	Queued    int            `json:"queued"`
	Attempted int            `json:"attempted"`
	Passed    int            `json:"passed"`
	Failed    int            `json:"failed"`
	Published int            `json:"published"`
	Routes    map[string]int `json:"routes,omitempty"`
	Tokens    int            `json:"tokens"`
	Cost      float64        `json:"cost_usd"`
	Commits   int            `json:"commits"`
	Files     int            `json:"files"`
	Wall      time.Duration  `json:"wall_ns"`
}

func (s Summary) String() string {
	line := fmt.Sprintf("attempted %d, passed %d, failed %d, published %d, committed %d files in %d commits, %s, %d tokens, $%.2f",
		s.Attempted, s.Passed, s.Failed, s.Published, s.Files, s.Commits, s.Wall.Round(time.Second), s.Tokens, s.Cost)
	if len(s.Routes) == 0 {
		return line
	}
	names := make([]string, 0, len(s.Routes))
	for name := range s.Routes {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s %d", name, s.Routes[name]))
	}
	return line + "\nroutes: " + joinComma(parts)
}

func joinComma(parts []string) string {
	out := ""
	for index, part := range parts {
		if index > 0 {
			out += ", "
		}
		out += part
	}
	return out
}

// Runner is a single unattended run.
type Runner struct {
	Options   Options
	Engine    Solver
	Pool      *route.Pool
	Publisher Publisher
	Committer *Committer
	Store     result.Store
	Log       func(Event)
	Now       func() time.Time
	Sleep     func(ctx context.Context, d time.Duration) error

	// publishing serialises the publisher, which rewrites index pages shared by
	// every exercise in a volume. Two workers publishing at once would race on
	// those files even though their solutions are unrelated.
	publishing sync.Mutex
	mu         sync.Mutex
	summary    Summary
}

// Queue is the work this run would do, in the order it would do it. It is
// recomputed from the source repository, the result store, and the published
// tree every time, so an interrupted run needs no cursor.
func (r *Runner) Queue() ([]coverage.Target, error) {
	scanner := coverage.New(r.Options.Source, r.Options.Output, r.Options.Brain)
	var targets []coverage.Target
	switch {
	case len(r.Options.Sections) > 0:
		for _, section := range r.Options.Sections {
			report, err := scanner.Run(coverage.Filter{Section: section})
			if err != nil {
				return nil, err
			}
			targets = append(targets, report.Queue()...)
		}
	default:
		report, err := scanner.Run(coverage.Filter{Volume: r.Options.Volume})
		if err != nil {
			return nil, err
		}
		targets = report.Queue()
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Section != targets[j].Section {
			return exercise.CompareSections(targets[i].Section, targets[j].Section) < 0
		}
		return targets[i].Number < targets[j].Number
	})
	if !r.Options.RetryEmpty {
		kept := targets[:0]
		for _, target := range targets {
			// Coverage calls an exercise missing when it has no solution. A
			// result file that exists anyway is a recorded failure, and running
			// at it again on every pass is how an unattended run stops making
			// progress.
			if r.Store.Exists(target.Section, target.Number) {
				continue
			}
			kept = append(kept, target)
		}
		targets = kept
	}
	if r.Options.Limit > 0 && len(targets) > r.Options.Limit {
		targets = targets[:r.Options.Limit]
	}
	return targets, nil
}

// Run works the queue until it is empty, the run is cancelled, or a pass makes
// no progress at all. Cancellation is not an error: the point of the drain is
// that a stopped run still commits what it finished.
func (r *Runner) Run(ctx context.Context) (Summary, error) {
	r.Options.Normalize()
	started := r.now()
	defer func() { r.summary.Wall = r.now().Sub(started) }()

	if r.Options.DryRun {
		return r.dryRun()
	}

	// Work outlives cancellation by the drain window, so an exercise that is
	// most of the way through a slow solve is not thrown away by a Ctrl-C.
	work, stopWork := context.WithCancel(context.WithoutCancel(ctx))
	finished := make(chan struct{})
	go func() {
		select {
		case <-finished:
		case <-ctx.Done():
			r.event(Event{Kind: KindSleep, Message: fmt.Sprintf("stopping, draining up to %s", r.Options.Drain)})
			select {
			case <-finished:
			case <-time.After(r.Options.Drain):
			}
		}
		stopWork()
	}()
	defer stopWork()

	var committer sync.WaitGroup
	if r.committing() {
		committer.Add(1)
		go func() {
			defer committer.Done()
			r.commitLoop(work)
		}()
	}

	var err error
	for {
		var queue []coverage.Target
		queue, err = r.Queue()
		if err != nil || len(queue) == 0 {
			break
		}
		r.event(Event{Kind: KindQueue, Files: len(queue), Message: "exercises missing"})
		r.mu.Lock()
		r.summary.Queued += len(queue)
		r.mu.Unlock()
		if r.pass(ctx, work, queue) == 0 {
			// Nothing solved, so recomputing the queue would hand back the same
			// list and the loop would spin. Stop and say so in the summary.
			break
		}
		if ctx.Err() != nil {
			break
		}
	}

	close(finished)
	stopWork()
	committer.Wait()
	if r.committing() {
		// Whatever the drain left behind goes in now. commitOnce already ignores
		// the spent context.
		r.commitOnce(ctx)
	}
	r.mu.Lock()
	summary := r.summary
	r.mu.Unlock()
	summary.Wall = r.now().Sub(started)
	return summary, err
}

func (r *Runner) dryRun() (Summary, error) {
	queue, err := r.Queue()
	if err != nil {
		return Summary{}, err
	}
	for _, target := range queue {
		r.event(Event{Kind: KindQueue, Section: target.Section, Number: target.Number, Message: "would solve"})
	}
	return Summary{Queued: len(queue)}, nil
}

// pass works one computed queue and reports how many exercises came out of it
// with a solution.
func (r *Runner) pass(ctx, work context.Context, queue []coverage.Target) int {
	targets := make(chan coverage.Target)
	go func() {
		defer close(targets)
		for _, target := range queue {
			select {
			case <-ctx.Done():
				return
			case targets <- target:
			}
		}
	}()

	var solved atomic.Int64
	var workers sync.WaitGroup
	for i := 0; i < r.Options.Parallel; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for target := range targets {
				if work.Err() != nil {
					return
				}
				if r.one(work, target) {
					solved.Add(1)
				}
			}
		}()
	}
	workers.Wait()
	return int(solved.Load())
}

// one solves and publishes a single exercise. It reports whether a solution
// came out, which is what tells the outer loop the run is still moving.
func (r *Runner) one(ctx context.Context, target coverage.Target) bool {
	r.mu.Lock()
	r.summary.Attempted++
	r.mu.Unlock()
	// A slow solve can run for hours, so the log has to say what is in flight
	// rather than only what has finished. Otherwise a run that is working looks
	// exactly like a run that has hung.
	r.event(Event{Kind: KindStart, Section: target.Section, Number: target.Number, Mode: string(r.Options.Mode)})

	value, err := r.Engine.Solve(ctx, target.Section, target.Number, solver.Options{
		Mode:           r.Options.Mode,
		Candidates:     r.Options.Candidates,
		MaxCorrections: r.Options.MaxCorrections,
	})
	if err != nil {
		if ctx.Err() != nil {
			// The drain expired mid-solve. That is a stop, not a failure.
			return false
		}
		r.mu.Lock()
		r.summary.Failed++
		r.mu.Unlock()
		r.event(Event{Kind: KindError, Section: target.Section, Number: target.Number, Message: oneLine(err)})
		r.waitForRoutes(ctx)
		return false
	}

	truth := value.Evaluation.True
	metrics := value.Metrics.CurrentRun
	cost := 0.0
	if metrics.ListCost.Available {
		cost = metrics.ListCost.TotalUSD
	}
	r.mu.Lock()
	if value.Verdict == "PASS" {
		r.summary.Passed++
	} else {
		r.summary.Failed++
	}
	r.summary.Tokens += metrics.Tokens.TotalTokens
	r.summary.Cost += cost
	for name := range value.Metrics.ByRoute {
		if r.summary.Routes == nil {
			r.summary.Routes = map[string]int{}
		}
		r.summary.Routes[name]++
	}
	served := routeOf(value)
	if len(value.Metrics.ByRoute) == 0 && served != "" {
		if r.summary.Routes == nil {
			r.summary.Routes = map[string]int{}
		}
		r.summary.Routes[served]++
	}
	r.mu.Unlock()

	r.event(Event{
		Kind: KindSolve, Section: target.Section, Number: target.Number,
		Route: served, Mode: string(r.Options.Mode), Verdict: value.Verdict, Truth: &truth,
		Elapsed: value.SolveTime.Round(time.Second).String(),
		Tokens:  metrics.Tokens.TotalTokens, Cost: cost,
	})

	if value.Solution == "" {
		return false
	}
	r.publishOne(target)
	return true
}

// publishOne renders this exercise, and only this exercise, so an interrupted
// run leaves the content repository consistent rather than holding a batch.
func (r *Runner) publishOne(target coverage.Target) {
	if r.Options.NoPublish || r.Publisher == nil {
		return
	}
	r.publishing.Lock()
	defer r.publishing.Unlock()
	report, err := r.Publisher.Run([]publish.Target{{Section: target.Section, Number: target.Number}}, false)
	if err != nil {
		r.event(Event{Kind: KindError, Section: target.Section, Number: target.Number, Message: "publish: " + oneLine(err)})
		return
	}
	message := "written"
	if report.Written == 0 {
		message = "unchanged"
	}
	if report.Deleted > 0 {
		message = "held back by the leak gate"
	}
	r.mu.Lock()
	r.summary.Published += report.Written
	r.mu.Unlock()
	r.event(Event{
		Kind: KindPublish, Section: target.Section, Number: target.Number,
		Message: message, Indexes: report.Sections + report.Volumes + report.Top,
	})
}

// waitForRoutes sleeps out a quota window. The pool has already failed over
// where it could, so reaching here means every route is cold and the only
// useful thing left is to wait for the earliest one to come back.
func (r *Runner) waitForRoutes(ctx context.Context) {
	if r.Pool == nil {
		return
	}
	earliest, name := r.Pool.EarliestReset()
	wait := earliest.Sub(r.now())
	if earliest.IsZero() || wait <= 0 {
		return
	}
	if wait > r.Options.MaxSleep {
		wait = r.Options.MaxSleep
	}
	r.event(Event{Kind: KindSleep, Route: name, Elapsed: wait.Round(time.Second).String(),
		Message: "every route is cold, waiting until " + earliest.UTC().Format(time.RFC3339)})
	_ = r.sleep(ctx, wait)
}

func (r *Runner) commitLoop(ctx context.Context) {
	ticker := time.NewTicker(r.Options.CommitInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.commitOnce(ctx)
		}
	}
}

func (r *Runner) commitOnce(ctx context.Context) {
	// A git sequence is never chopped in half. Cancellation decides whether the
	// next commit starts, not whether the one under way finishes: a stop that
	// landed between `git commit` and `git push` would leave the content
	// repository holding work the remote has never seen.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CommitTimeout)
	defer cancel()
	files, err := r.Committer.Commit(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		r.event(Event{Kind: KindError, Message: "commit: " + oneLine(err)})
	}
	if files == 0 {
		return
	}
	r.mu.Lock()
	r.summary.Commits++
	r.summary.Files += files
	r.mu.Unlock()
}

// Step logs one step of one solve. Hand it to the engine's progress hook: the
// engine is shared by every worker, so the exercise has to travel with the
// message or two parallel solves become one unreadable braid.
func (r *Runner) Step(step solver.Progress) {
	r.event(Event{Kind: KindStep, Section: step.Section, Number: step.Number, Message: step.Message})
}

// Routing logs a route changing state. Failover is the whole point of the pool
// and it used to happen silently, which left a log where a run mysteriously
// slowed down and nothing said why.
func (r *Runner) Routing(format string, args ...any) {
	r.event(Event{Kind: KindRoute, Message: fmt.Sprintf(format, args...)})
}

func (r *Runner) committing() bool {
	return !r.Options.NoCommit && r.Committer != nil
}

func (r *Runner) event(event Event) {
	if r.Log == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = r.now()
	}
	r.Log(event)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	return sleep(ctx, d)
}

// routeOf names the endpoint that answered first this run. A solve that failed
// over is reported in full by the result's by_route breakdown; the log line
// only has room for where it started.
func routeOf(value result.Result) string {
	for _, attempt := range value.Attempts {
		if attempt.CurrentRun && attempt.Route != "" {
			return attempt.Route
		}
	}
	return ""
}
