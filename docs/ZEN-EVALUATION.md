# Zen evaluation, 2026-07-20

This run compares five limited-time Zen routes on five exercises spanning levels 5, 15, 25, 30, and 35. Every available model used fast mode, the same exercise source, the same solver prompt, and a 32,768-token output ceiling. The evaluator was fixed to GPT-5.6 Sol.

The run completed 15 of 25 planned cases. The remaining 10 were unavailable because the Zen daily free quota for DeepSeek V4 Flash Free and MiMo-V2.5 Free was exhausted. Provider errors are availability results, not incorrect solutions, and do not enter capability-rate denominators.

## Capability

| Model | Coverage | True | Publishable | Mean score |
|:--|--:|--:|--:|--:|
| Nemotron 3 Ultra Free | 5/5 | 4/5 | 4/5 | 6.00/7 |
| Hy3 Free | 5/5 | 3/5 | 3/5 | 5.60/7 |
| North Mini Code Free | 5/5 | 2/5 | 2/5 | 5.20/7 |
| DeepSeek V4 Flash Free | 0/5 | unavailable | unavailable | unavailable |
| MiMo-V2.5 Free | 0/5 | unavailable | unavailable | unavailable |

Nemotron passed levels 5 through 30 and failed level 35. Hy3 passed levels 5, 15, and 25. North passed levels 5 and 25. A pass required agreement between a reference-grounded criteria judge and a separately prompted reference-blind falsification judge, plus a criteria score of at least 6/7.

These are five observations per available model, not a claim about population-level capability. The fixed exercise set makes reruns comparable, while the small sample keeps the run practical enough to repeat when routes change.

## Accepted token and cost accounting

Reasoning tokens are included within output tokens and are shown separately for analysis. They must not be added to the total again.

| Model | Requests | Input | Cached input | Output | Reasoning | Total | Paid-equivalent generation cost | Audit tokens | Audit list cost |
|:--|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| Nemotron 3 Ultra Free | 5 | 23,629 | 4,352 | 44,837 | 35,306 | 68,466 | $0.173850 | 146,626 | $1.187755 |
| Hy3 Free | 5 | 22,849 | 3,904 | 11,280 | 0 | 34,129 | $0.013008 | 112,815 | $1.418925 |
| North Mini Code Free | 5 | 22,381 | 0 | 46,412 | 37,077 | 68,793 | unavailable | 102,285 | $1.377925 |

Accepted generation used 15 requests, 68,859 input tokens, 8,256 cached input tokens, 102,529 output tokens, and 171,388 total tokens. Of the output total, 72,383 were reported as reasoning tokens. The 30 solution-audit requests used 274,687 input tokens, 87,039 output tokens, and 361,726 total tokens, including 54,728 reported reasoning tokens. Their standard GPT-5.6 Sol list cost was $3.984605.

Five fixed-reference requests used another 20,280 input tokens, 30,977 output tokens, and 51,257 total tokens, including 19,035 reported reasoning tokens. Their standard GPT-5.6 Sol list cost was $1.030710. The complete accepted run therefore accounted for 584,371 tokens and $5.015315 in evaluator list cost. The paid-equivalent generation value is $0.186858 for the two models with published paid token routes. North Mini Code is excluded from that sum because no paid per-token rate is published.

The accepted-case figures intentionally exclude unsuccessful transport attempts. The proxy trace retains every request, response, retry, latency record, and reported usage event so operational overhead can be analyzed separately without charging failed infrastructure work to model capability.

## Paid-equivalent prices

Nemotron uses an OpenRouter paid-route card of $0.60 input, $0.20 cached input, and $3.60 output per million tokens. Hy3 uses an OpenRouter paid-route card of $0.20 input, $0.05 cached input, and $0.80 output per million tokens. DeepSeek and Xiaomi direct rates are recorded for the two quota-limited models and will be applied when those cases complete. Cohere publishes North Mini Code as free for trial and production access and publishes no paid per-token rate, so the report does not invent one.

## Availability and retries

Each DeepSeek V4 Flash Free and MiMo-V2.5 Free case returned HTTP 429 with an advertised retry delay of about 8 hours 29 minutes. The runner skipped that impractical inline wait, finished every other route, then retried all 10 quota-limited cases once in a deferred pass. They remained unavailable. Each case records `rate_limit_deferrals: 1`, the final provider response, and a deduplicated error history. A later `--resume` run will revisit only those cases.

## Reproduction and integrity

The run used this command from the repository root:

```sh
TAOCP_BRIDGE_URL=http://127.0.0.1:8791/v1 go run ./cmd/taocp matrix \
  --source /path/to/taocp \
  --output /path/to/output \
  --models deepseek-v4-flash-free,mimo-v2.5-free,hy3-free,nemotron-3-ultra-free,north-mini-code-free \
  --parallel 2 --timeout 30m --retries 4 \
  --deferred-rate-limit-retries 1 --resume
```

The local `report.json` SHA-256 is `4a139a784c5ec5063d2e75af82046013c3c42642188072a0f0edc4820d559f89`. The generated Markdown report SHA-256 is `d7667365639ee50aaf6263eb5789fde3422c27879498e40d693dcdfdc34b7e78`.
