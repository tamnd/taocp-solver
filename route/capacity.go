package route

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// How much of a run to fan out is a question about the endpoint, not about this
// machine. The old default guessed from the local core count, which describes a
// host that only marshals JSON and waits. An endpoint sitting in front of a
// limited resource knows the real answer and some of them publish it, so ask
// rather than guess: too low leaves it idle, and too high piles on work it
// queues anyway, which only hides where the time went.
//
// The figure is read from /v1/health as pool.concurrency. Nothing else in the
// OpenAI-compatible surface carries it, and most servers have no such notion,
// so an endpoint that does not answer is not an error.

// Concurrency asks one endpoint how many requests it will run at once. Zero
// means it published nothing, which a caller must treat as unknown rather than
// as none.
func (p Prober) Concurrency(ctx context.Context, value Route) int {
	// The Codex wire has no base URL to ask and no such endpoint behind it.
	if value.Wire == WireCodex || strings.TrimSpace(value.BaseURL) == "" {
		return 0
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, value.endpoint("/health"), nil)
	if err != nil {
		return 0
	}
	if key := value.Key(); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := p.httpClient().Do(request)
	if err != nil {
		return 0
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0
	}
	// A health body is a few hundred bytes. The limit is here because this
	// talks to whatever the route file names, and a server answering with a
	// gigabyte of HTML should cost a read that fails rather than the memory.
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0
	}
	var envelope struct {
		Pool struct {
			Concurrency int `json:"concurrency"`
		} `json:"pool"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return 0
	}
	if envelope.Pool.Concurrency < 0 {
		return 0
	}
	return envelope.Pool.Concurrency
}

// Advertised is what the run should fan out to, and the route that said so. It
// returns zero when no route published a figure, leaving the caller on its own
// default.
//
// It takes the first answer in rank order rather than adding them up, because
// the pool sends everything to the highest ranked route that is answering. That
// is the endpoint the whole run leans on, and sizing for the fallbacks too
// would mean sizing for capacity the run only reaches once the top route has
// already fallen over.
func Advertised(ctx context.Context, routes []Route, timeout time.Duration) (int, string) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	prober := Prober{Timeout: timeout, HTTPClient: &http.Client{Timeout: timeout}}
	for _, value := range routes {
		if value.Disabled {
			continue
		}
		if size := prober.Concurrency(ctx, value); size > 0 {
			return size, value.Name
		}
	}
	return 0, ""
}
