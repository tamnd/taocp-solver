# Changelog

All notable changes to `taocp` are recorded here. The project follows Semantic Versioning.

## [Unreleased]

### Added

- `taocp bridge`, an OpenAI-compatible local server that puts a ChatGPT account behind `/v1/chat/completions`, `/v1/responses`, `/v1/models`, and `/v1/health`. The solver no longer needs a separate program to reach that backend.
- Access tokens are refreshed before expiry and written back atomically at mode 0600, with a backup, a lock, and unrecognised fields preserved, so a credential file shared with another tool survives the write.
- An exhausted plan is reported as its own condition with the exact reset instant and a `Retry-After`, distinct from a transient rate limit, which is retried in place, and from a rejected model slug, which is a request error.
- A route file, given with `--routes` or `--route`, names several ranked endpoints so a run continues on the next one when the current one runs out of quota, loses its credential, or stops answering. Cooldowns match the cause rather than being uniform, and every attempt records the route that served it.
- Results report a `by_route` token and cost breakdown once more than one route contributed, and the saved `model` names the models that actually answered rather than the one that was requested.
- `taocp doctor` probes every route, prints a table or JSON, compares each configured model against the endpoint's catalogue, and exits non-zero when nothing is live, so it can guard an unattended run. `--write-routes` dumps the effective file for editing and `--suggest-routes` refreshes one from the live catalogues, appending unknown models disabled.

### Changed

- The matrix values a free-route execution at the paid token rate for the same underlying model instead of recording it as costing zero. A free promotion is a billing state, not a measure of what the work is worth.
- Reports carry the rate card and separate uncached-input, cached-input, cache-write, and output components, so an aggregate total can be checked against its parts.
- A model with no published paid per-token rate reports its cost as unavailable rather than zero.

## [0.1.0] - 2026-07-20

### Added

- A Go library and command-line program for solving exercises from a local TAOCP content repository.
- Fast mode for single-call solutions and slow mode for population generation, selection, independent truth checking, adversarial falsification, and bounded correction.
- Complete token accounting for input, cached input, cache writes, output, reasoning output, and totals.
- Standard GPT-5.6 list-cost estimates, including cache and long-context pricing rules.
- Atomic JSON and Markdown result storage with complete attempt and evaluation records.
- Blind, order-reversed comparisons of fast and slow solution quality, tokens, and estimated cost.
- Cross-platform release archives, Linux packages, container images, checksums, SBOMs, and signed release metadata.
