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
	if !strings.Contains(correctness, "SCORE: N/7") || !strings.Contains(process, "EARLIEST_ERROR: NONE") {
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
	if !strings.Contains(research, "Do not invent a complete proof") {
		t.Fatal("research guidance missing")
	}
}

func TestComparePromptIsAnonymous(t *testing.T) {
	t.Parallel()
	_, value := (Builder{}).Compare(
		exercise.Exercise{SectionID: "1.1", Number: 1, Body: "Problem."},
		exercise.Context{}, "First.", "Second.",
	)
	if !strings.Contains(value, "<solution_a>\nFirst.") || !strings.Contains(value, "<solution_b>\nSecond.") {
		t.Fatal("anonymous candidates missing")
	}
}
