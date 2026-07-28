package route

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func healthServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAnEndpointThatPublishesItsCapacityIsBelieved(t *testing.T) {
	t.Parallel()
	server := healthServer(t, `{"status":"ok","pool":{"verified":10,"concurrency":4}}`, http.StatusOK)
	value := Route{Name: "bridge", Wire: WireChat, BaseURL: server.URL, Model: "m"}
	if got := (Prober{}).Concurrency(context.Background(), value); got != 4 {
		t.Fatalf("concurrency = %d, want 4", got)
	}
}

func TestAnEndpointThatPublishesNothingIsUnknownRatherThanZeroCapacity(t *testing.T) {
	t.Parallel()
	for name, server := range map[string]*httptest.Server{
		"no health endpoint": healthServer(t, "not found", http.StatusNotFound),
		"no pool field":      healthServer(t, `{"status":"ok"}`, http.StatusOK),
		"not json":           healthServer(t, `<html>ok</html>`, http.StatusOK),
	} {
		value := Route{Name: "bridge", Wire: WireChat, BaseURL: server.URL, Model: "m"}
		if got := (Prober{}).Concurrency(context.Background(), value); got != 0 {
			t.Fatalf("%s: concurrency = %d, want 0", name, got)
		}
	}
}

func TestTheCodexWireIsNotAskedForCapacity(t *testing.T) {
	t.Parallel()
	// It has no base URL to ask, so a probe would be a request to nowhere.
	value := Route{Name: "codex", Wire: WireCodex, Model: "m"}
	if got := (Prober{}).Concurrency(context.Background(), value); got != 0 {
		t.Fatalf("concurrency = %d, want 0", got)
	}
}

func TestTheFirstRouteToAnswerSetsTheFanOut(t *testing.T) {
	t.Parallel()
	quiet := healthServer(t, "not found", http.StatusNotFound)
	first := healthServer(t, `{"pool":{"concurrency":3}}`, http.StatusOK)
	second := healthServer(t, `{"pool":{"concurrency":9}}`, http.StatusOK)

	// Rank order, and the pool sends everything to the first route that is
	// answering. Sizing for the fallback would size for capacity this run only
	// reaches once the route above it has already fallen over.
	routes := []Route{
		{Name: "codex", Wire: WireCodex, Model: "m"},
		{Name: "quiet", Wire: WireChat, BaseURL: quiet.URL, Model: "m"},
		{Name: "first", Wire: WireChat, BaseURL: first.URL, Model: "m"},
		{Name: "second", Wire: WireChat, BaseURL: second.URL, Model: "m"},
	}
	size, name := Advertised(context.Background(), routes, 5*time.Second)
	if size != 3 || name != "first" {
		t.Fatalf("advertised = %d from %q, want 3 from \"first\"", size, name)
	}
}

func TestADisabledRouteDoesNotSetTheFanOut(t *testing.T) {
	t.Parallel()
	off := healthServer(t, `{"pool":{"concurrency":9}}`, http.StatusOK)
	on := healthServer(t, `{"pool":{"concurrency":2}}`, http.StatusOK)
	routes := []Route{
		{Name: "off", Wire: WireChat, BaseURL: off.URL, Model: "m", Disabled: true},
		{Name: "on", Wire: WireChat, BaseURL: on.URL, Model: "m"},
	}
	if size, name := Advertised(context.Background(), routes, 5*time.Second); size != 2 || name != "on" {
		t.Fatalf("advertised = %d from %q, want 2 from \"on\"", size, name)
	}
}

func TestNoRouteAnsweringLeavesTheFanOutToTheCaller(t *testing.T) {
	t.Parallel()
	quiet := healthServer(t, "not found", http.StatusNotFound)
	routes := []Route{{Name: "quiet", Wire: WireChat, BaseURL: quiet.URL, Model: "m"}}
	if size, name := Advertised(context.Background(), routes, 5*time.Second); size != 0 || name != "" {
		t.Fatalf("advertised = %d from %q, want 0 and no name", size, name)
	}
}
