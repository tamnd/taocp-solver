package prompt

import (
	"strings"
	"testing"

	"github.com/tamnd/taocp-solver/exercise"
)

func TestReviewPromptsAreIndependent(t *testing.T) {
	t.Parallel()
	builder := Builder{}
	ex := exercise.Exercise{SectionID: "1.1", Number: 1, Rating: "20", Body: "Prove it."}
	ctx := exercise.Context{Section: "Definitions."}
	correctnessInstructions, correctness := builder.ReviewCorrectness(ex, ctx, "Proof.")
	processInstructions, process := builder.ReviewProcess(ex, ctx, "Proof.")
	if correctnessInstructions == processInstructions || correctness == process {
		t.Fatal("review prompts must be distinct")
	}
	if !strings.Contains(correctness, "SCORE: N/7") || !strings.Contains(correctness, "TRUTH: TRUE") ||
		!strings.Contains(correctness, "HUMAN_READABLE: YES") || !strings.Contains(process, "EARLIEST_ERROR: NONE") ||
		!strings.Contains(process, "TRUTH: TRUE") || !strings.Contains(process, "counterexample") {
		t.Fatal("review prompts lack required evaluation signals")
	}
}

func TestSolvePromptScalesWithRating(t *testing.T) {
	t.Parallel()
	builder := Builder{}
	_, easy := builder.Solve(exercise.Exercise{SectionID: "1.1", Number: 1, Rating: "5", Body: "Compute."}, exercise.Context{})
	_, research := builder.Solve(exercise.Exercise{SectionID: "1.1", Number: 2, Rating: "M50", Body: "Discuss."}, exercise.Context{})
	if !strings.Contains(easy, "one or two short paragraphs") {
		t.Fatal("easy guidance missing")
	}
	if !strings.Contains(easy, "self-contained") || !strings.Contains(easy, "natural voice") || !strings.Contains(easy, "Make the verification visible") {
		t.Fatal("human-readable solution contract missing")
	}
	if !strings.Contains(research, "Do not invent a complete proof") {
		t.Fatal("research guidance missing")
	}
}

func TestPopulationPromptsSeparateReferenceAndSelection(t *testing.T) {
	t.Parallel()
	builder := Builder{}
	ex := exercise.Exercise{SectionID: "1.1", Number: 1, Rating: "20", Body: "Prove it."}
	_, reference := builder.Reference(ex, exercise.Context{Section: "Definitions."})
	_, selection := builder.Select(ex, "Reference work.", []string{"First.", "Second."})
	_, first := builder.SolveCandidate(ex, exercise.Context{}, 1)
	_, second := builder.SolveCandidate(ex, exercise.Context{}, 2)
	if !strings.Contains(reference, "## Falsification Checks") || strings.Contains(reference, "candidate_1") {
		t.Fatalf("reference prompt = %q", reference)
	}
	if !strings.Contains(selection, "<candidate_1>\nFirst.") || !strings.Contains(selection, "SELECTED: N") {
		t.Fatalf("selection prompt = %q", selection)
	}
	if first == second || !strings.Contains(first, "source-aligned") || !strings.Contains(second, "counterexample resistance") {
		t.Fatal("candidate prompts are not independently diversified")
	}
}

func TestQualityComparisonRequiresAuditEvidence(t *testing.T) {
	t.Parallel()
	_, value := (Builder{}).CompareQuality(
		exercise.Exercise{SectionID: "1.1", Number: 1, Body: "Problem."},
		exercise.Context{}, "First.", "Second.",
	)
	for _, heading := range []string{"## Independent Reference", "## Obligation Matrix", "## Candidate A Audit", "## Candidate B Audit", "## Final Fields"} {
		if !strings.Contains(value, heading) {
			t.Fatalf("missing %s", heading)
		}
	}
}
