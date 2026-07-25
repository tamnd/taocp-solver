package route

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/taocp-solver/api"
)

// Cooldowns after a failure. They are not uniform because the causes are not:
// a credential does not fix itself in thirty seconds, and a weekly quota does
// not clear in five minutes.
const (
	MinCooldown          = time.Minute
	MaxCooldown          = 30 * time.Minute
	QuotaCooldown        = 30 * time.Minute
	UnauthorizedCooldown = 6 * time.Hour
	// HealthTTL is how long a probe result is trusted before a reprobe.
	HealthTTL = 10 * time.Minute
)

// entry is a route plus what the pool has learned about it.
type entry struct {
	route    Route
	health   Health
	coldTill time.Time
	failures int
	retired  bool // gone, for the life of the process
}

// Pool picks a route, notices when it dies, and moves to the next one.
type Pool struct {
	Prober     Prober
	Timeout    time.Duration
	MaxRetries int
	// Logf reports every failover, because a solution assembled from two
	// routes should never be a surprise after the fact.
	Logf func(string, ...any)
	Now  func() time.Time

	mu      sync.Mutex
	entries []*entry
}

// NewPool builds a pool from a registry, skipping disabled routes.
func NewPool(registry Registry) *Pool {
	pool := &Pool{}
	for _, value := range registry.Enabled() {
		pool.entries = append(pool.entries, &entry{route: value})
	}
	return pool
}

func (p *Pool) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pool) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

// Pick returns the best route that is not cold, probing lazily. A route with
// no health record is probed before its first use, and a record older than
// HealthTTL is refreshed.
func (p *Pool) Pick(ctx context.Context) (Route, api.Completer, error) {
	for {
		candidate, err := p.next(ctx)
		if err != nil {
			return Route{}, nil, err
		}
		client, err := candidate.Client(p.Timeout, p.MaxRetries)
		if err != nil {
			p.Fail(candidate.Name, err)
			continue
		}
		return candidate, client, nil
	}
}

func (p *Pool) next(ctx context.Context) (Route, error) {
	for {
		p.mu.Lock()
		now := p.now()
		var chosen *entry
		for _, value := range p.entries {
			if value.retired || now.Before(value.coldTill) {
				continue
			}
			chosen = value
			break
		}
		if chosen == nil {
			err := p.coldError(now)
			p.mu.Unlock()
			return Route{}, err
		}
		fresh := chosen.health.State != "" && now.Sub(chosen.health.CheckedAt) < HealthTTL
		candidate := chosen.route
		p.mu.Unlock()

		if fresh {
			return candidate, nil
		}
		health := p.Prober.Probe(ctx, candidate)
		p.mu.Lock()
		chosen.health = health
		if health.State.Usable() {
			chosen.failures = 0
			p.mu.Unlock()
			return candidate, nil
		}
		p.applyLocked(chosen, Signal{State: health.State, Detail: health.Detail, ResetsAt: health.ResetsAt})
		p.mu.Unlock()
		p.logf("route %s is %s: %s", candidate.Name, health.State, health.Detail)
	}
}

// Fail records that a route failed a real call and takes it out for a while.
func (p *Pool) Fail(name string, err error) {
	if err == nil {
		return
	}
	signal := classifyTransport(err)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, value := range p.entries {
		if value.route.Name != name {
			continue
		}
		value.health = Health{
			Route: name, State: signal.State, Detail: signal.Detail,
			ResetsAt: signal.ResetsAt, Model: value.route.Model, CheckedAt: p.now(),
		}
		p.applyLocked(value, signal)
		return
	}
}

// Succeed clears the failure count after a route completes a call.
func (p *Pool) Succeed(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, value := range p.entries {
		if value.route.Name == name {
			value.failures = 0
			value.health = Health{Route: name, State: StateLive, Detail: "ok",
				Model: value.route.Model, CheckedAt: p.now()}
			return
		}
	}
}

// applyLocked sets the cooldown for a failure. The caller holds the lock.
func (p *Pool) applyLocked(value *entry, signal Signal) {
	now := p.now()
	switch signal.State {
	case StateGone:
		// No amount of waiting brings a deleted model back. The fix is an edit
		// to the route file, which doctor prints.
		value.retired = true
		value.coldTill = now.Add(100 * 365 * 24 * time.Hour)
	case StateQuota:
		// A provider that says nothing gets the default window. A provider
		// that names an instant already past is reporting a stale window, and
		// the honest reading of that is "try again shortly", not "wait half an
		// hour" and not "go straight back in".
		cooldown := QuotaCooldown
		switch {
		case signal.ResetsAt.IsZero():
		case signal.ResetsAt.After(now):
			cooldown = signal.ResetsAt.Sub(now)
		default:
			cooldown = MinCooldown
		}
		value.coldTill = now.Add(max(cooldown, MinCooldown))
	case StateUnauthorized:
		value.coldTill = now.Add(UnauthorizedCooldown)
	default:
		value.failures++
		value.coldTill = now.Add(backoff(value.failures))
	}
}

// backoff doubles from the minimum to the maximum cooldown.
func backoff(failures int) time.Duration {
	delay := MinCooldown
	for range max(0, failures-1) {
		delay *= 2
		if delay >= MaxCooldown {
			return MaxCooldown
		}
	}
	return delay
}

// coldError names the earliest reset, because someone waiting at a terminal
// wants to know how long rather than just that everything is down.
func (p *Pool) coldError(now time.Time) error {
	states := make([]string, 0, len(p.entries))
	for _, value := range p.entries {
		state := value.health.State
		if state == "" {
			state = StateUnknown
		}
		states = append(states, fmt.Sprintf("%s: %s", value.route.Name, state))
	}
	found := strings.Join(states, ", ")
	earliest, name := p.earliestLocked(now)
	if earliest.IsZero() {
		return fmt.Errorf("no routes available (%s)", found)
	}
	return fmt.Errorf("every route is cold (%s); %s is the first to return at %s, in %s",
		found, name, earliest.UTC().Format(time.RFC3339), earliest.Sub(now).Round(time.Second))
}

// EarliestReset is when the first cold route becomes usable again, so an
// unattended runner can sleep instead of exiting.
func (p *Pool) EarliestReset() (time.Time, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.earliestLocked(p.now())
}

func (p *Pool) earliestLocked(now time.Time) (time.Time, string) {
	var earliest time.Time
	name := ""
	for _, value := range p.entries {
		if value.retired {
			continue
		}
		if value.coldTill.Before(now) {
			return now, value.route.Name
		}
		if earliest.IsZero() || value.coldTill.Before(earliest) {
			earliest = value.coldTill
			name = value.route.Name
		}
	}
	return earliest, name
}

// Health returns what the pool currently believes about every route.
func (p *Pool) Health() []Health {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Health, 0, len(p.entries))
	for _, value := range p.entries {
		health := value.health
		if health.Route == "" {
			health = Health{Route: value.route.Name, State: StateUnknown, Model: value.route.Model}
		}
		out = append(out, health)
	}
	return out
}

// Names lists the routes the pool holds, in order.
func (p *Pool) Names() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.entries))
	for _, value := range p.entries {
		out = append(out, value.route.Name)
	}
	return out
}

// ProbeAll checks every route, including disabled ones the caller kept, and
// returns the results in rank order. It is what doctor prints.
func (p *Pool) ProbeAll(ctx context.Context) []Health {
	p.mu.Lock()
	entries := append([]*entry(nil), p.entries...)
	p.mu.Unlock()

	results := make([]Health, len(entries))
	var wait sync.WaitGroup
	for index, value := range entries {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index] = p.Prober.Probe(ctx, value.route)
		}()
	}
	wait.Wait()

	p.mu.Lock()
	for index, value := range entries {
		value.health = results[index]
		if !results[index].State.Usable() {
			p.applyLocked(value, Signal{State: results[index].State,
				Detail: results[index].Detail, ResetsAt: results[index].ResetsAt})
		}
	}
	p.mu.Unlock()
	return results
}

// Table renders health results the way doctor prints them.
func Table(results []Health) string {
	rows := results
	widths := []int{len("route"), len("state"), len("model")}
	for _, row := range rows {
		widths[0] = max(widths[0], len(row.Route))
		widths[1] = max(widths[1], len(string(row.State)))
		widths[2] = max(widths[2], len(row.Model))
	}
	var out strings.Builder
	line := func(route, state, latency, model, detail string) {
		fmt.Fprintf(&out, "%-*s  %-*s  %8s   %-*s  %s\n",
			widths[0], route, widths[1], state, latency, widths[2], model, detail)
	}
	line("route", "state", "latency", "model", "detail")
	for _, row := range rows {
		detail := row.Detail
		if !row.ResetsAt.IsZero() {
			detail = fmt.Sprintf("%s, resets %s", detail, row.ResetsAt.UTC().Format("2006-01-02 15:04 UTC"))
		}
		line(row.Route, string(row.State),
			fmt.Sprintf("%.1fs", row.Latency.Seconds()), row.Model, detail)
	}
	return out.String()
}
