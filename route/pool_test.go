package route

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a hand-wound clock, because cooldown arithmetic measured against
// the wall clock is a flaky test waiting to happen.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// liveRoute is a route backed by a server that answers every probe.
func liveRoute(t *testing.T, name string, rank int) Route {
	t.Helper()
	url := probeServer(t, []string{"m"}, http.StatusOK, "ok")
	return Route{Name: name, Wire: WireChat, BaseURL: url, Model: "m", Rank: rank}
}

func testPool(t *testing.T, routes ...Route) (*Pool, *clock) {
	t.Helper()
	tick := newClock()
	pool := NewPool(Registry{Routes: routes})
	pool.Now = tick.Now
	pool.Prober = Prober{Now: tick.Now, Timeout: 5 * time.Second}
	return pool, tick
}

func TestPickReturnsTheBestLiveRoute(t *testing.T) {
	t.Parallel()
	pool, _ := testPool(t, liveRoute(t, "second", 20), liveRoute(t, "first", 10))

	chosen, client, err := pool.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if chosen.Name != "first" {
		t.Errorf("picked %s, want the lowest rank", chosen.Name)
	}
	if client == nil {
		t.Error("no client returned")
	}
}

func TestPickSkipsARouteThatIsOutOfQuota(t *testing.T) {
	t.Parallel()
	dead := Route{Name: "zen-paid", Wire: WireChat, Rank: 10, Model: "gpt-5.6-sol",
		BaseURL: probeServer(t, []string{"gpt-5.6-sol"}, http.StatusBadRequest, zenCreditsBody)}
	pool, _ := testPool(t, dead, liveRoute(t, "zen-free-nemotron", 30))

	chosen, _, err := pool.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if chosen.Name != "zen-free-nemotron" {
		t.Errorf("picked %s, want the route that answers", chosen.Name)
	}
}

func TestQuotaCooldownUsesTheProvidersResetInstant(t *testing.T) {
	t.Parallel()
	pool, tick := testPool(t, liveRoute(t, "a", 10), liveRoute(t, "b", 20))
	resets := tick.Now().Add(3 * time.Hour)
	pool.Fail("a", fmt.Errorf("429: usage_limit_reached, resets_at %d", resets.Unix()))

	chosen, _, err := pool.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if chosen.Name != "b" {
		t.Fatalf("picked %s while a is cold", chosen.Name)
	}
	// Still cold five minutes later, live again after the window.
	tick.advance(5 * time.Minute)
	if chosen, _, _ := pool.Pick(context.Background()); chosen.Name != "b" {
		t.Errorf("picked %s five minutes in; the window is three hours", chosen.Name)
	}
	tick.advance(3 * time.Hour)
	if chosen, _, _ := pool.Pick(context.Background()); chosen.Name != "a" {
		t.Errorf("picked %s after the window closed, want a back", chosen.Name)
	}
}

// A provider reporting a reset instant already in the past is reporting a
// stale window. Trusting it would mean no cooldown at all.
func TestAResetInThePastCollapsesToTheMinimumCooldown(t *testing.T) {
	t.Parallel()
	pool, tick := testPool(t, liveRoute(t, "a", 10), liveRoute(t, "b", 20))
	stale := tick.Now().Add(-time.Hour)
	pool.Fail("a", fmt.Errorf("429: usage_limit_reached, resets_at %d", stale.Unix()))

	if chosen, _, _ := pool.Pick(context.Background()); chosen.Name != "b" {
		t.Fatalf("picked %s immediately after a quota failure", chosen.Name)
	}
	tick.advance(MinCooldown + time.Second)
	if chosen, _, _ := pool.Pick(context.Background()); chosen.Name != "a" {
		t.Errorf("picked %s after the minimum cooldown", chosen.Name)
	}
}

func TestUnauthorizedIsColdForHours(t *testing.T) {
	t.Parallel()
	pool, tick := testPool(t, liveRoute(t, "a", 10), liveRoute(t, "b", 20))
	pool.Fail("a", errors.New("chat completions API returned 401 Unauthorized"))

	tick.advance(time.Hour)
	if chosen, _, _ := pool.Pick(context.Background()); chosen.Name != "b" {
		t.Errorf("picked %s an hour after a bad credential; that does not fix itself", chosen.Name)
	}
	tick.advance(UnauthorizedCooldown)
	if chosen, _, _ := pool.Pick(context.Background()); chosen.Name != "a" {
		t.Errorf("picked %s after the credential cooldown", chosen.Name)
	}
}

func TestBrokenRoutesBackOffExponentially(t *testing.T) {
	t.Parallel()
	pool, tick := testPool(t, liveRoute(t, "a", 10), liveRoute(t, "b", 20))
	for attempt := range 3 {
		pool.Fail("a", errors.New("decode chat stream: invalid character"))
		delay := backoff(attempt + 1)
		tick.advance(delay - time.Second)
		if chosen, _, _ := pool.Pick(context.Background()); chosen.Name != "a" {
			// Still cold, which is what we want.
			tick.advance(2 * time.Second)
			continue
		}
		t.Fatalf("attempt %d: a came back before its %s cooldown expired", attempt+1, delay)
	}
	if got := backoff(20); got != MaxCooldown {
		t.Errorf("backoff caps at %s, want %s", got, MaxCooldown)
	}
}

func TestGoneRetiresARouteForTheLifeOfTheProcess(t *testing.T) {
	t.Parallel()
	pool, tick := testPool(t, liveRoute(t, "a", 10), liveRoute(t, "b", 20))
	pool.Fail("a", errors.New("ModelError: Model hy3-free is not supported"))

	// No amount of waiting brings a deleted model back.
	tick.advance(30 * 24 * time.Hour)
	chosen, _, err := pool.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if chosen.Name != "b" {
		t.Fatalf("picked %s a month later; a is gone, not cold", chosen.Name)
	}
	if _, name := pool.EarliestReset(); name == "a" {
		t.Error("a retired route was offered as something to wait for")
	}
}

func TestEveryRouteColdNamesTheEarliestReset(t *testing.T) {
	t.Parallel()
	pool, tick := testPool(t, liveRoute(t, "a", 10), liveRoute(t, "b", 20))
	pool.Fail("a", fmt.Errorf("429: usage_limit_reached, resets_at %d", tick.Now().Add(6*time.Hour).Unix()))
	pool.Fail("b", fmt.Errorf("429: usage_limit_reached, resets_at %d", tick.Now().Add(2*time.Hour).Unix()))

	_, _, err := pool.Pick(context.Background())
	if err == nil {
		t.Fatal("expected an error when every route is cold")
	}
	// A person waiting at a terminal wants the number, not just the bad news.
	if !strings.Contains(err.Error(), "b is the first to return") {
		t.Errorf("error = %q, want it to name the earliest route", err)
	}
	if !strings.Contains(err.Error(), "2h0m0s") {
		t.Errorf("error = %q, want it to say how long", err)
	}
	when, name := pool.EarliestReset()
	if name != "b" || !when.Equal(tick.Now().Add(2*time.Hour)) {
		t.Errorf("earliest = %s at %s", name, when)
	}
}

func TestHealthIsReprobedAfterItGoesStale(t *testing.T) {
	t.Parallel()
	var calls int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	pool, tick := testPool(t, Route{Name: "a", Wire: WireChat, BaseURL: server.URL + "/v1", Model: "m", Rank: 10})

	for range 3 {
		if _, _, err := pool.Pick(context.Background()); err != nil {
			t.Fatalf("Pick: %v", err)
		}
	}
	mu.Lock()
	after := calls
	mu.Unlock()
	if after != 1 {
		t.Errorf("probed %d times inside the health window, want 1", after)
	}

	tick.advance(HealthTTL + time.Minute)
	if _, _, err := pool.Pick(context.Background()); err != nil {
		t.Fatalf("Pick: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("probed %d times after the record went stale, want 2", calls)
	}
}

// Failover happens per model call, not per exercise. A run that starts on one
// route and finishes on another has to record both.
func TestFailoverMidRunReportsBothRoutes(t *testing.T) {
	t.Parallel()
	pool, _ := testPool(t, liveRoute(t, "codex", 10), liveRoute(t, "zen-free-nemotron", 30))

	var used []string
	for call := range 4 {
		chosen, _, err := pool.Pick(context.Background())
		if err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		used = append(used, chosen.Name)
		if call == 1 {
			pool.Fail(chosen.Name, errors.New("429: usage_limit_reached"))
			continue
		}
		pool.Succeed(chosen.Name)
	}
	if used[0] != "codex" || used[1] != "codex" {
		t.Errorf("calls started on %v, want codex first", used[:2])
	}
	if used[2] != "zen-free-nemotron" || used[3] != "zen-free-nemotron" {
		t.Errorf("calls continued on %v, want the fallback after the quota wall", used[2:])
	}
}

func TestFailoverIsLogged(t *testing.T) {
	t.Parallel()
	dead := Route{Name: "zen-paid", Wire: WireChat, Rank: 10, Model: "gpt-5.6-sol",
		BaseURL: probeServer(t, []string{"gpt-5.6-sol"}, http.StatusBadRequest, zenCreditsBody)}
	pool, _ := testPool(t, dead, liveRoute(t, "fallback", 30))
	var logged []string
	pool.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	if _, _, err := pool.Pick(context.Background()); err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if len(logged) == 0 {
		t.Fatal("a failover happened silently")
	}
	if !strings.Contains(logged[0], "zen-paid") || !strings.Contains(logged[0], "quota") {
		t.Errorf("log = %q, want the route and why", logged[0])
	}
}

func TestProbeAllReturnsEveryRoute(t *testing.T) {
	t.Parallel()
	pool, _ := testPool(t, liveRoute(t, "a", 10),
		Route{Name: "b", Wire: WireChat, Rank: 20, Model: "m",
			BaseURL: probeServer(t, []string{"m"}, http.StatusBadRequest, zenCreditsBody)})

	results := pool.ProbeAll(context.Background())
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Route != "a" || results[0].State != StateLive {
		t.Errorf("first = %+v", results[0])
	}
	if results[1].Route != "b" || results[1].State != StateQuota {
		t.Errorf("second = %+v", results[1])
	}
	// A probe that found a dead route must leave it cold, not just report it.
	if chosen, _, _ := pool.Pick(context.Background()); chosen.Name != "a" {
		t.Errorf("picked %s after the probe found b out of quota", chosen.Name)
	}
}

func TestTableRendersEveryRow(t *testing.T) {
	t.Parallel()
	rendered := Table([]Health{
		{Route: "codex", State: StateQuota, Latency: 400 * time.Millisecond, Model: "gpt-5.6-luna",
			Detail: "usage limit", ResetsAt: time.Date(2026, 7, 29, 8, 34, 0, 0, time.UTC)},
		{Route: "zen-free-nemotron", State: StateLive, Latency: 3 * time.Second,
			Model: "nemotron-3-ultra-free", Detail: "ok"},
	})
	for _, want := range []string{"route", "codex", "quota", "0.4s", "resets 2026-07-29 08:34 UTC", "zen-free-nemotron"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("table is missing %q:\n%s", want, rendered)
		}
	}
}
