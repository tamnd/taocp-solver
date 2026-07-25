# Evaluation

## Live fast and slow benchmark

The end-to-end regression exercise is TAOCP 7.2.1.2 Exercise 97, which asks for a method to generate all derangements of \(\{1,\ldots,n\}\). The benchmark used `gpt-5.6-sol` at high reasoning effort through the local bridge.

Fast mode made one generation call and performed no verification. Slow mode made seven calls:

1. Build an independent reference, obligation list, failure-mode list, and falsification plan before seeing any candidate.
2. Generate three independently guided candidates.
3. Select the strongest candidate obligation by obligation.
4. Check the selected candidate with a reference-grounded truth judge.
5. Check it again with a reference-blind adversarial audit.

The selector chose candidate 2. The truth judge and audit judge both passed it without a correction call. The stored evaluation certified that it is true, complete, self-contained, human-readable, and verifiable.

## Quality result

Two additional blind judges compared the fast and slow solutions. The second judge received the solutions in reversed order. Both reports contain an independent derivation, an obligation matrix, separate earliest-error audits, and a material-difference analysis. Both judges returned the same result.

| Judge order | Fast score | Slow score | Fast true | Slow true | Winner |
| --- | ---: | ---: | --- | --- | --- |
| fast, slow | 7/7 | 7/7 | true | true | tie |
| slow, fast | 7/7 | 7/7 | true | true | tie |

The fast solution already gave a correct fixed-point invariant, constant-time update, exact derangement count, recurrence check, boundary cases, and complexity argument. The slow solution made several details more explicit, including the exact adjacent positions used by Algorithm P, the reason intermediate nonderangements cannot generally be skipped, and the carry-work argument. The blind judges did not consider those differences material enough to prefer one solution.

For this exercise, slow mode increased confidence and retained a detailed audit trail, but it did not produce a measurable quality win over fast mode.

## Tokens and standard list cost

| Scope | Requests | Input | Cached input | Cache write | Output | Reasoning output | Total | List-cost estimate |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Fast production | 1 | 3,815 | 0 | 0 | 6,252 | 5,178 | 10,067 | $0.206635 |
| Slow production and verification | 7 | 34,754 | 0 | 0 | 49,320 | 38,179 | 84,074 | $1.653370 |
| Blind quality comparison | 2 | 11,584 | 0 | 0 | 9,482 | 6,705 | 21,066 | $0.342380 |

Slow mode used 74,007 more tokens than fast mode, or 8.35 times as many. Its standard list-cost estimate was $1.446735 higher, or 8.00 times the fast estimate. The blind quality comparison is reported separately and is not included in either production cost.

These are standard GPT-5.6 Sol API list-cost estimates applied to provider-reported token usage. They are not claims about subscription charges. The endpoint reported no cached-input or cache-write tokens for this run.

## Reproduction

Start the bridge, leave it running, and drive the benchmark through it:

```sh
taocp bridge --port 8790
```

```sh
go run ./cmd/taocp benchmark 7.2.1.2 97 \
  --base-url http://localhost:8790/v1 \
  --model gpt-5.6-luna \
  --source /Users/apple/github/tamnd/taocp \
  --output /Users/apple/data/taocp-solver-eval \
  --candidates 3 \
  --timeout 30m \
  --json
```

The recorded run used `gpt-5.6-sol`, which the bridge no longer reaches. `GET /v1/models` on a running bridge lists the slugs the current credential may use, and `gpt-5.6-luna` is the closest substitute. Token counts and cost estimates from a rerun will not match the numbers above, so treat them as a fresh measurement rather than a check of this one.

## Limits

This is one high-signal regression case, not a population-level benchmark. The generator and evaluators use the same model family, so their decisions are automated evidence rather than a substitute for expert review or formal proof checking. Broader claims require a stratified sample across volumes, difficulty ratings, exercise types, and independently reviewed references. The benchmark command makes those larger studies reproducible while keeping generation and evaluation costs separate.
