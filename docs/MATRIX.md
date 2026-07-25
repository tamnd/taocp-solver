# Model matrix

The model matrix measures solution quality, verification confidence, tokens, cost, and latency under one reproducible protocol. Its purpose is comparison, not certification.

## Exercise sample

The built-in sample uses five exercises with published difficulty levels 5, 15, 25, 30, and 35. The set includes direct induction, diagnosis of a proof flaw, an identity proof, symbolic derivation, and an algorithm with a proof obligation. The manifest stores the exact section and exercise number, so every run uses the same source material.

## Generation modes

Fast mode makes one solution call and performs no verification. Slow mode constructs an independent reference before candidate generation, creates a configurable population of solutions, selects by problem-specific obligations, applies two checks, and performs bounded correction when a check fails.

The default matrix runs every model in fast mode. It also runs GPT-5.6 Sol in slow mode on the same five exercises. This matched comparison measures the marginal quality, token use, list cost, and elapsed time of verification.

## Independent evaluation

All generated solutions are judged by one fixed GPT-5.6 Sol evaluator. The evaluator does not receive the generator's identity.

For each exercise, it first builds one fixed reference solution, obligation list, and expected conclusion. That reference is reused for every model and mode. Reuse prevents a different evaluator sample from becoming a hidden source of variance between models.

Each proposed solution then receives two checks with different information and failure-search strategies:

1. The criteria judge sees the fixed reference. It derives a problem-specific marking scheme and separately decides truth, completeness, self-containment, readability, and verifiability.
2. The falsification judge does not see the reference. It follows the proposed reasoning in order, searches for the earliest material error, constructs counterexamples, tests boundary cases, and independently checks the conclusion.

A solution is counted as true only when both judges find it true. It is counted as publishable only when both judges pass, every quality field is affirmative, and the criteria score is at least 6 out of 7. Missing fields fail closed. Judge disagreements remain visible in the case artifact.

This is stronger than one sequential proof audit because it combines criteria decomposition, a fixed reference, information separation, earliest-error localization, adversarial falsification, and independent conclusion checking. It is still automated evidence. It is not a formal proof checker or a replacement for expert review.

## Failures and denominators

Provider failures and evaluator failures are never silently converted into incorrect solutions. The report records them separately. Truth and publishable rates use completed evaluations as their denominator, while planned and completed counts expose missing coverage.

Limited-time Zen routes refuse advertised retry delays over 60 seconds. A daily quota exhaustion can advertise a delay of many hours; recording the provider failure immediately keeps the remaining matrix runnable. The fixed evaluator uses one immediate retry. After the first pass through all models, the runner retries generator and evaluator rate limits once by default. `--deferred-rate-limit-retries` controls that final pass. If a route is still unavailable, `--resume` can revisit it later without rerunning completed cases or regenerating checkpointed solutions. Other profiles inherit the command-level retry policy.

## Token and cost accounting

Every request preserves input, uncached input, cached input, cache write, output, reasoning output, and total tokens. Reasoning tokens are a subset of output tokens and are not added twice.

Generation and evaluation are reported separately. Official GPT rows apply published standard API rates to provider-reported usage. Zen executions are valued at paid per-token rates for the same underlying model. DeepSeek V4 Flash and MiMo-V2.5 use their creators' direct rates. Hy3 and Nemotron 3 Ultra use published paid inference-route rates. Cohere publishes North Mini Code as free for both trial and production keys and publishes no paid per-token rate, so the report marks that value unavailable. Local rows have no API list price, and the report does not invent electricity or hardware cost.

Every generated report includes the paid rate card and separate uncached-input, cached-input, cache-write, and output cost components. An unpublished component is shown as unavailable, not silently treated as zero.

Each case JSON stores the complete published price card and source URL. The aggregate Markdown report is a compact view of the same data.

## Reproduction

Write the built-in manifest before a run if you want to review or modify every model and endpoint:

```sh
taocp matrix --write-manifest matrix.json
```

The default endpoint layout is:

```text
Zen tracing proxy:      http://127.0.0.1:8788/v1
GamingPC tracing proxy: http://127.0.0.1:8789/v1
taocp bridge:           http://127.0.0.1:8790/v1
```

8788 and 8789 are external tracing proxies you start yourself. 8790 is `taocp bridge`, in this repository.

Run the matrix with resumable per-case artifacts:

```sh
taocp matrix \
  --manifest matrix.json \
  --source /path/to/taocp-content \
  --output /path/to/matrix-results \
  --parallel 2 \
  --resume
```

Use `--models` or `--modes` for a smaller diagnostic run. A complete run writes `report.json`, `REPORT.md`, fixed references, individual case files, and all generated solution artifacts.

Matrix requests default to a 32,768 completion-token ceiling. This leaves room for a detailed proof while preventing a nonterminating compatible stream from consuming unbounded time and storage. Use `--max-output-tokens` to change the ceiling for a controlled experiment.

## Research basis

The protocol draws on criteria-decomposed verification, earliest-error process evaluation, population generation and selection, fine-grained proof grading, and explicit assessment of proof quality beyond final-answer correctness. Relevant references are listed in the project README.
