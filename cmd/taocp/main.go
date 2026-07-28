package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/benchmark"
	"github.com/tamnd/taocp-solver/codex"
	"github.com/tamnd/taocp-solver/config"
	"github.com/tamnd/taocp-solver/coverage"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/matrix"
	"github.com/tamnd/taocp-solver/prompt"
	"github.com/tamnd/taocp-solver/publish"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/route"
	"github.com/tamnd/taocp-solver/runner"
	"github.com/tamnd/taocp-solver/solver"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usage(stdout)
	}
	switch args[0] {
	case "help", "-h", "--help":
		return usage(stdout)
	case "version":
		_, err := fmt.Fprintf(stdout, "taocp %s (%s, %s)\n", version, commit, date)
		return err
	case "solve":
		return runSolve(ctx, args[1:], stdout, stderr)
	case "batch":
		return runBatch(ctx, args[1:], stdout, stderr)
	case "prompt":
		return runPrompt(args[1:], stdout, stderr)
	case "review":
		return runReview(ctx, args[1:], stdout, stderr)
	case "benchmark":
		return runBenchmark(ctx, args[1:], stdout, stderr)
	case "matrix":
		return runMatrix(ctx, args[1:], stdout, stderr)
	case "bridge":
		return runBridge(ctx, args[1:], stdout, stderr)
	case "publish":
		return runPublish(args[1:], stdout, stderr)
	case "coverage":
		return runCoverage(args[1:], stdout, stderr)
	case "run":
		return runRun(ctx, args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run taocp help", args[0])
	}
}

func runMatrix(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	defaults := config.FromEnv()
	fs := pflag.NewFlagSet("matrix", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "", "JSON manifest; defaults to the built-in model and exercise matrix")
	output := fs.String("output", filepath.Join(defaults.OutputRoot, "matrix"), "matrix report directory")
	source := fs.String("source", defaults.TAOCPRoot, "TAOCP source repository")
	timeoutText := fs.String("timeout", defaults.Timeout.String(), "timeout per model request")
	retries := fs.Int("retries", defaults.MaxRetries, "retries for transient API failures")
	maxOutputTokens := fs.Int("max-output-tokens", 32768, "maximum completion tokens per matrix request")
	deferredRateLimitRetries := fs.Int("deferred-rate-limit-retries", 1, "retry rate-limited cases after all other cases finish")
	parallel := fs.Int("parallel", defaults.Parallel, "parallel model and exercise cases")
	resume := fs.Bool("resume", false, "reuse completed cases and fixed references")
	models := fs.String("models", "", "comma-separated profile names to run")
	modes := fs.String("modes", "", "override profile modes with fast, slow, or fast,slow")
	candidates := fs.Int("candidates", defaults.Candidates, "slow-mode candidate population")
	corrections := fs.Int("max-corrections", defaults.MaxCorrections, "slow-mode correction passes")
	writeManifest := fs.String("write-manifest", "", "write the effective manifest and exit")
	evaluatorURL := fs.String("evaluator-base-url", "", "override the fixed evaluator base URL")
	evaluatorModel := fs.String("evaluator-model", "", "override the fixed evaluator model")
	evaluatorKeyEnv := fs.String("evaluator-api-key-env", "", "environment variable holding the evaluator API key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("matrix takes no positional arguments")
	}
	if *maxOutputTokens < 1 {
		return errors.New("max-output-tokens must be positive")
	}
	manifest := matrix.DefaultManifest()
	var err error
	if *manifestPath != "" {
		manifest, err = matrix.LoadManifest(*manifestPath)
		if err != nil {
			return err
		}
	}
	if *evaluatorURL != "" {
		manifest.Evaluator.BaseURL = *evaluatorURL
	}
	if *evaluatorModel != "" {
		manifest.Evaluator.Model = *evaluatorModel
	}
	if *evaluatorKeyEnv != "" {
		manifest.Evaluator.APIKeyEnv = *evaluatorKeyEnv
	}
	if *models != "" {
		wanted := map[string]bool{}
		for _, name := range strings.Split(*models, ",") {
			wanted[strings.TrimSpace(name)] = true
		}
		var selected []matrix.ModelProfile
		for _, profile := range manifest.Models {
			if wanted[profile.Name] {
				selected = append(selected, profile)
				delete(wanted, profile.Name)
			}
		}
		if len(wanted) != 0 {
			return fmt.Errorf("unknown matrix model profiles: %v", mapKeys(wanted))
		}
		manifest.Models = selected
	}
	if *modes != "" {
		var selected []solver.Mode
		for _, value := range strings.Split(*modes, ",") {
			mode, err := parseMode(value)
			if err != nil {
				return err
			}
			selected = append(selected, mode)
		}
		for index := range manifest.Models {
			manifest.Models[index].Modes = selected
		}
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	if *writeManifest != "" {
		raw, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		return os.WriteFile(*writeManifest, raw, 0o644)
	}
	timeout, err := config.ParseDuration(*timeoutText)
	if err != nil {
		return err
	}
	var progressMu sync.Mutex
	runner := matrix.Runner{
		Manifest: manifest, Repository: exercise.NewRepository(*source), OutputRoot: *output,
		Timeout: timeout, MaxRetries: *retries, MaxOutputTokens: *maxOutputTokens, Parallel: *parallel, Resume: *resume,
		Candidates: *candidates, MaxCorrections: *corrections, DeferredRateLimitRetries: *deferredRateLimitRetries,
		Progress: func(message string) {
			progressMu.Lock()
			defer progressMu.Unlock()
			_, _ = fmt.Fprintln(stderr, message)
		},
	}
	report, err := runner.Run(ctx)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", filepath.Join(*output, "REPORT.md")); err != nil {
		return err
	}
	for _, item := range report.Aggregates {
		cost := "n/a"
		if item.GenerationListCost.Available &&
			(item.GenerationMetrics.Tokens.CachedInputTokens == 0 || item.PublishedListPrice.CachedInputPriceAvailable) &&
			(item.GenerationMetrics.Tokens.CacheWriteTokens == 0 || item.PublishedListPrice.CacheWritePriceAvailable) {
			cost = fmt.Sprintf("$%.6f", item.GenerationListCost.TotalUSD)
		}
		if _, err := fmt.Fprintf(stdout, "%s %s: %d/%d publishable, %d/%d true, %d tokens, %s paid-equivalent generation\n",
			item.Model, item.Mode, item.Publishable, item.Completed, item.TrueSolutions, item.Completed,
			item.GenerationMetrics.Tokens.TotalTokens, cost); err != nil {
			return err
		}
	}
	return nil
}

func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type commonFlags struct {
	config      config.Config
	timeoutText string
	routesFile  string
	routeNames  []string
	// flags is kept so the run can tell an explicit --base-url from the same
	// value arriving out of the environment.
	flags *pflag.FlagSet
}

// routing reports whether the caller asked for the route registry. A bare
// --base-url keeps its old single-endpoint behaviour, because breaking every
// existing command line to gain failover would be a poor trade.
func (c *commonFlags) routing() bool {
	return strings.TrimSpace(c.routesFile) != "" || len(c.routeNames) > 0
}

func bindCommon(fs *pflag.FlagSet, defaults config.Config) *commonFlags {
	c := &commonFlags{config: defaults, timeoutText: defaults.Timeout.String(), flags: fs}
	fs.StringVar(&c.routesFile, "routes", "", "route file to run against; enables failover across routes")
	fs.StringSliceVar(&c.routeNames, "route", nil, "restrict the run to these routes, tried in the order given")
	fs.StringVar(&c.config.BaseURL, "base-url", defaults.BaseURL, "bridge or proxy base URL")
	fs.StringVar(&c.config.APIKey, "api-key", defaults.APIKey, "API key, if required by the endpoint")
	fs.StringVar(&c.config.Model, "model", defaults.Model, "model name")
	fs.StringVar(&c.config.TAOCPRoot, "source", defaults.TAOCPRoot, "TAOCP source repository")
	fs.StringVar(&c.config.OutputRoot, "output", defaults.OutputRoot, "solution output directory")
	fs.StringVar(&c.timeoutText, "timeout", defaults.Timeout.String(), "timeout per API request")
	fs.IntVar(&c.config.MaxRetries, "retries", defaults.MaxRetries, "retries for transient API failures")
	return c
}

func (c *commonFlags) finish(requireAPI bool) error {
	timeout, err := config.ParseDuration(c.timeoutText)
	if err != nil {
		return err
	}
	c.config.Timeout = timeout
	c.config.Normalize()
	// With routing on there is nothing for --base-url to point at yet, so the
	// endpoint requirement moves to the route file.
	return c.config.Validate(requireAPI && !c.routing())
}

// pool builds the route pool for a run, or nil when the caller is using a
// single endpoint.
func (c *commonFlags) pool(stderr io.Writer) (*route.Pool, error) {
	if !c.routing() {
		return nil, nil
	}
	registry, source, err := route.LoadOrDefault(c.routesFile)
	if err != nil {
		return nil, err
	}
	if len(c.routeNames) > 0 {
		registry, err = registry.Select(c.routeNames)
		if err != nil {
			return nil, err
		}
	}
	// An explicit --base-url alongside a route file is a deliberate instruction
	// to try that endpoint first, so it becomes a rank 0 route rather than being
	// silently ignored. The same value arriving from the environment is not,
	// which is why this reads the flag rather than the config.
	if c.flags != nil && c.flags.Changed("base-url") && strings.TrimSpace(c.config.BaseURL) != "" {
		adHoc := route.AdHoc(c.config.BaseURL, c.config.Model, "", "")
		adHoc.APIKey = c.config.APIKey
		registry.Routes = append([]route.Route{adHoc}, registry.Routes...)
	}
	// Each route names its own model, so --model does not reach the wire here.
	// Labelling the run with the first route's model keeps the progress lines
	// from announcing a model that nothing was ever asked for.
	if enabled := registry.Enabled(); len(enabled) > 0 {
		c.config.Model = enabled[0].Model
	}
	_, _ = fmt.Fprintf(stderr, "routing across %s from %s\n", strings.Join(registry.Names(), ", "), source)
	pool := route.NewPool(registry)
	pool.Timeout = c.config.Timeout
	pool.MaxRetries = c.config.MaxRetries
	pool.Prober = route.Prober{Timeout: 60 * time.Second}
	pool.Logf = func(format string, args ...any) { _, _ = fmt.Fprintf(stderr, format+"\n", args...) }
	return pool, nil
}

func runSolve(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := pflag.NewFlagSet("solve", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	common := bindCommon(fs, config.FromEnv())
	mode := fs.String("mode", string(solver.ModeSlow), "solve mode: fast or slow")
	force := fs.Bool("force", false, "ignore a cached solution")
	jsonOutput := fs.Bool("json", false, "write the full result as JSON")
	fs.IntVar(&common.config.MaxCorrections, "max-corrections", common.config.MaxCorrections, "maximum correction passes")
	fs.IntVar(&common.config.Candidates, "candidates", common.config.Candidates, "independent solution candidates, 1 to 5")
	if err := fs.Parse(args); err != nil {
		return err
	}
	section, number, err := parseTarget(fs.Args())
	if err != nil {
		return err
	}
	if err := common.finish(true); err != nil {
		return err
	}
	pool, err := common.pool(stderr)
	if err != nil {
		return err
	}
	engine := newEngine(common.config, pool, stderr)
	solveMode, err := parseMode(*mode)
	if err != nil {
		return err
	}
	value, err := engine.Solve(ctx, section, number, solver.Options{
		Mode: solveMode, Model: common.config.Model, Force: *force,
		MaxCorrections: common.config.MaxCorrections,
		Candidates:     common.config.Candidates,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, value)
	}
	if _, err := fmt.Fprintln(stdout, engine.Store.MarkdownPath(section, number)); err != nil {
		return err
	}
	if _, err = fmt.Fprintf(stderr, "verdict: %s, truth: %t, elapsed: %s\n", value.Verdict, value.Evaluation.True, value.SolveTime); err != nil {
		return err
	}
	return printMetrics(stderr, value.Metrics.CurrentRun)
}

func runBatch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := pflag.NewFlagSet("batch", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	common := bindCommon(fs, config.FromEnv())
	mode := fs.String("mode", string(solver.ModeSlow), "solve mode: fast or slow")
	force := fs.Bool("force", false, "solve exercises that already have cached results")
	jsonOutput := fs.Bool("json", false, "write results as a JSON array")
	fs.IntVar(&common.config.Parallel, "parallel", common.config.Parallel, "parallel exercises")
	fs.IntVar(&common.config.MaxCorrections, "max-corrections", common.config.MaxCorrections, "maximum correction passes")
	fs.IntVar(&common.config.Candidates, "candidates", common.config.Candidates, "independent solution candidates per exercise, 1 to 5")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) == 0 {
		return errors.New("batch requires at least one section")
	}
	if err := common.finish(true); err != nil {
		return err
	}
	solveMode, err := parseMode(*mode)
	if err != nil {
		return err
	}
	pool, err := common.pool(stderr)
	if err != nil {
		return err
	}
	engine := newEngine(common.config, pool, stderr)
	type task struct {
		section string
		number  int
	}
	var tasks []task
	for _, section := range fs.Args() {
		numbers, err := engine.Repository.List(section)
		if err != nil {
			return err
		}
		for _, number := range numbers {
			if !*force && engine.Store.Exists(section, number) {
				continue
			}
			tasks = append(tasks, task{section: section, number: number})
		}
	}
	if len(tasks) == 0 {
		_, err := fmt.Fprintln(stderr, "all selected exercises already have results")
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan task)
	values := make(chan result.Result, len(tasks))
	errs := make(chan error, 1)
	var workers sync.WaitGroup
	for range common.config.Parallel {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				value, err := engine.Solve(ctx, item.section, item.number, solver.Options{
					Mode: solveMode, Model: common.config.Model, Force: *force,
					MaxCorrections: common.config.MaxCorrections,
					Candidates:     common.config.Candidates,
				})
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					cancel()
					return
				}
				values <- value
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, item := range tasks {
			select {
			case jobs <- item:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(values)
	}()

	results := make([]result.Result, 0, len(tasks))
	for value := range values {
		results = append(results, value)
		if !*jsonOutput {
			if _, err := fmt.Fprintln(stdout, engine.Store.MarkdownPath(value.Exercise.SectionID, value.Exercise.Number)); err != nil {
				return err
			}
		}
	}
	select {
	case err := <-errs:
		return err
	default:
	}
	if *jsonOutput {
		return writeJSON(stdout, results)
	}
	return nil
}

func runPrompt(args []string, stdout, stderr io.Writer) error {
	fs := pflag.NewFlagSet("prompt", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	common := bindCommon(fs, config.FromEnv())
	jsonOutput := fs.Bool("json", false, "write instructions and input as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	section, number, err := parseTarget(fs.Args())
	if err != nil {
		return err
	}
	if err := common.finish(false); err != nil {
		return err
	}
	repo := exercise.NewRepository(common.config.TAOCPRoot)
	ex, sourceContext, err := repo.Load(section, number)
	if err != nil {
		return err
	}
	instructions, input := (prompt.Builder{}).Solve(ex, sourceContext)
	if *jsonOutput {
		return writeJSON(stdout, map[string]string{"instructions": instructions, "input": input})
	}
	_, err = fmt.Fprintf(stdout, "%s\n\n%s\n", instructions, input)
	return err
}

func runReview(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := pflag.NewFlagSet("review", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	common := bindCommon(fs, config.FromEnv())
	file := fs.String("file", "-", "solution Markdown file, or - for standard input")
	jsonOutput := fs.Bool("json", false, "write review and verdict as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	section, number, err := parseTarget(fs.Args())
	if err != nil {
		return err
	}
	if err := common.finish(true); err != nil {
		return err
	}
	var data []byte
	if *file == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(*file)
	}
	if err != nil {
		return fmt.Errorf("read solution: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return errors.New("solution is empty")
	}
	pool, err := common.pool(stderr)
	if err != nil {
		return err
	}
	engine := newEngine(common.config, pool, stderr)
	review, verdict, err := engine.Review(ctx, section, number, string(data), common.config.Model)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, map[string]string{"review": review, "verdict": verdict})
	}
	_, err = fmt.Fprintln(stdout, review)
	return err
}

func runBenchmark(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := pflag.NewFlagSet("benchmark", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	common := bindCommon(fs, config.FromEnv())
	jsonOutput := fs.Bool("json", false, "write the complete benchmark report as JSON")
	reuse := fs.Bool("reuse", false, "reuse saved mode results and rerun only blind quality evaluation")
	fs.IntVar(&common.config.MaxCorrections, "max-corrections", common.config.MaxCorrections, "maximum slow-mode correction passes")
	fs.IntVar(&common.config.Candidates, "candidates", common.config.Candidates, "slow-mode candidates, 1 to 5")
	if err := fs.Parse(args); err != nil {
		return err
	}
	section, number, err := parseTarget(fs.Args())
	if err != nil {
		return err
	}
	if err := common.finish(true); err != nil {
		return err
	}
	pool, err := common.pool(stderr)
	if err != nil {
		return err
	}
	engine := newEngine(common.config, pool, stderr)
	runner := benchmark.Runner{
		Repository: engine.Repository, Client: engine.Client,
		OutputRoot: filepath.Join(common.config.OutputRoot, "benchmarks", fmt.Sprintf("%s-%d", section, number)),
		Model:      common.config.Model, Candidates: common.config.Candidates,
		MaxCorrections: common.config.MaxCorrections, Progress: engine.Progress,
	}
	var report benchmark.Report
	if *reuse {
		fast, loadErr := (result.Store{Root: filepath.Join(runner.OutputRoot, "fast")}).Load(section, number)
		if loadErr != nil {
			return fmt.Errorf("load fast benchmark result: %w", loadErr)
		}
		slow, loadErr := (result.Store{Root: filepath.Join(runner.OutputRoot, "slow")}).Load(section, number)
		if loadErr != nil {
			return fmt.Errorf("load slow benchmark result: %w", loadErr)
		}
		report, err = runner.Compare(ctx, section, number, fast, slow)
	} else {
		report, err = runner.Run(ctx, section, number)
	}
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, report)
	}
	if _, err := fmt.Fprintf(stdout, "quality winner: %s\n", report.Winner); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, "fast mode:"); err != nil {
		return err
	}
	if err := printMetrics(stdout, report.Fast.Metrics.CurrentRun); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, "slow mode:"); err != nil {
		return err
	}
	if err := printMetrics(stdout, report.Slow.Metrics.CurrentRun); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "slow minus fast: %d tokens, $%.6f USD, %.2fx tokens, %.2fx list cost\n",
		report.Difference.TotalTokens, report.Difference.ListCostUSD,
		report.Difference.TokenRatio, report.Difference.CostRatio); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, "blind quality evaluation:"); err != nil {
		return err
	}
	return printMetrics(stdout, report.EvaluationMetrics.CurrentRun)
}

func newEngine(cfg config.Config, pool *route.Pool, stderr io.Writer) *solver.Engine {
	var progressMu sync.Mutex
	var client api.Completer = &api.ChatClient{
		URL: cfg.ChatCompletionsURL(), APIKey: cfg.APIKey, MaxRetries: cfg.MaxRetries,
		HTTPClient: &http.Client{Timeout: cfg.Timeout}, UserAgent: "taocp-solver/" + version,
	}
	if pool != nil {
		client = route.NewCompleter(pool)
	}
	return &solver.Engine{
		Repository: exercise.NewRepository(cfg.TAOCPRoot),
		Client:     client,
		Store:      result.Store{Root: cfg.OutputRoot},
		Progress: func(step solver.Progress) {
			progressMu.Lock()
			defer progressMu.Unlock()
			_, _ = fmt.Fprintln(stderr, step)
		},
	}
}

func parseTarget(args []string) (string, int, error) {
	if len(args) == 1 {
		return exercise.ParseReference(args[0])
	}
	if len(args) != 2 {
		return "", 0, errors.New("want SECTION NUMBER or SECTION.NUMBER")
	}
	number, err := strconv.Atoi(args[1])
	if err != nil || number < 1 {
		return "", 0, fmt.Errorf("invalid exercise number %q", args[1])
	}
	if strings.TrimSpace(args[0]) == "" {
		return "", 0, errors.New("section is empty")
	}
	return args[0], number, nil
}

func parseMode(value string) (solver.Mode, error) {
	mode := solver.Mode(strings.ToLower(strings.TrimSpace(value)))
	if mode != solver.ModeFast && mode != solver.ModeSlow {
		return "", fmt.Errorf("invalid mode %q; want fast or slow", value)
	}
	return mode, nil
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printMetrics(w io.Writer, metrics result.MetricSet) error {
	tokens := metrics.Tokens
	if _, err := fmt.Fprintf(w,
		"tokens: input %d (uncached %d, cached %d, cache write %d), output %d (reasoning %d), total %d across %d requests\n",
		tokens.InputTokens, tokens.UncachedInputTokens, tokens.CachedInputTokens,
		tokens.CacheWriteTokens, tokens.OutputTokens, tokens.ReasoningTokens,
		tokens.TotalTokens, tokens.Requests); err != nil {
		return err
	}
	if metrics.UnpricedRequests > 0 {
		_, err := fmt.Fprintf(w, "official list cost estimate: unavailable for %d of %d requests\n", metrics.UnpricedRequests, tokens.Requests)
		return err
	}
	_, err := fmt.Fprintf(w, "official list cost estimate: $%.6f USD\n", metrics.ListCost.TotalUSD)
	return err
}

func runBridge(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := pflag.NewFlagSet("bridge", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 8790, "listen port")
	host := fs.String("host", "127.0.0.1", "listen address; a non-loopback address requires --api-key")
	apiKey := fs.String("api-key", "", "key clients must present as a bearer token")
	authPath := fs.String("auth", "", "Codex credential file; defaults to ~/.codex/auth.json")
	model := fs.String("model", codex.DefaultModel, "model used when a request does not name one")
	effort := fs.String("effort", "", "reasoning effort applied when a request does not set one")
	timeoutText := fs.String("timeout", "30m", "upstream request timeout")
	retries := fs.Int("retries", 2, "retries for transient upstream failures")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("bridge takes no positional arguments")
	}
	timeout, err := time.ParseDuration(*timeoutText)
	if err != nil {
		return fmt.Errorf("invalid timeout: %w", err)
	}

	auth := &codex.Auth{
		Path: *authPath,
		Logf: func(format string, args ...any) { _, _ = fmt.Fprintf(stderr, format+"\n", args...) },
	}
	// Read the credential before binding the port. Failing here with a clear
	// message beats a listener that answers every request with the same 502.
	token, err := auth.Token(ctx)
	if err != nil {
		return err
	}
	bridge := &codex.Bridge{
		Client: &codex.Client{
			Auth:       auth,
			HTTPClient: &http.Client{Timeout: timeout},
			MaxRetries: *retries,
			Effort:     *effort,
		},
		APIKey: *apiKey,
		Model:  *model,
		Logf:   func(format string, args ...any) { _, _ = fmt.Fprintf(stderr, format+"\n", args...) },
	}
	expiry := "unknown"
	if !token.ExpiresAt.IsZero() {
		expiry = token.ExpiresAt.Local().Format(time.RFC3339)
	}
	return bridge.Serve(ctx, *host, *port, func(address string) {
		_, _ = fmt.Fprintf(stdout, "taocp bridge on http://%s, plan %s, token expires %s, default model %s\n",
			address, orUnknown(token.PlanType), expiry, *model)
		if *apiKey == "" {
			_, _ = fmt.Fprintln(stdout, "no --api-key set, so any local process can spend this plan's quota")
		}
	})
}

// runPublish renders stored solutions into the brain content tree. It never
// touches git, so running it by hand is safe and committing stays the runner's
// job.
func runPublish(args []string, stdout, stderr io.Writer) error {
	defaults := config.FromEnv()
	fs := pflag.NewFlagSet("publish", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	brain := fs.String("brain", defaults.BrainRoot, "brain repository to publish into")
	source := fs.String("source", defaults.TAOCPRoot, "TAOCP source repository")
	output := fs.String("output", defaults.OutputRoot, "result store to publish from")
	section := fs.String("section", "", "publish one section")
	check := fs.Bool("check", false, "report what would change, write nothing, exit non-zero if anything would")
	verbose := fs.Bool("verbose", false, "list every path that changed")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var targets []publish.Target
	switch {
	case len(fs.Args()) > 0 && *section != "":
		return errors.New("give either --section or a positional target, not both")
	case len(fs.Args()) > 0:
		name, number, err := parseTarget(fs.Args())
		if err != nil {
			return err
		}
		targets = append(targets, publish.Target{Section: name, Number: number})
	case *section != "":
		targets = append(targets, publish.Target{Section: *section})
	}

	publisher := publish.New(*brain, *source, result.Store{Root: *output})
	report, err := publisher.Run(targets, *check)
	if err != nil {
		return err
	}

	verb := "written"
	if *check {
		verb = "to write"
	}
	if _, err := fmt.Fprintf(stdout, "publish: %d solutions %s, %d deleted by leak gate, %d unchanged\n",
		report.Written, verb, report.Deleted, report.Unchanged); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "indexes: %d sections, %d volumes, %d top\n",
		report.Sections, report.Volumes, report.Top); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "brain: %d solved, %d verified, %d total\n",
		report.Solved, report.Verified, report.Total); err != nil {
		return err
	}
	// Most pages carry no figure, so this line only appears when one travelled.
	if report.Images > 0 {
		if _, err := fmt.Fprintf(stdout, "figures: %d copied from the source repository\n", report.Images); err != nil {
			return err
		}
	}
	if *verbose {
		for _, path := range report.Changes {
			if _, err := fmt.Fprintln(stdout, " ", path); err != nil {
				return err
			}
		}
	}
	// A check that exits zero when the tree is out of date would be useless in a
	// pre-commit hook or a scheduled run.
	if *check && len(report.Changes) > 0 {
		return fmt.Errorf("%d files would change", len(report.Changes))
	}
	return nil
}

// runRun is the unattended campaign: work the coverage queue until it is empty,
// publishing each proof as it lands and committing the content repository on a
// timer. It is meant to be started in a screen session or under systemd and
// left alone for days, so everything it does is restartable.
func runRun(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	defaults := config.FromEnv()
	fs := pflag.NewFlagSet("run", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	common := bindCommon(fs, defaults)
	brain := fs.String("brain", defaults.BrainRoot, "brain repository to publish and commit into")
	volume := fs.String("volume", "", "restrict the queue to one volume, as 3 or vol4a")
	sections := fs.StringArray("section", nil, "restrict the queue to these sections; repeatable")
	limit := fs.Int("limit", 0, "stop after this many exercises")
	mode := fs.String("mode", string(solver.ModeSlow), "solve mode: fast or slow")
	retryEmpty := fs.Bool("retry-empty", false, "queue exercises whose stored result has no solution")
	noPublish := fs.Bool("no-publish", false, "solve and store, but do not render into the content tree")
	noCommit := fs.Bool("no-commit", false, "publish, but leave the content repository uncommitted")
	dryRun := fs.Bool("dry-run", false, "print the queue and exit without writing anything")
	asJSON := fs.Bool("json", false, "write the log and the summary as JSON")
	commitInterval := fs.String("commit-interval", runner.DefaultCommitInterval.String(), "how often to commit the content repository")
	maxSleep := fs.String("max-sleep", runner.DefaultMaxSleep.String(), "longest wait when every route is cold")
	drain := fs.String("drain", runner.DefaultDrain.String(), "how long to let in-flight solves finish after a stop signal")
	lock := fs.String("lock", runner.DefaultLock, "advisory lock the content repository's git commands run under")
	fs.IntVar(&common.config.Parallel, "parallel", defaults.Parallel, "exercises solved at once")
	fs.IntVar(&common.config.Candidates, "candidates", defaults.Candidates, "independent solution candidates per exercise")
	fs.IntVar(&common.config.MaxCorrections, "max-corrections", defaults.MaxCorrections, "correction passes per exercise")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return errors.New("run takes no positional arguments; use --section or --volume")
	}
	// An unattended run wants failover more than any other command, and the
	// host it runs on has a route file rather than a command line. Picking it
	// up without being told to is the difference between a deployment that
	// works from an environment file and one that needs a wrapper script.
	if !common.routing() {
		if path := route.DefaultPath(); path != "" {
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				common.routesFile = path
			}
		}
	}
	// A dry run only reads directories, so it must not demand a working
	// endpoint. Everything else does.
	if err := common.finish(!*dryRun); err != nil {
		return err
	}
	solveMode, err := parseMode(*mode)
	if err != nil {
		return err
	}
	options := runner.Options{
		Source: common.config.TAOCPRoot, Output: common.config.OutputRoot, Brain: *brain,
		Volume: *volume, Sections: *sections, Limit: *limit,
		Parallel: common.config.Parallel, Mode: solveMode,
		Candidates: common.config.Candidates, MaxCorrections: common.config.MaxCorrections,
		RetryEmpty: *retryEmpty, NoPublish: *noPublish, NoCommit: *noCommit, DryRun: *dryRun,
		Lock: *lock,
	}
	for _, field := range []struct {
		text  string
		into  *time.Duration
		named string
	}{
		{*commitInterval, &options.CommitInterval, "commit-interval"},
		{*maxSleep, &options.MaxSleep, "max-sleep"},
		{*drain, &options.Drain, "drain"},
	} {
		value, err := config.ParseDuration(field.text)
		if err != nil {
			return fmt.Errorf("--%s: %w", field.named, err)
		}
		*field.into = value
	}

	store := result.Store{Root: options.Output}
	campaign := &runner.Runner{
		Options:   options,
		Store:     store,
		Publisher: publish.New(options.Brain, options.Source, store),
		Log:       runner.Logger(stderr, *asJSON),
	}
	if !*dryRun {
		pool, err := common.pool(stderr)
		if err != nil {
			return err
		}
		// Everything a run says goes through one logger, so a week of screen
		// output is one stream of timestamped lines rather than the runner's
		// events with the engine's and the pool's chatter shuffled into them.
		if pool != nil {
			pool.Logf = campaign.Routing
		}
		engine := newEngine(common.config, pool, stderr)
		engine.Progress = campaign.Step
		campaign.Pool = pool
		campaign.Engine = engine
	}
	if !*noCommit && !*dryRun {
		// The lock comes from options: the run holds it for publishing too, and
		// setting it in two places is how the two stop matching.
		committer := runner.NewCommitter(options.Brain)
		committer.Log = campaign.Log
		campaign.Committer = committer
	}

	summary, err := campaign.Run(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, summary)
	}
	_, err = fmt.Fprintln(stdout, summary)
	return err
}

// runCoverage answers which exercises are still missing, and where. The three
// inputs are directories, so there is nothing to keep in sync and no cursor to
// corrupt: every run recomputes the answer from what is on disk.
func runCoverage(args []string, stdout, stderr io.Writer) error {
	defaults := config.FromEnv()
	fs := pflag.NewFlagSet("coverage", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	source := fs.String("source", defaults.TAOCPRoot, "TAOCP source repository")
	output := fs.String("output", defaults.OutputRoot, "result store to count solves in")
	brain := fs.String("brain", defaults.BrainRoot, "brain repository to count published pages in")
	volume := fs.String("volume", "", "one volume, as 3 or vol4a")
	section := fs.String("section", "", "one section")
	asJSON := fs.Bool("json", false, "write the whole report as JSON")
	missing := fs.Bool("missing", false, "write the work queue, one section and number per line")
	orphans := fs.Bool("orphans", false, "list published exercises the source repository does not enumerate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		if *section != "" {
			return errors.New("give either --section or a positional section, not both")
		}
		*section = fs.Args()[0]
	}

	report, err := coverage.New(*source, *output, *brain).Run(coverage.Filter{Volume: *volume, Section: *section})
	if err != nil {
		return err
	}
	switch {
	case *asJSON:
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	case *missing:
		return report.WriteMissing(stdout)
	case *orphans:
		return report.WriteOrphans(stdout)
	}
	return report.Write(stdout, true)
}

// runDoctor probes every route and reports what it found. It exits non-zero
// when nothing is live, which is what makes it usable as a cron guard and as a
// systemd ExecStartPre.
func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := pflag.NewFlagSet("doctor", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	routesFile := fs.String("routes", "", "route file to probe; defaults to the personal file, then the built-ins")
	routeNames := fs.StringSlice("route", nil, "probe only these routes")
	jsonOutput := fs.Bool("json", false, "write the probe results as JSON")
	writeRoutes := fs.String("write-routes", "", "write the effective route file and exit without probing")
	suggestRoutes := fs.String("suggest-routes", "", "probe, then write a route file refreshed from the live catalogues")
	timeoutText := fs.String("timeout", "60s", "timeout per probe")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("doctor takes no positional arguments")
	}
	timeout, err := config.ParseDuration(*timeoutText)
	if err != nil {
		return err
	}
	registry, source, err := route.LoadOrDefault(*routesFile)
	if err != nil {
		return err
	}
	if len(*routeNames) > 0 {
		registry, err = registry.Select(*routeNames)
		if err != nil {
			return err
		}
	}
	if *writeRoutes != "" {
		if err := registry.Write(*writeRoutes); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "wrote %d routes from %s to %s\n", len(registry.Routes), source, *writeRoutes)
		return err
	}

	// Disabled routes are probed too. A route is disabled because of something
	// that was true once, and doctor is how you find out it has changed.
	pool := route.NewPool(route.Registry{Routes: allRoutes(registry)})
	pool.Prober = route.Prober{Timeout: timeout}
	results := pool.ProbeAll(ctx)

	if *suggestRoutes != "" {
		catalogues := map[string][]string{}
		for _, health := range results {
			if len(health.Catalogue) > 0 {
				catalogues[health.Route] = health.Catalogue
			}
		}
		suggested := route.Suggest(registry, catalogues)
		if err := suggested.Write(*suggestRoutes); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "wrote %d routes to %s\n", len(suggested.Routes), *suggestRoutes)
	}

	if *jsonOutput {
		if err := writeJSON(stdout, map[string]any{
			"source":          source,
			"routes":          results,
			"catalogue_drift": route.Drift(results),
		}); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprint(stdout, route.Table(results)); err != nil {
			return err
		}
		for _, line := range route.Drift(results) {
			_, _ = fmt.Fprintln(stdout, line)
		}
	}
	for _, health := range results {
		if health.State == route.StateLive {
			return nil
		}
	}
	return fmt.Errorf("no route is live; %d probed from %s", len(results), source)
}

// allRoutes includes disabled routes, which Enabled deliberately drops.
func allRoutes(registry route.Registry) []route.Route {
	out := make([]route.Route, 0, len(registry.Routes))
	for _, value := range registry.Routes {
		value.Disabled = false
		out = append(out, value)
	}
	return out
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func usage(w io.Writer) error {
	_, err := fmt.Fprint(w, `taocp solves and reviews TAOCP exercises through an OpenAI-compatible bridge or proxy.

Usage:
  taocp solve SECTION NUMBER [flags]
  taocp solve SECTION.NUMBER [flags]
  taocp batch SECTION... [flags]
  taocp prompt SECTION NUMBER [flags]
  taocp review SECTION NUMBER --file solution.md [flags]
  taocp benchmark SECTION NUMBER [flags]
  taocp matrix [flags]
  taocp publish [SECTION NUMBER] [flags]
  taocp coverage [SECTION] [flags]
  taocp run [flags]
  taocp bridge [flags]
  taocp doctor [flags]
  taocp version

Run a command with -h to see its flags.
`)
	return err
}
