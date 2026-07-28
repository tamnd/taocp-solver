package route

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tamnd/taocp-solver/api"
)

// Completer sends each call to the best route that is answering, and moves to
// the next one when a route fails.
//
// Failover is per call rather than per exercise. A run whose reference lands on
// one route and whose candidates land on another is normal, and every attempt
// records which route served it.
type Completer struct {
	Pool *Pool
	// Attempts caps how many routes one call may try before giving up. It is
	// not a retry count: transient retries against a single route stay with
	// the transport, where the delays the provider advertises are known.
	Attempts int
}

// NewCompleter wraps a pool.
func NewCompleter(pool *Pool) *Completer {
	return &Completer{Pool: pool, Attempts: 3}
}

func (c *Completer) Complete(ctx context.Context, request api.Request) (api.Response, error) {
	if c.Pool == nil {
		return api.Response{}, errors.New("route pool is nil")
	}
	attempts := c.Attempts
	if attempts < 1 {
		attempts = 1
	}
	var failures []string
	for range attempts {
		chosen, client, err := c.Pool.Pick(ctx)
		if err != nil {
			if len(failures) > 0 {
				return api.Response{}, fmt.Errorf("%w; tried %s", err, strings.Join(failures, ", "))
			}
			return api.Response{}, err
		}
		// The route names its own model. Carrying the previous route's slug
		// onto the next one is how a failover turns into a model rejection.
		call := request
		call.Model = chosen.Model
		if call.Effort == "" {
			call.Effort = chosen.Effort
		}
		response, err := client.Complete(ctx, call)
		if err != nil {
			if ctx.Err() != nil {
				return api.Response{}, err
			}
			c.Pool.Fail(chosen.Name, err)
			failures = append(failures, fmt.Sprintf("%s (%s)", chosen.Name, oneLine(err)))
			// No log line here: Fail already reported the route and the cause,
			// and two lines per failover said the same thing twice.
			continue
		}
		c.Pool.Succeed(chosen.Name)
		response.Route = chosen.Name
		response.PricingModel = chosen.PricingModel()
		if response.Model == "" {
			response.Model = chosen.Model
		}
		return response, nil
	}
	return api.Response{}, fmt.Errorf("every attempt failed: %s", strings.Join(failures, "; "))
}

// oneLine flattens an error for a log line.
func oneLine(err error) string {
	text := strings.Join(strings.Fields(err.Error()), " ")
	if len(text) > 160 {
		text = text[:160] + "..."
	}
	return text
}
