package solver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/prompt"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/textguard"
)

type Options struct {
	Model          string
	Verify         bool
	Force          bool
	MaxCorrections int
}

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
	started := time.Now()
	e.log("loading TAOCP %s.%d", section, number)
	ex, sourceContext, err := e.Repository.Load(section, number)
	if err != nil {
		return result.Result{}, err
	}

	var solution string
	var attempts []result.Attempt
	if !options.Force {
		cached, err := e.Store.Load(section, number)
		if err == nil && cached.Solution != "" {
			e.log("using cached solution for TAOCP %s.%d", section, number)
			solution = cached.Solution
			attempts = append(attempts, cached.Attempts...)
		} else if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			// A missing cache is normal. A corrupt cache must be surfaced because
			// silently replacing it would hide data loss.
			if !errors.Is(err, errors.ErrUnsupported) && !os.IsNotExist(err) {
				return result.Result{}, err
			}
		}
	}

	if solution == "" {
		e.log("solving TAOCP %s.%d with %s", section, number, options.Model)
		instructions, input := e.Prompts.Solve(ex, sourceContext)
		response, err := e.complete(ctx, "solve", 0, options.Model, instructions, input, ex)
		if err != nil {
			return result.Result{}, err
		}
		solution, err = textguard.CleanSolution(response.Text)
		if err != nil {
			return result.Result{}, fmt.Errorf("reject solve response: %w", err)
		}
		attempts = append(attempts, attempt("solve", 0, response))
	}

	verdict := "SKIP"
	review := ""
	var reviews []result.Review
	if options.Verify {
		for iteration := 0; iteration <= options.MaxCorrections; iteration++ {
			e.log("checking correctness for TAOCP %s.%d, pass %d", section, number, iteration+1)
			instructions, input := e.Prompts.ReviewCorrectness(ex, sourceContext, solution)
			correctnessResponse, err := e.complete(ctx, "review-correctness", iteration, options.Model, instructions, input, ex)
			if err != nil {
				return result.Result{}, err
			}
			correctnessText := textguard.CleanGeneratedText(correctnessResponse.Text)
			correctnessVerdict := textguard.Verdict(correctnessText)
			correctnessScore := textguard.Score(correctnessText)
			attempts = append(attempts, attempt("review-correctness", iteration, correctnessResponse))
			if correctnessVerdict == "UNKNOWN" || correctnessScore < 0 {
				return result.Result{}, fmt.Errorf("correctness review has no valid score or final verdict")
			}
			if correctnessScore < 6 {
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
			attempts = append(attempts, attempt("review-process", iteration, processResponse))
			if processVerdict == "UNKNOWN" {
				return result.Result{}, fmt.Errorf("process review has no valid final verdict")
			}
			reviews = []result.Review{
				{Kind: "correctness", Text: correctnessText, Verdict: correctnessVerdict, Score: correctnessScore},
				{Kind: "process", Text: processText, Verdict: processVerdict},
			}
			review = "## Correctness Judge\n\n" + correctnessText + "\n\n## Process Judge\n\n" + processText
			if correctnessVerdict == "PASS" && processVerdict == "PASS" {
				verdict = "PASS"
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
			attempts = append(attempts, attempt("correct", iteration, correctionResponse))
		}
	}

	value := result.Result{
		ID:          fmt.Sprintf("%s-%d", ex.SectionID, ex.Number),
		Exercise:    ex,
		Solution:    solution,
		Review:      review,
		Reviews:     reviews,
		Verdict:     verdict,
		Verified:    verdict == "PASS",
		Model:       options.Model,
		SolveTime:   time.Since(started).Round(time.Millisecond),
		CompletedAt: time.Now().UTC(),
		Attempts:    attempts,
	}
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
	instructions, input := e.Prompts.ReviewCorrectness(ex, sourceContext, solution)
	correctnessResponse, err := e.complete(ctx, "review-correctness", 0, model, instructions, input, ex)
	if err != nil {
		return "", "", err
	}
	correctness := textguard.CleanGeneratedText(correctnessResponse.Text)
	correctnessVerdict := textguard.Verdict(correctness)
	correctnessScore := textguard.Score(correctness)
	if correctnessVerdict == "UNKNOWN" || correctnessScore < 0 {
		return "", "", errors.New("correctness review has no valid score or final verdict")
	}
	if correctnessScore < 6 {
		correctnessVerdict = "FAIL"
	}
	instructions, input = e.Prompts.ReviewProcess(ex, sourceContext, solution)
	processResponse, err := e.complete(ctx, "review-process", 0, model, instructions, input, ex)
	if err != nil {
		return "", "", err
	}
	process := textguard.CleanGeneratedText(processResponse.Text)
	processVerdict := textguard.Verdict(process)
	if processVerdict == "UNKNOWN" {
		return "", "", errors.New("process review has no valid final verdict")
	}
	verdict := "FAIL"
	if correctnessVerdict == "PASS" && processVerdict == "PASS" {
		verdict = "PASS"
	}
	combined := "## Correctness Judge\n\n" + correctness + "\n\n## Process Judge\n\n" + process
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

func attempt(phase string, iteration int, response api.Response) result.Attempt {
	return result.Attempt{
		Phase:        phase,
		Iteration:    iteration,
		ResponseID:   response.ID,
		Model:        response.Model,
		InputTokens:  response.InputTokens,
		OutputTokens: response.OutputTokens,
	}
}
