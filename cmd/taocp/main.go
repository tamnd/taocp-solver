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
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/config"
	"github.com/tamnd/taocp-solver/evaluation"
	"github.com/tamnd/taocp-solver/exercise"
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
	case "compare":
		return runCompare(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run taocp-solver help", args[0])
	}
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
	verify := fs.Bool("verify", true, "run independent review and correction")
	force := fs.Bool("force", false, "ignore a cached solution")
	jsonOutput := fs.Bool("json", false, "write the full result as JSON")
	fs.IntVar(&common.config.MaxCorrections, "max-corrections", common.config.MaxCorrections, "maximum correction passes")
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
	value, err := engine.Solve(ctx, section, number, solver.Options{
		Model: common.config.Model, Verify: *verify, Force: *force,
		MaxCorrections: common.config.MaxCorrections,
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
	_, err = fmt.Fprintf(stderr, "verdict: %s, elapsed: %s\n", value.Verdict, value.SolveTime)
	return err
}

func runBatch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := pflag.NewFlagSet("batch", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	common := bindCommon(fs, config.FromEnv())
	verify := fs.Bool("verify", true, "run independent review and correction")
	force := fs.Bool("force", false, "solve exercises that already have cached results")
	jsonOutput := fs.Bool("json", false, "write results as a JSON array")
	fs.IntVar(&common.config.Parallel, "parallel", common.config.Parallel, "parallel exercises")
	fs.IntVar(&common.config.MaxCorrections, "max-corrections", common.config.MaxCorrections, "maximum correction passes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) == 0 {
		return errors.New("batch requires at least one section")
	}
	if err := common.finish(true); err != nil {
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
					Model: common.config.Model, Verify: *verify, Force: *force,
					MaxCorrections: common.config.MaxCorrections,
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

func runCompare(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := pflag.NewFlagSet("compare", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	common := bindCommon(fs, config.FromEnv())
	home, _ := os.UserHomeDir()
	candidateFile := fs.String("candidate", "", "new solution file, defaults to the cached Markdown result")
	brainRoot := fs.String("brain", filepath.Join(home, "github", "tamnd", "brain", "content", "en", "practice", "maths", "taocp"), "brain TAOCP directory")
	jsonOutput := fs.Bool("json", false, "write the complete comparison report as JSON")
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
	if *candidateFile == "" {
		*candidateFile = engine.Store.MarkdownPath(section, number)
	}
	candidate, err := os.ReadFile(*candidateFile)
	if err != nil {
		return fmt.Errorf("read candidate solution: %w", err)
	}
	ex, _, err := engine.Repository.Load(section, number)
	if err != nil {
		return err
	}
	baseline, baselinePath, err := evaluation.LoadBrainSolution(*brainRoot, ex.Volume, section, number)
	if err != nil {
		return err
	}
	comparator := evaluation.Comparator{
		Repository: engine.Repository, Client: engine.Client, Model: common.config.Model,
	}
	report, err := comparator.Compare(ctx, section, number, string(candidate), baseline)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, report)
	}
	if _, err := fmt.Fprintf(stdout, "winner: %s\nnew: %s\nbrain: %s\n", report.Winner, *candidateFile, baselinePath); err != nil {
		return err
	}
	for i, judgment := range report.Judgments {
		if _, err := fmt.Fprintf(stdout, "\n## Judgment %d (%s)\n\n%s\n", i+1, judgment.Order, judgment.Review); err != nil {
			return err
		}
	}
	return nil
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

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usage(w io.Writer) error {
	_, err := fmt.Fprint(w, `taocp solves, reviews, and compares TAOCP exercises through an OpenAI-compatible bridge or proxy.

Usage:
  taocp solve SECTION NUMBER [flags]
  taocp solve SECTION.NUMBER [flags]
  taocp batch SECTION... [flags]
  taocp prompt SECTION NUMBER [flags]
  taocp review SECTION NUMBER --file solution.md [flags]
  taocp compare SECTION NUMBER [flags]
  taocp version

Run a command with -h to see its flags.
`)
	return err
}
