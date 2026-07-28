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
- `taocp publish` renders the result store into the site content tree and rebuilds the section, volume, and top indexes. The format matches what is already published byte for byte, and the golden tests are copies of live pages, because a renderer that drifted by one byte would rewrite the whole corpus.
- An unchanged solution is left alone rather than rewritten, so it keeps its original publication date. The comparison ignores the `date:` line, which is stamped at render time, and two runs over an unchanged store leave the content repository's `git status` empty.
- Every page passes the leak gate before it is written. A stored solution that trips the gate is not published, and a live page that now trips it is deleted and counted on its own line.
- `--check` reports what would change, writes nothing, and exits non-zero when the tree is out of date, which makes it usable as a pre-commit hook or a scheduled guard.
- Published dates carry a real `Asia/Ho_Chi_Minh` clock. The previous publisher stamped a UTC time and labelled it `+07:00`, so every date was seven hours early against its own offset.
- A section index falls back to the pages already published when the source repository has nothing to list for it, instead of rendering a table with a header and no rows.
- Figures travel with the page that references them. Exercise bodies link images the way the source repository is laid out, which resolves to nothing in the published tree, so the file is copied next to the page and the link becomes a bare filename. A remote image and a link the source cannot resolve are both left alone.
- `TAOCP_SOLVER_BRAIN` names the content repository, defaulting to `~/github/tamnd/brain`.
- `taocp coverage` reports which exercises are still missing and where, over the source repository, the result store, and the published tree. It needs no database and keeps no state, because all three inputs are directories and every run recomputes the answer.
- Coverage counts published-but-unstored exercises as `imported` rather than missing. On a fresh install the store is empty while thousands of pages are published, and queueing those would spend a model on work that already exists.
- `--missing` writes the work queue one section and number per line, sorted so consecutive runs give the same order and an interrupted run needs no cursor.
- `--orphans` lists published exercises the source repository does not enumerate. It reports and never deletes, because where the two disagree it is usually the extraction that is incomplete and the published page is the only record that the exercise exists.
- `taocp run` works the coverage queue unattended: solve, store, publish that one exercise, and commit the content repository on its own timer. It is meant to be started in a screen session or under systemd and left for days.
- Every exercise is published the moment it is solved rather than at the end, so an interrupted run leaves the content repository consistent instead of holding a batch that dies with the process.
- The queue is recomputed from the three directories on every pass, so a run that is killed needs no cursor and loses at most the exercise that was in flight.
- A stored result with no solution is a tombstone: the exercise stays out of the queue until `--retry-empty` asks for it, which is what stops a week-long run from spending every pass on the same unsolvable exercise.
- When every route is cold the run sleeps until the earliest one returns, capped by `--max-sleep`, instead of exiting and losing the campaign to a quota window.
- SIGINT and SIGTERM stop the queue, let the solves already in flight finish for up to `--drain`, force a final commit, and exit zero.
- A commit says which solutions went into it. The runner publishes each proof as it lands, so most commits hold one exercise, and `Add 1 solution` said nothing the file count did not. Past three the subject falls back to counts with a per-section breakdown.
- A run logs what is in flight, not only what has finished. Every solve reports its start and each step it reaches, and every line names its exercise, because the engine is shared and at `--parallel 2` two solves otherwise braid into one unreadable stream.
- A route changing state is logged. Failover is the pool's whole purpose and it used to happen silently, which left a log where a campaign slowed down overnight and nothing said why.
- Every git command runs under an advisory lock, so a second runner or a leftover cron job on the same host cannot interleave with it.
- A run takes the host's route file from `TAOCP_ROUTES` or the default path without being passed a flag, so a machine configured by an environment file gets failover instead of demanding a single `--base-url`.
- `--dry-run` prints the queue and touches nothing, and does not need a working endpoint to do it.
- A review that comes back without its decision lines is asked again, up to three times, with the format restated. A reviewer refusing to sign its verdict used to discard a finished solution, which threw away the expensive half of an hour-long solve; on a weaker free model that was most of the campaign.
- Decision lines are read through their decoration. A model that bolds, bullets, quotes, or puts a full stop after `VERDICT: PASS` has still decided, and losing a solve to a pair of asterisks is not a gate, it is a bug. A verdict mentioned mid-sentence is still rejected.
- `make dist` cross-compiles a static Linux binary and `make deploy HOST=...` installs it with a systemd user unit, because a run host is not required to have a Go toolchain.

### Changed

- The zen routes default to the upstream endpoint instead of a local tracing proxy. Every one of them was dead out of the box on a machine with nothing listening on that port, which is a poor first impression and no gain. `TAOCP_ZEN_PROXY_URL` still puts a proxy back in front.
- The default matrix runs `gpt-5.6-luna` as its deep model and its evaluator, and no longer lists `gpt-5.6-sol`. No ChatGPT-account credential may use that slug on any plan, so the row was a guaranteed rejection and the evaluator could never score a grid.
- The matrix values a free-route execution at the paid token rate for the same underlying model instead of recording it as costing zero. A free promotion is a billing state, not a measure of what the work is worth.
- Reports carry the rate card and separate uncached-input, cached-input, cache-write, and output components, so an aggregate total can be checked against its parts.
- A model with no published paid per-token rate reports its cost as unavailable rather than zero.

### Fixed

- A stop no longer interrupts a commit that is already under way. Cancellation decides whether the next commit starts; a signal landing between `git commit` and `git push` used to kill the sequence and leave the working copy holding solutions the remote had never seen.
- A candidate line no longer ends in a dangling `with `. Under a route pool the request names no model, because which one answers is the pool's decision.
- The route file test no longer reads whatever personal route file the developer happens to have, so it stops passing or failing by accident of the machine it runs on.

## [0.1.0] - 2026-07-20

### Added

- A Go library and command-line program for solving exercises from a local TAOCP content repository.
- Fast mode for single-call solutions and slow mode for population generation, selection, independent truth checking, adversarial falsification, and bounded correction.
- Complete token accounting for input, cached input, cache writes, output, reasoning output, and totals.
- Standard GPT-5.6 list-cost estimates, including cache and long-context pricing rules.
- Atomic JSON and Markdown result storage with complete attempt and evaluation records.
- Blind, order-reversed comparisons of fast and slow solution quality, tokens, and estimated cost.
- Cross-platform release archives, Linux packages, container images, checksums, SBOMs, and signed release metadata.
