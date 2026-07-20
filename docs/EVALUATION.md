# Evaluation

## Live bridge check

The first end-to-end evaluation used TAOCP 7.2.1.2 Exercise 97, which asks for a method to generate all derangements of $\{1,\ldots,n\}$. The run used `gpt-5.6-sol` at high reasoning effort through the local bridge.

The solver produced an explicit adaptation of Algorithm P:

- maintain the fixed-point count $f=\#\{i:a_i=i\}$;
- subtract the two old fixed-point contributions before each adjacent swap;
- perform the swap;
- add the two new contributions;
- visit the permutation exactly when $f=0$.

The solution proves the invariant, completeness, exclusion of nonderangements, uniqueness, and termination. It derives

$$
D_n=n!\sum_{k=0}^{n}\frac{(-1)^k}{k!},
$$

checks small cases, proves that $D_n$ is a constant fraction of $n!$, and distinguishes constant-time visits from the cost of copying each length-$n$ output.

The correctness judge created a seven-obligation marking scheme and assigned 7/7. The process judge audited ten reasoning steps, independently checked the derangement recurrence and small values, and reported `EARLIEST_ERROR: NONE`. Both judges returned PASS.

## Comparison with tamnd/brain

The matching existing entry is:

```text
tamnd/brain/content/en/practice/maths/taocp/vol4/7.2.1.2/97.md
```

That entry contains workflow instructions about waiting for an exercise, proposed solution, and reviewer feedback. It does not contain an algorithm or mathematical answer to the exercise.

The comparison evaluator received anonymous solutions A and B. It ran twice, reversing their order on the second run to expose position bias.

| Run | New position | New score | Brain score | Winner |
| --- | --- | ---: | ---: | --- |
| 1 | A | 7/7 | 0/7 | A |
| 2 | B | 7/7 | 0/7 | B |

After mapping both anonymous positions back to their sources, both runs selected the new result. The final comparison winner was `new`.

Each judge independently identified the existing entry's earliest material defect: it immediately discusses missing review inputs instead of solving the stated derangement-generation problem. Each judge found no material error in the new solution.

## Reproduction

Start a compatible local bridge on port 8790, then run:

```sh
go run ./cmd/taocp solve 7.2.1.2 97 \
  --base-url http://localhost:8790/v1 \
  --model gpt-5.6-sol \
  --source /Users/apple/github/tamnd/taocp \
  --output /Users/apple/data/taocp-solver-eval \
  --timeout 30m

go run ./cmd/taocp compare 7.2.1.2 97 \
  --base-url http://localhost:8790/v1 \
  --model gpt-5.6-sol \
  --source /Users/apple/github/tamnd/taocp \
  --output /Users/apple/data/taocp-solver-eval \
  --timeout 30m \
  --json
```

## Limits

This is one high-signal regression case, not a population-level benchmark. The comparison uses the same model family for judging, so its scores are automated evidence rather than a substitute for expert review. The order reversal controls one common comparison bias, but broader quality claims require a stratified sample across volumes, difficulty ratings, exercise types, and independently reviewed references.
