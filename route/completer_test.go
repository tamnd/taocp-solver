package route

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/result"
)

func TestCompleterRecordsTheRouteThatAnswered(t *testing.T) {
	t.Parallel()
	pool, _ := testPool(t, liveRoute(t, "zen-free-nemotron", 30))
	completer := NewCompleter(pool)

	response, err := completer.Complete(context.Background(), api.Request{Input: "add them up"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Route != "zen-free-nemotron" {
		t.Errorf("route = %q", response.Route)
	}
	if response.Model != "m" {
		t.Errorf("model = %q, want the route's model", response.Model)
	}
}

func TestCompleterMovesToTheNextRouteOnFailure(t *testing.T) {
	t.Parallel()
	dead := Route{Name: "zen-paid", Wire: WireChat, Rank: 10, Model: "gpt-5.6-sol",
		BaseURL: probeServer(t, []string{"gpt-5.6-sol"}, http.StatusBadRequest, zenCreditsBody)}
	pool, _ := testPool(t, dead, liveRoute(t, "zen-free-nemotron", 30))
	completer := NewCompleter(pool)

	response, err := completer.Complete(context.Background(), api.Request{Input: "go"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Route != "zen-free-nemotron" {
		t.Errorf("route = %q, want the fallback", response.Route)
	}
}

// The route names its own model. Carrying the dead route's slug onto the live
// one turns a quota failure into a model rejection on the way out.
func TestCompleterSendsEachRouteItsOwnModel(t *testing.T) {
	t.Parallel()
	pool, _ := testPool(t, liveRoute(t, "a", 10))
	completer := NewCompleter(pool)

	response, err := completer.Complete(context.Background(),
		api.Request{Model: "gpt-5.6-sol", Input: "go"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Model == "gpt-5.6-sol" {
		t.Error("the caller's model reached a route that does not serve it")
	}
}

func TestCompleterReportsEveryRouteItTried(t *testing.T) {
	t.Parallel()
	first := Route{Name: "zen-paid", Wire: WireChat, Rank: 10, Model: "m",
		BaseURL: probeServer(t, []string{"m"}, http.StatusBadRequest, zenCreditsBody)}
	second := Route{Name: "backup", Wire: WireChat, Rank: 20, Model: "m",
		BaseURL: probeServer(t, []string{"m"}, http.StatusServiceUnavailable, proxyDownBody)}
	pool, _ := testPool(t, first, second)

	_, err := NewCompleter(pool).Complete(context.Background(), api.Request{Input: "go"})
	if err == nil {
		t.Fatal("expected an error when every route is down")
	}
	// The message has to name what was tried. "request failed" after silently
	// burning two routes is not a report.
	for _, want := range []string{"zen-paid", "backup"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %s", err, want)
		}
	}
}

// A mixed-route solution reports both routes and prices each with its own card.
func TestMixedRouteMetricsBreakDownByRoute(t *testing.T) {
	t.Parallel()
	attempts := []result.Attempt{
		{Phase: "reference", Model: "gpt-5.6-luna", Route: "codex", CurrentRun: true,
			Usage: api.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}},
		{Phase: "generate", Model: "nemotron-3-ultra-free", Route: "zen-free-nemotron", CurrentRun: true,
			Usage: api.Usage{InputTokens: 200, OutputTokens: 80, TotalTokens: 280}},
	}
	metrics := result.BuildMetrics(attempts)

	if len(metrics.ByRoute) != 2 {
		t.Fatalf("by_route has %d entries, want 2", len(metrics.ByRoute))
	}
	if got := metrics.ByRoute["codex"].Tokens.TotalTokens; got != 150 {
		t.Errorf("codex tokens = %d, want 150", got)
	}
	if got := metrics.ByRoute["zen-free-nemotron"].Tokens.TotalTokens; got != 280 {
		t.Errorf("nemotron tokens = %d, want 280", got)
	}
	if got := metrics.CurrentRun.Tokens.TotalTokens; got != 430 {
		t.Errorf("total = %d, want the sum of both routes", got)
	}
}

// One route means no breakdown, because a per-route table with one row is
// noise rather than information.
func TestSingleRouteMetricsHaveNoBreakdown(t *testing.T) {
	t.Parallel()
	metrics := result.BuildMetrics([]result.Attempt{
		{Model: "m", Route: "codex", CurrentRun: true, Usage: api.Usage{InputTokens: 10, OutputTokens: 5}},
		{Model: "m", Route: "codex", CurrentRun: true, Usage: api.Usage{InputTokens: 20, OutputTokens: 5}},
	})
	if metrics.ByRoute != nil {
		t.Errorf("by_route = %v, want nothing for a single-route run", metrics.ByRoute)
	}
}

func TestCompleterWithoutAPoolIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := (&Completer{}).Complete(context.Background(), api.Request{Input: "go"}); err == nil {
		t.Fatal("expected an error")
	}
}
