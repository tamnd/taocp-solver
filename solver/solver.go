package solver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/pricing"
	"github.com/tamnd/taocp-solver/prompt"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/textguard"
)

type Options struct {
	Mode           Mode
	Model          string
	Verify         bool
	Force          bool
	MaxCorrections int
	Candidates     int
}

type Mode string

const (
	ModeFast Mode = "fast"
	ModeSlow Mode = "slow"
)

type Engine struct {
	Repository *exercise.Repository
	Client     api.Completer
	Prompts    prompt.Builder
	Store      result.Store
	Progress   func(string)
}

func (e *Engine) Solve(ctx context.Context, section string, number int, options Options) (result.Result, error) {
	if e.Repository == nil {
		return result.Result{}, errors.New("exercise repository is nil")
	}
	if e.Client == nil {
		return result.Result{}, errors.New("API client is nil")
	}
	switch options.Mode {
	case ModeFast:
		options.Verify = false
		options.Candidates = 1
		options.MaxCorrections = 0
	case ModeSlow:
		options.Verify = true
	case "":
	default:
		return result.Result{}, fmt.Errorf("unknown solve mode %q", options.Mode)
	}
	started := time.Now()
	e.log("loading TAOCP %s.%d", section, number)
	ex, sourceContext, err := e.Repository.Load(section, number)
	if err != nil {
		return result.Result{}, err
	}
	candidateCount := options.Candidates
	if candidateCount < 1 {
		candidateCount = 1
	}
	if candidateCount > 5 {
		return result.Result{}, errors.New("candidate count cannot exceed 5")
	}

	var solution string
	var attempts []result.Attempt
	var candidates []result.Candidate
	var reference, selection string
	selected := 0
	if options.Verify {
		e.log("building an independent reference for TAOCP %s.%d", section, number)
		instructions, input := e.Prompts.Reference(ex, sourceContext)
		response, err := e.complete(ctx, "reference", 0, options.Model, instructions, input, ex)
		if err != nil {
			return result.Result{}, err
		}
		reference = textguard.CleanGeneratedText(response.Text)
		if reference == "" {
			return result.Result{}, errors.New("independent reference is empty")
		}
		attempts = append(attempts, attempt("reference", 0, options.Model, response))
	}
	if !options.Force {
		cached, err := e.Store.Load(section, number)
		if err == nil && cached.Solution != "" {
			e.log("using cached solution for TAOCP %s.%d", section, number)
			solution = cached.Solution
			candidates = []result.Candidate{{Number: 1, Solution: solution}}
			selected = 1
			for _, previous := range cached.Attempts {
				previous.CurrentRun = false
				attempts = append(attempts, previous)
			}
		} else if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			// A missing cache is normal. A corrupt cache must be surfaced because
			// silently replacing it would hide data loss.
			if !errors.Is(err, errors.ErrUnsupported) && !os.IsNotExist(err) {
				return result.Result{}, err
			}
		}
	}

	if solution == "" {
		if !options.Verify {
			candidateCount = 1
		}
		candidateTexts := make([]string, 0, candidateCount)
		for i := 1; i <= candidateCount; i++ {
			e.log("generating candidate %d/%d for TAOCP %s.%d with %s", i, candidateCount, section, number, options.Model)
			instructions, input := e.Prompts.SolveCandidate(ex, sourceContext, i)
			response, err := e.complete(ctx, "solve-candidate", i, options.Model, instructions, input, ex)
			if err != nil {
				return result.Result{}, err
			}
			candidate, err := textguard.CleanSolution(response.Text)
			if err != nil {
				return result.Result{}, fmt.Errorf("reject candidate %d: %w", i, err)
			}
			candidateTexts = append(candidateTexts, candidate)
			candidates = append(candidates, result.Candidate{Number: i, Solution: candidate})
			attempts = append(attempts, attempt("solve-candidate", i, options.Model, response))
		}
		selected = 1
		if len(candidateTexts) > 1 {
			e.log("selecting from %d candidates for TAOCP %s.%d", len(candidateTexts), section, number)
			instructions, input := e.Prompts.Select(ex, reference, candidateTexts)
			response, err := e.complete(ctx, "select", 0, options.Model, instructions, input, ex)
			if err != nil {
				return result.Result{}, err
			}
			selection = textguard.CleanGeneratedText(response.Text)
			selected = textguard.SelectedCandidate(selection)
			attempts = append(attempts, attempt("select", 0, options.Model, response))
			if selected < 1 || selected > len(candidateTexts) {
				return result.Result{}, errors.New("candidate selection has no valid final choice")
			}
		}
		solution = candidateTexts[selected-1]
	}

	verdict := "SKIP"
	review := ""
	var reviews []result.Review
	evaluation := result.Evaluation{Verdict: "SKIPPED"}
	if options.Verify {
		for iteration := 0; iteration <= options.MaxCorrections; iteration++ {
			e.log("checking correctness for TAOCP %s.%d, pass %d", section, number, iteration+1)
			instructions, input := e.Prompts.ReviewTruth(ex, sourceContext, reference, solution)
			correctnessResponse, err := e.complete(ctx, "review-correctness", iteration, options.Model, instructions, input, ex)
			if err != nil {
				return result.Result{}, err
			}
			correctnessText := textguard.CleanGeneratedText(correctnessResponse.Text)
			correctnessVerdict := textguard.Verdict(correctnessText)
			correctnessScore := textguard.Score(correctnessText)
			correctnessAssessment := textguard.ParseAssessment(correctnessText)
			attempts = append(attempts, attempt("review-correctness", iteration, options.Model, correctnessResponse))
			if correctnessVerdict == "UNKNOWN" || correctnessScore < 0 || !correctnessAssessment.HasTruth || !correctnessAssessment.HasQuality {
				return result.Result{}, fmt.Errorf("truth review has incomplete decision fields")
			}
			qualityPassed := correctnessAssessment.Truth && correctnessAssessment.Complete &&
				correctnessAssessment.SelfContained && correctnessAssessment.HumanReadable &&
				correctnessAssessment.Verifiable && correctnessScore >= 6
			if !qualityPassed {
				correctnessVerdict = "FAIL"
			}

			e.log("auditing reasoning for TAOCP %s.%d, pass %d", section, number, iteration+1)
			instructions, input = e.Prompts.ReviewProcess(ex, sourceContext, solution)
			processResponse, err := e.complete(ctx, "review-process", iteration, options.Model, instructions, input, ex)
			if err != nil {
				return result.Result{}, err
			}
			processText := textguard.CleanGeneratedText(processResponse.Text)
			processVerdict := textguard.Verdict(processText)
			processAssessment := textguard.ParseAssessment(processText)
			attempts = append(attempts, attempt("review-process", iteration, options.Model, processResponse))
			if processVerdict == "UNKNOWN" || !processAssessment.HasTruth {
				return result.Result{}, fmt.Errorf("audit review has no valid truth decision")
			}
			if !processAssessment.Truth {
				processVerdict = "FAIL"
			}
			reviews = []result.Review{
				{Kind: "truth", Text: correctnessText, Verdict: correctnessVerdict, Score: correctnessScore},
				{Kind: "audit", Text: processText, Verdict: processVerdict},
			}
			review = "## Truth Judge\n\n" + correctnessText + "\n\n## Audit Judge\n\n" + processText
			evaluation = result.Evaluation{
				Verdict: "FALSE", Complete: correctnessAssessment.Complete,
				SelfContained:    correctnessAssessment.SelfContained,
				HumanReadable:    correctnessAssessment.HumanReadable,
				Verifiable:       correctnessAssessment.Verifiable,
				TruthJudgePassed: correctnessVerdict == "PASS",
				AuditJudgePassed: processVerdict == "PASS",
			}
			if correctnessVerdict == "PASS" && processVerdict == "PASS" {
				verdict = "PASS"
				evaluation.Verdict = "TRUE"
				evaluation.True = true
				break
			}
			verdict = "FAIL"
			if iteration == options.MaxCorrections {
				break
			}

			e.log("correcting TAOCP %s.%d, pass %d", section, number, iteration+1)
			instructions, input = e.Prompts.Correct(ex, sourceContext, solution, review)
			correctionResponse, err := e.complete(ctx, "correct", iteration, options.Model, instructions, input, ex)
			if err != nil {
				return result.Result{}, err
			}
			solution, err = textguard.CleanSolution(correctionResponse.Text)
			if err != nil {
				return result.Result{}, fmt.Errorf("reject correction response: %w", err)
			}
			attempts = append(attempts, attempt("correct", iteration, options.Model, correctionResponse))
		}
	}

	value := result.Result{
		ID:          fmt.Sprintf("%s-%d", ex.SectionID, ex.Number),
		Exercise:    ex,
		Solution:    solution,
		Candidates:  candidates,
		Reference:   reference,
		Selection:   selection,
		Selected:    selected,
		Review:      review,
		Reviews:     reviews,
		Verdict:     verdict,
		Verified:    verdict == "PASS",
		Evaluation:  evaluation,
		Model:       servedModels(attempts, options.Model),
		SolveTime:   time.Since(started).Round(time.Millisecond),
		CompletedAt: time.Now().UTC(),
		Attempts:    attempts,
	}
	value.Metrics = result.BuildMetrics(value.Attempts)
	if err := e.Store.Save(value); err != nil {
		return result.Result{}, err
	}
	e.log("saved TAOCP %s.%d with verdict %s", section, number, verdict)
	return value, nil
}

func (e *Engine) Review(ctx context.Context, section string, number int, solution, model string) (string, string, error) {
	ex, sourceContext, err := e.Repository.Load(section, number)
	if err != nil {
		return "", "", err
	}
	instructions, input := e.Prompts.Reference(ex, sourceContext)
	referenceResponse, err := e.complete(ctx, "reference", 0, model, instructions, input, ex)
	if err != nil {
		return "", "", err
	}
	reference := textguard.CleanGeneratedText(referenceResponse.Text)
	instructions, input = e.Prompts.ReviewTruth(ex, sourceContext, reference, solution)
	correctnessResponse, err := e.complete(ctx, "review-correctness", 0, model, instructions, input, ex)
	if err != nil {
		return "", "", err
	}
	correctness := textguard.CleanGeneratedText(correctnessResponse.Text)
	correctnessVerdict := textguard.Verdict(correctness)
	correctnessScore := textguard.Score(correctness)
	assessment := textguard.ParseAssessment(correctness)
	if correctnessVerdict == "UNKNOWN" || correctnessScore < 0 || !assessment.HasTruth || !assessment.HasQuality {
		return "", "", errors.New("truth review has incomplete decision fields")
	}
	if correctnessScore < 6 || !assessment.Truth || !assessment.Complete || !assessment.SelfContained || !assessment.HumanReadable || !assessment.Verifiable {
		correctnessVerdict = "FAIL"
	}
	instructions, input = e.Prompts.ReviewProcess(ex, sourceContext, solution)
	processResponse, err := e.complete(ctx, "review-process", 0, model, instructions, input, ex)
	if err != nil {
		return "", "", err
	}
	process := textguard.CleanGeneratedText(processResponse.Text)
	processVerdict := textguard.Verdict(process)
	processAssessment := textguard.ParseAssessment(process)
	if processVerdict == "UNKNOWN" || !processAssessment.HasTruth {
		return "", "", errors.New("audit review has no valid truth decision")
	}
	if !processAssessment.Truth {
		processVerdict = "FAIL"
	}
	verdict := "FAIL"
	if correctnessVerdict == "PASS" && processVerdict == "PASS" {
		verdict = "PASS"
	}
	combined := "## Truth Judge\n\n" + correctness + "\n\n## Audit Judge\n\n" + process
	return combined, verdict, nil
}

func (e *Engine) complete(ctx context.Context, phase string, iteration int, model, instructions, input string, ex exercise.Exercise) (api.Response, error) {
	response, err := e.Client.Complete(ctx, api.Request{
		Model:        model,
		Instructions: instructions,
		Input:        input,
		Effort:       "high",
		Metadata: map[string]string{
			"task":      "taocp-solution",
			"phase":     phase,
			"exercise":  fmt.Sprintf("%s.%d", ex.SectionID, ex.Number),
			"iteration": fmt.Sprintf("%d", iteration),
		},
	})
	if err != nil {
		return api.Response{}, fmt.Errorf("%s TAOCP %s.%d: %w", phase, ex.SectionID, ex.Number, err)
	}
	return response, nil
}

func (e *Engine) log(format string, args ...any) {
	if e.Progress != nil {
		e.Progress(fmt.Sprintf(format, args...))
	}
}

// servedModels names the models that actually answered. A run that failed over
// onto a second route did not use the model it was asked for, and recording the
// request rather than the answer would misattribute the work.
func servedModels(attempts []result.Attempt, requested string) string {
	var out []string
	for _, value := range attempts {
		if !value.CurrentRun || value.Model == "" || slices.Contains(out, value.Model) {
			continue
		}
		out = append(out, value.Model)
	}
	if len(out) == 0 {
		return requested
	}
	return strings.Join(out, ", ")
}

func attempt(phase string, iteration int, requestedModel string, response api.Response) result.Attempt {
	usage := response.Usage
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		usage.InputTokens = response.InputTokens
		usage.OutputTokens = response.OutputTokens
	}
	usage = usage.Normalized()
	model := response.Model
	if model == "" {
		model = requestedModel
	}
	// A route may serve a model with no rate card of its own, in which case
	// the route names the card that applies. Pricing the answer at whatever
	// slug the provider echoed back would be a guess.
	priced := response.PricingModel
	if priced == "" {
		priced = model
	}
	return result.Attempt{
		Phase:        phase,
		Iteration:    iteration,
		ResponseID:   response.ID,
		Model:        model,
		Route:        response.Route,
		CurrentRun:   true,
		InputTokens:  response.InputTokens,
		OutputTokens: response.OutputTokens,
		Usage:        usage,
		ListCost:     pricing.Calculate(priced, usage),
	}
}
