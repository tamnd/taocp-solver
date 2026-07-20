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

	"github.com/spf13/pflag"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/benchmark"
	"github.com/tamnd/taocp-solver/config"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/matrix"
	"github.com/tamnd/taocp-solver/prompt"
	"github.com/tamnd/taocp-solver/result"
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
		Timeout: timeout, MaxRetries: *retries, Parallel: *parallel, Resume: *resume,
		Candidates: *candidates, MaxCorrections: *corrections,
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
		if _, err := fmt.Fprintf(stdout, "%s %s: %d/%d publishable, %d/%d true, %d tokens, $%.6f generation\n",
			item.Model, item.Mode, item.Publishable, item.Completed, item.TrueSolutions, item.Completed,
			item.GenerationMetrics.Tokens.TotalTokens, item.GenerationCostUSD); err != nil {
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
}

func bindCommon(fs *pflag.FlagSet, defaults config.Config) *commonFlags {
	c := &commonFlags{config: defaults, timeoutText: defaults.Timeout.String()}
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
	return c.config.Validate(requireAPI)
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
	engine := newEngine(common.config, stderr)
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
	engine := newEngine(common.config, stderr)
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
	engine := newEngine(common.config, stderr)
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
	engine := newEngine(common.config, stderr)
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

func newEngine(cfg config.Config, stderr io.Writer) *solver.Engine {
	var progressMu sync.Mutex
	return &solver.Engine{
		Repository: exercise.NewRepository(cfg.TAOCPRoot),
		Client: &api.ChatClient{
			URL: cfg.ChatCompletionsURL(), APIKey: cfg.APIKey, MaxRetries: cfg.MaxRetries,
			HTTPClient: &http.Client{Timeout: cfg.Timeout}, UserAgent: "taocp-solver/" + version,
		},
		Store: result.Store{Root: cfg.OutputRoot},
		Progress: func(message string) {
			progressMu.Lock()
			defer progressMu.Unlock()
			_, _ = fmt.Fprintln(stderr, message)
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
  taocp version

Run a command with -h to see its flags.
`)
	return err
}
