package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/taocp-solver/pricing"
	"github.com/tamnd/taocp-solver/result"
	"github.com/tamnd/taocp-solver/route"
)

func TestParseTarget(t *testing.T) {
	t.Parallel()
	section, number, err := parseTarget([]string{"1.2.6", "10"})
	if err != nil || section != "1.2.6" || number != 10 {
		t.Fatalf("got %q, %d, %v", section, number, err)
	}
	section, number, err = parseTarget([]string{"1.2.6.10"})
	if err != nil || section != "1.2.6" || number != 10 {
		t.Fatalf("got %q, %d, %v", section, number, err)
	}
}

func TestPrintMetrics(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	metrics := result.MetricSet{
		Tokens:   result.TokenMetrics{Requests: 2, InputTokens: 100, UncachedInputTokens: 70, CachedInputTokens: 20, CacheWriteTokens: 10, OutputTokens: 30, ReasoningTokens: 12, TotalTokens: 130},
		ListCost: pricing.Cost{Available: true, TotalUSD: 0.001234}, PricedRequests: 2,
	}
	if err := printMetrics(&output, metrics); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "cached 20, cache write 10") || !strings.Contains(got, "list cost estimate: $0.001234 USD") {
		t.Fatalf("output = %q", got)
	}
}

func TestHelp(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "taocp solve") || !strings.Contains(output.String(), "taocp review") || !strings.Contains(output.String(), "taocp benchmark") || !strings.Contains(output.String(), "taocp matrix") {
		t.Fatalf("help = %q", output.String())
	}
}

// routeServer is an OpenAI-compatible stub: a catalogue and one streamed word.
func routeServer(t *testing.T, model string, status int, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"data":[{"id":%q}]}`, model)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", body)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

func writeRouteFile(t *testing.T, routes ...route.Route) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routes.json")
	if err := (route.Registry{Routes: routes}).Write(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDoctorReportsALiveRoute(t *testing.T) {
	t.Parallel()
	path := writeRouteFile(t, route.Route{
		Name: "stub", Wire: route.WireChat, Rank: 10, Model: "m",
		BaseURL: routeServer(t, "m", http.StatusOK, "ok"),
	})
	var output, errors bytes.Buffer
	if err := run(context.Background(), []string{"doctor", "--routes", path}, &output, &errors); err != nil {
		t.Fatalf("doctor: %v (%s)", err, errors.String())
	}
	if got := output.String(); !strings.Contains(got, "stub") || !strings.Contains(got, "live") {
		t.Fatalf("output = %q", got)
	}
}

// A doctor that exits 0 with nothing live would be useless as a cron guard, so
// the failing case has to come back as an error.
func TestDoctorFailsWhenNoRouteIsLive(t *testing.T) {
	t.Parallel()
	path := writeRouteFile(t, route.Route{
		Name: "broke", Wire: route.WireChat, Rank: 10, Model: "m",
		BaseURL: routeServer(t, "m", http.StatusTooManyRequests,
			`{"error":{"message":"usage_limit_reached","resets_at":1785326082}}`),
	})
	var output, errors bytes.Buffer
	err := run(context.Background(), []string{"doctor", "--routes", path}, &output, &errors)
	if err == nil {
		t.Fatal("expected an error when nothing is live")
	}
	if !strings.Contains(output.String(), "quota") {
		t.Errorf("output = %q, want the quota state", output.String())
	}
}

func TestDoctorJSONNamesTheSource(t *testing.T) {
	t.Parallel()
	path := writeRouteFile(t, route.Route{
		Name: "stub", Wire: route.WireChat, Rank: 10, Model: "m",
		BaseURL: routeServer(t, "m", http.StatusOK, "ok"),
	})
	var output, errors bytes.Buffer
	if err := run(context.Background(), []string{"doctor", "--routes", path, "--json"}, &output, &errors); err != nil {
		t.Fatalf("doctor: %v (%s)", err, errors.String())
	}
	var report struct {
		Source string `json:"source"`
		Routes []struct {
			Route string `json:"route"`
			State string `json:"state"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v (%s)", err, output.String())
	}
	if report.Source != path {
		t.Errorf("source = %q, want %q", report.Source, path)
	}
	if len(report.Routes) != 1 || report.Routes[0].State != "live" {
		t.Errorf("routes = %+v", report.Routes)
	}
}

// Writing the route file must not need a working network, because the first
// thing someone does with an empty config is dump the built-ins and edit them.
func TestDoctorWritesTheEffectiveRouteFile(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "nested", "routes.json")
	var output, errors bytes.Buffer
	if err := run(context.Background(), []string{"doctor", "--write-routes", target}, &output, &errors); err != nil {
		t.Fatalf("doctor: %v (%s)", err, errors.String())
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"name": "codex"`) {
		t.Errorf("route file = %s", raw)
	}
}

func TestDoctorRejectsAnUnknownRoute(t *testing.T) {
	t.Parallel()
	path := writeRouteFile(t, route.Route{
		Name: "stub", Wire: route.WireChat, Rank: 10, Model: "m", BaseURL: "http://127.0.0.1:1/v1",
	})
	var output, errors bytes.Buffer
	err := run(context.Background(), []string{"doctor", "--routes", path, "--route", "nope"}, &output, &errors)
	if err == nil || !strings.Contains(err.Error(), "unknown route") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseMode(t *testing.T) {
	t.Parallel()
	if mode, err := parseMode("FAST"); err != nil || mode != "fast" {
		t.Fatalf("mode = %q, err = %v", mode, err)
	}
	if _, err := parseMode("medium"); err == nil {
		t.Fatal("expected invalid mode")
	}
}
