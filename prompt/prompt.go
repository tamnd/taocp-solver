package prompt

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tamnd/taocp-solver/exercise"
)

const solverInstructions = `You are an expert mathematics and computer science author preparing a rigorous study companion for The Art of Computer Programming. Work privately through the problem before drafting. Return only the finished solution in Markdown. Follow the source notation exactly. Never claim a result that you have not derived or justified.`

const correctnessReviewerInstructions = `You are an independent mathematics and computer science truth evaluator. Solve the exercise independently, build a problem-specific marking scheme, and decide whether the proposed solution establishes a true answer. Return explicit truth and publication-quality fields. Do not rewrite the proposal.`

const processReviewerInstructions = `You are an adversarial mathematical proof auditor. Try to falsify the proposed solution by checking its steps, constructing counterexamples, testing boundary cases, and independently verifying its conclusion. Return an explicit truth decision. Do not rewrite the proposal.`

var ratingNumber = regexp.MustCompile(`\d+`)

type Builder struct{}

func (Builder) Reference(ex exercise.Exercise, context exercise.Context) (string, string) {
	instructions := `You are preparing a private verification reference for a TAOCP exercise. Solve it independently before seeing any proposed answer. Extract every explicit and implicit obligation, derive the result, and design concrete falsification checks. Accuracy is more important than presentation.`
	input := fmt.Sprintf(`<section_context>
%s
</section_context>

<exercise>
Section %s, Exercise %d, difficulty %s

%s
</exercise>

Return these headings in order:

## Independent Derivation
Derive the result without assuming a candidate approach.

## Obligations
Number every condition a complete solution must establish.

## Failure Modes
List plausible wrong answers, missing cases, circular arguments, invalid complexity claims, and notation traps specific to this exercise.

## Falsification Checks
Give small cases, boundary cases, invariants, alternative computations, or counterexamples that can distinguish a valid solution from a polished but false one.

## Reference Conclusion
State the exact conclusion that follows from the derivation.`, context.Section, ex.SectionID, ex.Number, ex.Rating, ex.Body)
	return instructions, input
}

func (Builder) Select(ex exercise.Exercise, reference string, candidates []string) (string, string) {
	instructions := `You are selecting the strongest proof from an independently generated population. Use the private reference as a fallible checking aid, verify it where needed, and prefer mathematical validity and completeness over style. Reject consensus when all candidates share an error.`
	var population strings.Builder
	for i, candidate := range candidates {
		fmt.Fprintf(&population, "\n<candidate_%d>\n%s\n</candidate_%d>\n", i+1, candidate, i+1)
	}
	input := fmt.Sprintf(`<exercise>
Section %s, Exercise %d

%s
</exercise>

<private_reference>
%s
</private_reference>

<candidate_population>%s
</candidate_population>

Compare each candidate obligation by obligation. Check its conclusion independently, identify the earliest material error or omission, and note useful reasoning that could repair another candidate. Select the candidate requiring the least substantive repair. Do not average incompatible arguments and do not select by prose polish.

Return these headings in order:

## Obligation Matrix
## Candidate Findings
## Independent Conclusion Check
## Selection Rationale
## Selection
On the final line write exactly `+"`SELECTED: N`"+`, where N is the chosen candidate number.`, ex.SectionID, ex.Number, ex.Body, reference, population.String())
	return instructions, input
}

func (Builder) CompareQuality(ex exercise.Exercise, context exercise.Context, candidateA, candidateB string) (string, string) {
	instructions := `You are a blind evaluator of two TAOCP solutions. Solve the exercise independently. Judge mathematical truth, completeness, self-containment, human readability, and verifiability. Do not infer how either solution was produced, and do not reward length or polish by itself.`
	input := fmt.Sprintf(`<section_context>
%s
</section_context>

<exercise>
Section %s, Exercise %d, difficulty %s

%s
</exercise>

<candidate_a>
%s
</candidate_a>

<candidate_b>
%s
</candidate_b>

Build a problem-specific obligation list. For each candidate, identify the earliest material error or gap, independently check the final conclusion, and score it from 0 to 7. A score of 6 or 7 requires a true and complete solution. Prefer a candidate only for a material difference. Return TIE when both are equivalent.

Return these headings in order:

## Independent Reference
Derive the exact result without relying on either candidate.

## Obligation Matrix
Check every obligation for both candidates with concise evidence.

## Candidate A Audit
State its earliest material error or `+"`EARLIEST_ERROR_A: NONE`"+` and independently check its conclusion.

## Candidate B Audit
State its earliest material error or `+"`EARLIEST_ERROR_B: NONE`"+` and independently check its conclusion.

## Comparison
Explain only material quality differences.

## Final Fields
Return these fields, one per line:
`+"`SCORE_A: N/7`"+`
`+"`SCORE_B: N/7`"+`
`+"`TRUTH_A: TRUE`"+` or `+"`TRUTH_A: FALSE`"+`
`+"`TRUTH_B: TRUE`"+` or `+"`TRUTH_B: FALSE`"+`
`+"`WINNER: A`"+`, `+"`WINNER: B`"+`, or `+"`WINNER: TIE`"+` on the final line.`, context.Section, ex.SectionID, ex.Number, ex.Rating, ex.Body, candidateA, candidateB)
	return instructions, input
}

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
Write a precise, economical, fully rigorous textbook solution for a knowledgeable human reader. It must be self-contained and directly verifiable from the displayed argument.

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
11. Write in the natural voice of a careful TAOCP solution author. Do not include meta-commentary, a rubric, a proposal label, or commentary about how the answer was produced.
12. Make the verification visible. State the invariant, independent check, boundary case, matching bound, or counterexample test that is appropriate for this particular exercise.
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

func (b Builder) SolveCandidate(ex exercise.Exercise, context exercise.Context, number int) (string, string) {
	instructions, input := b.Solve(ex, context)
	lenses := []string{
		"Follow the most direct source-aligned derivation and make every invariant explicit.",
		"Develop an independent route that emphasizes boundary cases, counterexample resistance, and necessity as well as sufficiency.",
		"Seek the cleanest complete argument, checking constructions against matching bounds and conclusions against small cases.",
		"Work verifier-first: identify the claims most likely to fail, then build a proof with explicit evidence for each one.",
		"Prefer a concise alternate formulation while preserving every logical and computational detail needed for verification.",
	}
	if number < 1 || number > len(lenses) {
		number = 1
	}
	input += fmt.Sprintf("\n\n<independence_guidance>\n%s\nDo not refer to this guidance in the finished solution.\n</independence_guidance>", lenses[number-1])
	return instructions, input
}

func (Builder) ReviewCorrectness(ex exercise.Exercise, context exercise.Context, solution string) (string, string) {
	return Builder{}.ReviewTruth(ex, context, "No separate reference was supplied. Derive one independently.", solution)
}

func (Builder) ReviewTruth(ex exercise.Exercise, context exercise.Context, reference, solution string) (string, string) {
	input := fmt.Sprintf(`<grading_contract>
First derive a concise reference approach and a problem-specific marking scheme. Then grade the proposal on a 0 to 7 scale:

7: fully correct, complete, rigorous, and responsive to every instruction.
6: correct and complete with only a minor presentation defect.
5: essentially correct with a small localized justification gap.
3-4: meaningful progress, but at least one major gap or error remains.
1-2: limited valid progress or a fundamentally wrong approach.
0: no meaningful correct progress.

A true solution must reach the correct conclusion through valid reasoning. A publishable solution must also be complete, self-contained, human-readable, and directly verifiable. A correct guess without a sufficient argument is incomplete. Accept valid approaches that differ from your reference. Actively check for circular reasoning, missing cases, invalid asymptotics, unproved optimality, and plausible but false intermediate claims.
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

<private_reference>
%s
</private_reference>

Return these headings in order:

## Reference Approach
Give the result and the key steps of an independent solution.

## Marking Scheme
List the problem-specific obligations required for a complete solution.

## Assessment
Grade each obligation and identify any unsupported claim.

## Score
Write `+"`SCORE: N/7`"+` on its own line, with an integer N.

## Decision Fields
Write each field on its own line:
`+"`TRUTH: TRUE`"+` only if the mathematical claims and final answer are true, otherwise `+"`TRUTH: FALSE`"+`.
`+"`COMPLETE: YES`"+` only if the argument establishes every required part, otherwise `+"`COMPLETE: NO`"+`.
`+"`SELF_CONTAINED: YES`"+` only if the argument can be checked from the exercise, supplied context, and stated facts, otherwise `+"`SELF_CONTAINED: NO`"+`.
`+"`HUMAN_READABLE: YES`"+` only if a knowledgeable reader can follow the notation and logic without reconstructing missing steps, otherwise `+"`HUMAN_READABLE: NO`"+`.
`+"`VERIFIABLE: YES`"+` only if the derivation exposes enough evidence to check its central claims, otherwise `+"`VERIFIABLE: NO`"+`.

## Verdict
On the final line, write exactly `+"`VERDICT: PASS`"+` only when all five decision fields are affirmative, or `+"`VERDICT: FAIL`"+` otherwise.`,
		context.Section, ex.SectionID, ex.Number, ex.Rating, ex.Body, solution, reference)
	return correctnessReviewerInstructions, input
}

func (Builder) ReviewProcess(ex exercise.Exercise, context exercise.Context, solution string) (string, string) {
	input := fmt.Sprintf(`<audit_contract>
Read the proposal in logical order. For each substantive step, check whether it follows from prior steps, the exercise, or the section context. Locate the earliest material error rather than merely summarizing later consequences. Independently recompute the final answer, count, bound, construction, or algorithm behavior. Search for a concrete counterexample and test small, boundary, and degenerate cases. Test necessity and sufficiency, optimality lower bounds, invariants, termination, and the precise question asked. Treat a style preference as non-material unless it prevents verification.
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

## Truth Decision
Write `+"`TRUTH: TRUE`"+` on its own line only if the attempted falsification found no false claim or material logical gap. Otherwise write `+"`TRUTH: FALSE`"+`.

## Verdict
On the final line, write exactly `+"`VERDICT: PASS`"+` if `+"`TRUTH: TRUE`"+`, or `+"`VERDICT: FAIL`"+` otherwise.`,
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
