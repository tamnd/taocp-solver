package prompt

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tamnd/taocp-solver/exercise"
)

const solverInstructions = `You are an expert mathematics and computer science author preparing a rigorous study companion for The Art of Computer Programming. Work privately through the problem before drafting. Return only the finished solution in Markdown. Follow the source notation exactly. Never claim a result that you have not derived or justified.`

const correctnessReviewerInstructions = `You are a senior mathematics and computer science examiner. Build a problem-specific marking scheme, solve the exercise independently, and grade the proposed solution against that work. Return a fine-grained score and a machine-readable verdict. Do not rewrite the proposal.`

const processReviewerInstructions = `You are an adversarial mathematical proof auditor. Inspect the proposed solution in logical order, identify the earliest material error if one exists, verify the final result by an independent check, and test every requirement in the exercise. Return a machine-readable verdict. Do not rewrite the proposal.`

var ratingNumber = regexp.MustCompile(`\d+`)

type Builder struct{}

func (Builder) Solve(ex exercise.Exercise, context exercise.Context) (string, string) {
	rating := numericRating(ex.Rating)
	preceding := ""
	if context.Preceding != "" {
		preceding = fmt.Sprintf(`
<preceding_exercises>
These are supplied only to clarify local notation and dependencies. Do not solve them again.

%s
</preceding_exercises>
`, context.Preceding)
	}
	input := fmt.Sprintf(`<quality_contract>
Write a precise, economical, fully rigorous textbook solution.

1. Derive and independently check every numeric answer, count, algebraic identity, and boundary case.
2. Use LaTeX for mathematics and preserve the book's variables, step labels, equations, and notation.
3. For a "find all" problem, prove sufficiency and necessity.
4. For an optimization problem, give a construction and prove the matching bound.
5. For a proof, justify each nontrivial implication and end with "This completes the proof." followed by ∎ on its own line.
6. For a computation, state and box the final answer after deriving it.
7. For an algorithm, state its invariant, termination argument, and complexity when relevant.
8. Do not use em dashes, horizontal rules, filler, throat-clearing, repeated conclusions, or unsupported phrases such as "obviously" and "it is clear".
9. Do not mention the model, provider, prompt, review process, or generation process.
10. Do not use bullet lists inside the mathematical argument unless the exercise itself requires a step sequence.
</quality_contract>

<exercise_metadata>
Volume: %d
Section: %s
Section title: %s
Exercise: %d
Difficulty rating: %s
Category: %s
</exercise_metadata>

<section_context>
%s
</section_context>
%s
<exercise>
%s
</exercise>

<format_guidance>
%s
</format_guidance>

Produce only the finished solution.`, ex.Volume, ex.SectionID, ex.SectionTitle, ex.Number,
		ex.Rating, ex.Category, context.Section, preceding, ex.Body, formatGuidance(rating))
	return solverInstructions, input
}

func (Builder) ReviewCorrectness(ex exercise.Exercise, context exercise.Context, solution string) (string, string) {
	input := fmt.Sprintf(`<grading_contract>
First derive a concise reference approach and a problem-specific marking scheme. Then grade the proposal on a 0 to 7 scale:

7: fully correct, complete, rigorous, and responsive to every instruction.
6: correct and complete with only a minor presentation defect.
5: essentially correct with a small localized justification gap.
3-4: meaningful progress, but at least one major gap or error remains.
1-2: limited valid progress or a fundamentally wrong approach.
0: no meaningful correct progress.

A passing solution must score at least 6 and contain no material error. Accept valid approaches that differ from your reference. Check both the final conclusion and the reasoning that supports it.
</grading_contract>

<section_context>
%s
</section_context>

<exercise>
Section %s, Exercise %d, difficulty %s

%s
</exercise>

<proposed_solution>
%s
</proposed_solution>

Return these headings in order:

## Reference Approach
Give the result and the key steps of an independent solution.

## Marking Scheme
List the problem-specific obligations required for a complete solution.

## Assessment
Grade each obligation and identify any unsupported claim.

## Score
Write `+"`SCORE: N/7`"+` on its own line, with an integer N.

## Verdict
On the final line, write exactly `+"`VERDICT: PASS`"+` if the solution is correct and complete, or `+"`VERDICT: FAIL`"+` if any mathematical error or material gap remains.`,
		context.Section, ex.SectionID, ex.Number, ex.Rating, ex.Body, solution)
	return correctnessReviewerInstructions, input
}

func (Builder) ReviewProcess(ex exercise.Exercise, context exercise.Context, solution string) (string, string) {
	input := fmt.Sprintf(`<audit_contract>
Read the proposal in logical order. For each substantive step, check whether it follows from prior steps, the exercise, or the section context. Locate the earliest material error rather than merely summarizing later consequences. Independently recompute the final answer, count, bound, construction, or algorithm behavior. Test omitted cases, necessity and sufficiency, optimality lower bounds, invariants, termination, and the precise question asked. Treat a style preference as non-material unless it makes the argument ambiguous or violates an explicit output requirement.
</audit_contract>

<section_context>
%s
</section_context>

<exercise>
Section %s, Exercise %d, difficulty %s

%s
</exercise>

<proposed_solution>
%s
</proposed_solution>

Return these headings in order:

## Step Audit
Check the reasoning in order and quote only the minimum text needed to identify a step.

## Earliest Material Error
Write `+"`EARLIEST_ERROR: NONE`"+` if every step is sound. Otherwise write `+"`EARLIEST_ERROR: <location>`"+` and explain the error.

## Independent Final Check
Recompute or cross-check the conclusion without relying on the proposal's derivation.

## Requirement Coverage
Check every explicit and implicit obligation in the exercise.

## Verdict
On the final line, write exactly `+"`VERDICT: PASS`"+` if no material error or omission exists, or `+"`VERDICT: FAIL`"+` otherwise.`,
		context.Section, ex.SectionID, ex.Number, ex.Rating, ex.Body, solution)
	return processReviewerInstructions, input
}

// ReviewCorrectness is the default single-review prompt for callers that need
// one review outside the full two-judge solver flow.
func (b Builder) Review(ex exercise.Exercise, context exercise.Context, solution string) (string, string) {
	return b.ReviewCorrectness(ex, context, solution)
}

func (Builder) Correct(ex exercise.Exercise, context exercise.Context, solution, review string) (string, string) {
	input := fmt.Sprintf(`<task>
Produce a corrected, publication-ready solution to the exercise. Address every material issue in the review. Rebuild the argument when the approach is unsound. Preserve correct work only after checking it independently. Return only the corrected solution, with no discussion of the review or revision process.
</task>

<section_context>
%s
</section_context>

<exercise>
Section %s, Exercise %d, difficulty %s

%s
</exercise>

<previous_solution>
%s
</previous_solution>

<review>
%s
</review>

The corrected solution must follow the same quality rules as a first solution: rigorous derivation, exact source notation, LaTeX mathematics, proof of both directions or matching bounds where required, no em dashes, no horizontal rules, no filler, and no references to the model or workflow.`,
		context.Section, ex.SectionID, ex.Number, ex.Rating, ex.Body, solution, review)
	return solverInstructions, input
}

func (Builder) Compare(ex exercise.Exercise, context exercise.Context, candidateA, candidateB string) (string, string) {
	instructions := `You are a senior mathematics and computer science examiner comparing two anonymous solutions. Judge only their contents. Derive the problem independently, apply the same standard to both, and do not infer authorship or origin.`
	input := fmt.Sprintf(`<comparison_contract>
Create a problem-specific marking scheme, then assess each solution on a 0 to 7 scale for correctness, completeness, rigor, exact instruction fulfillment, notation, and clarity. Mathematical validity dominates style. Identify the earliest material error in each solution. Prefer a solution only when it is materially stronger. Use TIE when they are equivalent or the difference is merely stylistic.
</comparison_contract>

<section_context>
%s
</section_context>

<exercise>
Section %s, Exercise %d, difficulty %s

%s
</exercise>

<solution_a>
%s
</solution_a>

<solution_b>
%s
</solution_b>

Return these headings in order:

## Reference Approach
## Marking Scheme
## Solution A Assessment
Include `+"`SCORE_A: N/7`"+`.
## Solution B Assessment
Include `+"`SCORE_B: N/7`"+`.
## Comparison
On the final line, write exactly one of `+"`WINNER: A`"+`, `+"`WINNER: B`"+`, or `+"`WINNER: TIE`"+`.`,
		context.Section, ex.SectionID, ex.Number, ex.Rating, ex.Body, candidateA, candidateB)
	return instructions, input
}

func numericRating(value string) int {
	match := ratingNumber.FindString(value)
	if match == "" {
		return 0
	}
	n, _ := strconv.Atoi(match)
	return n
}

func formatGuidance(rating int) string {
	switch {
	case rating <= 10:
		return "Give a direct answer in one or two short paragraphs without a section heading. If replacements or steps must be counted, show each operation before stating the total."
	case rating <= 25:
		return "Use ## Solution as the only required heading. Add ## Notes only for a genuinely useful observation. Keep algorithms compact while proving the invariant and any optimality claim."
	case rating < 46:
		return "Use ## Setup, ## Solution, and ## Verification. The verification must check the most failure-prone part by a logically independent route. Add ## Notes only when useful."
	default:
		return strings.Join([]string{
			"Treat this as a research-level problem.",
			"Do not invent a complete proof or literature result.",
			"Use ## Setup, ## Known Results, ## Partial Argument, and ## Status.",
			"State plainly what is proved, what relies on a cited result, and what remains open.",
		}, " ")
	}
}
