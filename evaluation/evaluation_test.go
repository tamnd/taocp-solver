package evaluation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/exercise"
)

type scriptedCompleter struct {
	responses []api.Response
	requests  []api.Request
}

func (s *scriptedCompleter) Complete(_ context.Context, request api.Request) (api.Response, error) {
	s.requests = append(s.requests, request)
	if len(s.responses) == 0 {
		return api.Response{}, errors.New("unexpected request")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func TestEvaluateRequiresBothIndependentJudges(t *testing.T) {
	t.Parallel()
	client := &scriptedCompleter{responses: []api.Response{
		{ID: "truth", Model: "gpt-5.6-sol", Text: passingTruthReview(), Usage: api.Usage{InputTokens: 100, CachedInputTokens: 20, OutputTokens: 30, ReasoningTokens: 10}},
		{ID: "audit", Model: "gpt-5.6-sol", Text: "## Truth Decision\nTRUTH: FALSE\n\n## Verdict\nVERDICT: FAIL", Usage: api.Usage{InputTokens: 80, OutputTokens: 20}},
	}}
	auditor := Auditor{Client: client, Model: "gpt-5.6-sol"}
	report, err := auditor.Evaluate(context.Background(), sampleExercise(), exercise.Context{Section: "Context"}, "Reference", "Proposal")
	if err != nil {
		t.Fatal(err)
	}
	if report.Truth || report.Publishable || !report.TruthJudgePassed || report.AuditJudgePassed || !report.Disagreement {
		t.Fatalf("report = %+v", report)
	}
	if report.Metrics.CurrentRun.Tokens.Requests != 2 || report.Metrics.CurrentRun.Tokens.TotalTokens != 230 || report.Metrics.CurrentRun.Tokens.ReasoningTokens != 10 {
		t.Fatalf("metrics = %+v", report.Metrics.CurrentRun)
	}
	if len(client.requests) != 2 || client.requests[0].Metadata["task"] != "taocp-evaluation-truth" || client.requests[1].Metadata["task"] != "taocp-evaluation-falsification" {
		t.Fatalf("requests = %+v", client.requests)
	}
	if strings.Contains(client.requests[1].Input, "Reference") {
		t.Fatal("reference-blind audit received the private reference")
	}
}

func TestEvaluateRejectsIncompleteDecisionFields(t *testing.T) {
	t.Parallel()
	client := &scriptedCompleter{responses: []api.Response{{Text: "SCORE: 7/7\nTRUTH: TRUE\nVERDICT: PASS"}}}
	_, err := (Auditor{Client: client, Model: "gpt-5.6-sol"}).Evaluate(
		context.Background(), sampleExercise(), exercise.Context{}, "Reference", "Proposal")
	if err == nil || !strings.Contains(err.Error(), "incomplete decision fields") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildReferenceCapturesUsage(t *testing.T) {
	t.Parallel()
	client := &scriptedCompleter{responses: []api.Response{{ID: "ref", Model: "gpt-5.6-sol", Text: "A checked reference.", Usage: api.Usage{InputTokens: 20, OutputTokens: 10}}}}
	reference, err := (Auditor{Client: client, Model: "gpt-5.6-sol"}).BuildReference(context.Background(), sampleExercise(), exercise.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if reference.Text != "A checked reference." || reference.Attempt.Phase != "evaluation-reference" || reference.Attempt.Usage.TotalTokens != 30 || !reference.Attempt.ListCost.Available {
		t.Fatalf("reference = %+v", reference)
	}
}

func sampleExercise() exercise.Exercise {
	return exercise.Exercise{SectionID: "1.2.1", Number: 1, Rating: "5", Body: "Prove the identity."}
}

func passingTruthReview() string {
	return "SCORE: 7/7\nTRUTH: TRUE\nCOMPLETE: YES\nSELF_CONTAINED: YES\nHUMAN_READABLE: YES\nVERIFIABLE: YES\nVERDICT: PASS"
}
