# Changelog

All notable changes to `taocp` are recorded here. The project follows Semantic Versioning.

## [Unreleased]

## [0.1.0] - 2026-07-20

### Added

- A Go library and command-line program for solving exercises from a local TAOCP content repository.
- Fast mode for single-call solutions and slow mode for population generation, selection, independent truth checking, adversarial falsification, and bounded correction.
- Complete token accounting for input, cached input, cache writes, output, reasoning output, and totals.
- Standard GPT-5.6 list-cost estimates, including cache and long-context pricing rules.
- Atomic JSON and Markdown result storage with complete attempt and evaluation records.
- Blind, order-reversed comparisons of fast and slow solution quality, tokens, and estimated cost.
- Cross-platform release archives, Linux packages, container images, checksums, SBOMs, and signed release metadata.
