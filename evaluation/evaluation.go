// Package evaluation audits completed solutions with a fixed, model-blind
// verifier. It separates reference construction, criteria grading, and
// adversarial falsification so one permissive judgment cannot certify a result.
package evaluation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/exercise"
	"github.com/tamnd/taocp-solver/pricing"
	"github.com/tamnd/taocp-solver/prompt"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/textguard"
)

type Auditor struct {
	Client  api.Completer
	Model   string
	Prompts prompt.Builder
}

type Reference struct {
	Text    string         `json:"text_md"`
	Attempt result.Attempt `json:"attempt"`
}

type Report struct {
	Truth            bool             `json:"true"`
	Publishable      bool             `json:"publishable"`
	Score            int              `json:"score"`
	Complete         bool             `json:"complete"`
	SelfContained    bool             `json:"self_contained"`
	HumanReadable    bool             `json:"human_readable"`
	Verifiable       bool             `json:"verifiable"`
	TruthJudgePassed bool             `json:"truth_judge_passed"`
	AuditJudgePassed bool             `json:"audit_judge_passed"`
	Disagreement     bool             `json:"judge_disagreement"`
	TruthReview      string           `json:"truth_review_md"`
	AuditReview      string           `json:"audit_review_md"`
	Attempts         []result.Attempt `json:"attempts"`
	Metrics          result.Metrics   `json:"metrics"`
	Elapsed          time.Duration    `json:"elapsed"`
}

func (a Auditor) BuildReference(ctx context.Context, ex exercise.Exercise, source exercise.Context) (Reference, error) {
	if a.Client == nil || a.Model == "" {
		return Reference{}, errors.New("evaluation auditor is not configured")
	}
	instructions, input := a.Prompts.Reference(ex, source)
	response, err := a.Client.Complete(ctx, api.Request{
		Model: a.Model, Instructions: instructions, Input: input, Effort: "high",
		Metadata: map[string]string{"task": "taocp-evaluation-reference", "exercise": fmt.Sprintf("%s.%d", ex.SectionID, ex.Number)},
	})
	if err != nil {
		return Reference{}, fmt.Errorf("build evaluation reference: %w", err)
	}
	text := textguard.CleanGeneratedText(response.Text)
	if text == "" {
		return Reference{}, errors.New("evaluation reference is empty")
	}
	return Reference{Text: text, Attempt: makeAttempt("evaluation-reference", a.Model, response)}, nil
}

func (a Auditor) Evaluate(ctx context.Context, ex exercise.Exercise, source exercise.Context, reference, solution string) (Report, error) {
	if a.Client == nil || a.Model == "" {
		return Report{}, errors.New("evaluation auditor is not configured")
	}
	if solution == "" {
		return Report{}, errors.New("solution is empty")
	}
	started := time.Now()
	instructions, input := a.Prompts.ReviewTruth(ex, source, reference, solution)
	truthResponse, err := a.Client.Complete(ctx, api.Request{
		Model: a.Model, Instructions: instructions, Input: input, Effort: "high",
		Metadata: map[string]string{"task": "taocp-evaluation-truth", "exercise": fmt.Sprintf("%s.%d", ex.SectionID, ex.Number)},
	})
	if err != nil {
		return Report{}, fmt.Errorf("criteria truth judge: %w", err)
	}
	truthText := textguard.CleanGeneratedText(truthResponse.Text)
	truthVerdict := textguard.Verdict(truthText)
	score := textguard.Score(truthText)
	assessment := textguard.ParseAssessment(truthText)
	if truthVerdict == "UNKNOWN" || score < 0 || !assessment.HasTruth || !assessment.HasQuality {
		return Report{}, errors.New("criteria truth judge returned incomplete decision fields")
	}
	truthPassed := truthVerdict == "PASS" && assessment.Truth && assessment.Complete &&
		assessment.SelfContained && assessment.HumanReadable && assessment.Verifiable && score >= 6

	instructions, input = a.Prompts.ReviewProcess(ex, source, solution)
	auditResponse, err := a.Client.Complete(ctx, api.Request{
		Model: a.Model, Instructions: instructions, Input: input, Effort: "high",
		Metadata: map[string]string{"task": "taocp-evaluation-falsification", "exercise": fmt.Sprintf("%s.%d", ex.SectionID, ex.Number)},
	})
	if err != nil {
		return Report{}, fmt.Errorf("adversarial audit judge: %w", err)
	}
	auditText := textguard.CleanGeneratedText(auditResponse.Text)
	auditVerdict := textguard.Verdict(auditText)
	auditAssessment := textguard.ParseAssessment(auditText)
	if auditVerdict == "UNKNOWN" || !auditAssessment.HasTruth {
		return Report{}, errors.New("adversarial audit judge returned incomplete decision fields")
	}
	auditPassed := auditVerdict == "PASS" && auditAssessment.Truth
	attempts := []result.Attempt{
		makeAttempt("evaluation-truth", a.Model, truthResponse),
		makeAttempt("evaluation-falsification", a.Model, auditResponse),
	}
	return Report{
		Truth:       assessment.Truth && auditAssessment.Truth,
		Publishable: truthPassed && auditPassed,
		Score:       score, Complete: assessment.Complete, SelfContained: assessment.SelfContained,
		HumanReadable: assessment.HumanReadable, Verifiable: assessment.Verifiable,
		TruthJudgePassed: truthPassed, AuditJudgePassed: auditPassed,
		Disagreement: assessment.Truth != auditAssessment.Truth,
		TruthReview:  truthText, AuditReview: auditText, Attempts: attempts,
		Metrics: result.BuildMetrics(attempts), Elapsed: time.Since(started).Round(time.Millisecond),
	}, nil
}

func makeAttempt(phase, requestedModel string, response api.Response) result.Attempt {
	usage := response.Usage.Normalized()
	model := response.Model
	if model == "" {
		model = requestedModel
	}
	return result.Attempt{
		Phase: phase, ResponseID: response.ID, Model: model, CurrentRun: true,
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		Usage: usage, ListCost: pricing.Calculate(model, usage),
	}
}
