# taocp

`taocp` solves and reviews exercises from The Art of Computer Programming. It is both a Go library and a command-line program.

The solver loads an exercise, relevant section text, and nearby exercises from a local TAOCP content repository. It sends a carefully structured prompt through an OpenAI-compatible bridge or proxy, then applies two independent verification prompts. A result is verified only when both judges pass it. Failed results enter a bounded correction loop and are checked again from scratch.

## Quality model

The workflow uses several complementary checks:

1. The solution prompt scales its requested structure and rigor with Knuth's difficulty rating.
2. The correctness judge derives a reference approach, creates a problem-specific marking scheme, and grades the result from 0 to 7.
3. The process judge audits the argument in order, identifies the earliest material error, independently recomputes the conclusion, and checks every obligation in the exercise.
4. Both judges must pass the same candidate. A disagreement is a failure and triggers correction when correction passes remain.
5. Publication guards reject empty responses and provider leakage, normalize typography, and remove horizontal rules.
6. The comparison command evaluates a new result against the matching solution in `tamnd/brain`. It runs the pairwise judge twice with reversed answer order. A winner is reported only when both orders agree.

This design follows current math evaluation findings: proof grading benefits from rich source context, problem-specific marking schemes, fine-grained scores, and evaluator ensembles. Process evaluation also benefits from locating the earliest incorrect step instead of checking only the final answer.

See [the live evaluation](docs/EVALUATION.md) for a verified solve and an order-reversed comparison with the matching `tamnd/brain` entry.

## Install

```sh
go install github.com/tamnd/taocp-solver/cmd/taocp@latest
```

Building from source requires the Go version declared in `go.mod`:

```sh
git clone https://github.com/tamnd/taocp-solver.git
cd taocp-solver
make test build
```

## Bridge and proxy

The default transport is streaming Chat Completions because the local subscription bridge accepts that wire format and translates it to the upstream Responses protocol. A tracing proxy can sit between `taocp` and the bridge without changing the solver:

```text
taocp -> trace proxy -> local bridge -> model backend
```

Point `--base-url` at the proxy when tracing, or at the bridge when calling it directly. The URL may end at the server root or `/v1`.

```sh
export TAOCP_SOLVER_BASE_URL=http://localhost:8790/v1
export TAOCP_SOLVER_MODEL=gpt-5.6-sol

taocp solve 1.1 1
```

An API key is optional for a trusted local bridge and required when the selected endpoint requires one:

```sh
export TAOCP_SOLVER_API_KEY=your-key
```

## Command line

Solve one exercise and run both judges:

```sh
taocp solve 1.2.6 10
taocp solve 1.2.6.10
```

Re-solve an exercise even when a cached result exists:

```sh
taocp solve 1.2.6 10 --force
```

Solve every exercise in selected sections:

```sh
taocp batch 1.1 1.2.1 --parallel 2
```

Inspect the exact solution prompt without making a model call:

```sh
taocp prompt 1.1 1
```

Run both judges on an existing Markdown solution:

```sh
taocp review 1.1 1 --file solution.md
```

Compare a generated result with the matching solution in `tamnd/brain`:

```sh
taocp compare 1.1 1
taocp compare 1.1 1 --candidate ./my-solution.md --json
```

Flags can appear before or after positional arguments. Every model-calling command accepts `--base-url`, `--api-key`, `--model`, `--source`, `--output`, `--timeout`, and `--retries`.

## Library

The root package is named `taocp`. Public subpackages expose the transport, configuration, exercise loader, prompts, result store, solver engine, text guards, and comparison evaluator.

```go
package main

import (
	"context"
	"log"

	taocp "github.com/tamnd/taocp-solver"
	"github.com/tamnd/taocp-solver/solver"
)

func main() {
	cfg := taocp.DefaultConfig("http://localhost:8790/v1", "")
	client, err := taocp.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	answer, err := client.SolveReference(context.Background(), "1.1.1", solver.Options{
		Verify:         true,
		MaxCorrections: 2,
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("verdict: %s", answer.Verdict)
}
```

Use `taocp.WithCompleter` to supply a custom model transport, `taocp.WithRepository` for a custom content source, and `taocp.WithStore` for a custom output location.

## Configuration

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `TAOCP_SOLVER_BASE_URL` | none | Bridge or proxy base URL |
| `TAOCP_SOLVER_API_KEY` | `OPENAI_API_KEY` | Endpoint credential |
| `TAOCP_SOLVER_MODEL` | `gpt-5.6` | Model name |
| `TAOCP_SOLVER_SOURCE` | `~/github/tamnd/taocp` | TAOCP content repository |
| `TAOCP_SOLVER_OUTPUT` | `~/data/taocp-solver` | JSON and Markdown results |
| `TAOCP_SOLVER_TIMEOUT` | `30m` | Timeout for each model call |
| `TAOCP_SOLVER_MAX_CORRECTIONS` | `2` | Correction passes |
| `TAOCP_SOLVER_MAX_RETRIES` | `4` | Transient transport retries |
| `TAOCP_SOLVER_PARALLEL` | `2` or available CPUs | Batch workers |

The output store writes `{section}/{number}.json` and `{section}/{number}.md` atomically. JSON keeps the final solution, both latest judge reports, verdict, model response identifiers, token counts, timestamps, and the complete attempt sequence.

## Development

```sh
make fmt
make vet
make test
make lint
```

Tests use local HTTP servers and scripted model responses. They do not require credentials or network access.

## Evaluation references

- [ProofGrader and ProofBench](https://arxiv.org/abs/2510.13888) study rich reference context, problem-specific marking schemes, fine-grained proof scoring, and evaluator ensembles.
- [ProcessBench](https://arxiv.org/abs/2412.06559) evaluates critics by whether they find the earliest incorrect reasoning step.
- [Math-Verify](https://github.com/huggingface/Math-Verify) separates answer extraction, normalization, and mathematical equivalence checks.
- [PRM800K](https://github.com/openai/prm800k) provides step-level correctness supervision and conservative symbolic answer grading.

## License

MIT
