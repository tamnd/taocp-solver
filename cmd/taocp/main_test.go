package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/tamnd/taocp-solver/config"
	"github.com/tamnd/taocp-solver/coverage"
	"github.com/tamnd/taocp-solver/exercise"
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
	// Not parallel, and pointed at a path that does not exist: without this the
	// test reads whatever personal route file the developer happens to have, and
	// passes or fails by accident of the machine it runs on.
	t.Setenv("TAOCP_ROUTES", filepath.Join(t.TempDir(), "absent.json"))
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

// An explicit --base-url alongside a route file is an instruction to try that
// endpoint first, not something to ignore.
func TestExplicitBaseURLBecomesTheFirstRoute(t *testing.T) {
	t.Parallel()
	path := writeRouteFile(t, route.Route{
		Name: "configured", Wire: route.WireChat, Rank: 10, Model: "m", BaseURL: "http://127.0.0.1:1/v1",
	})
	fs := pflag.NewFlagSet("solve", pflag.ContinueOnError)
	common := bindCommon(fs, config.FromEnv())
	if err := fs.Parse([]string{"--routes", path, "--base-url", "http://127.0.0.1:2/v1", "--api-key", "sk-local"}); err != nil {
		t.Fatal(err)
	}
	if err := common.finish(false); err != nil {
		t.Fatal(err)
	}
	pool, err := common.pool(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := pool.Names(); len(got) != 2 || got[0] != "ad-hoc" {
		t.Fatalf("routes = %v, want the ad hoc route first", got)
	}
}

// The same value out of the environment is not an instruction, so it does not
// jump the queue.
func TestEnvironmentBaseURLDoesNotBecomeARoute(t *testing.T) {
	t.Parallel()
	path := writeRouteFile(t, route.Route{
		Name: "configured", Wire: route.WireChat, Rank: 10, Model: "m", BaseURL: "http://127.0.0.1:1/v1",
	})
	defaults := config.FromEnv()
	defaults.BaseURL = "http://127.0.0.1:2/v1"
	fs := pflag.NewFlagSet("solve", pflag.ContinueOnError)
	common := bindCommon(fs, defaults)
	if err := fs.Parse([]string{"--routes", path}); err != nil {
		t.Fatal(err)
	}
	if err := common.finish(false); err != nil {
		t.Fatal(err)
	}
	pool, err := common.pool(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := pool.Names(); len(got) != 1 || got[0] != "configured" {
		t.Fatalf("routes = %v", got)
	}
}

// publishTree builds a source repository, an empty brain and a result store, so
// the command test drives the same file walk a real run would.
func publishTree(t *testing.T) (brain, source, output string) {
	t.Helper()
	root := t.TempDir()
	source, brain, output = filepath.Join(root, "taocp"), filepath.Join(root, "brain"), filepath.Join(root, "results")
	dir := filepath.Join(source, "content", "vol1", "exercises", "1.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := `---
section: "1.1"
section_title: "Algorithms"
chapter: 1
chapter_title: "Basic Concepts"
volume: 1
book_pages: "1–9"
exercise: 1
rating: "10"
category: "simple"
---
**1.** [*10*] Explain the notation.
`
	if err := os.WriteFile(filepath.Join(dir, "01.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	target, _, err := exercise.NewRepository(source).Load("1.1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := (result.Store{Root: output}).Save(result.Result{
		ID: "1.1.1", Exercise: target, Solution: "The notation names the steps.", Verified: true,
	}); err != nil {
		t.Fatal(err)
	}
	return brain, source, output
}

func TestPublishWritesAndThenHasNothingLeftToDo(t *testing.T) {
	t.Parallel()
	brain, source, output := publishTree(t)
	flags := []string{"publish", "--brain", brain, "--source", source, "--output", output}

	var first bytes.Buffer
	if err := run(context.Background(), append(flags, "--verbose"), &first, &first); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), "publish: 1 solutions written") {
		t.Fatalf("first run = %q", first.String())
	}
	if !strings.Contains(first.String(), filepath.Join("1.1", "01.md")) {
		t.Errorf("verbose must name the paths it wrote: %q", first.String())
	}

	// A second run over an unchanged store is what keeps brain's git history and
	// the published dates intact, so the command has to report it as such.
	var second bytes.Buffer
	if err := run(context.Background(), flags, &second, &second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.String(), "publish: 0 solutions written, 0 deleted by leak gate, 1 unchanged") {
		t.Fatalf("second run = %q", second.String())
	}
}

func TestPublishCheckWritesNothingAndFails(t *testing.T) {
	t.Parallel()
	brain, source, output := publishTree(t)

	var stdout bytes.Buffer
	err := run(context.Background(), []string{"publish", "--check", "--brain", brain, "--source", source, "--output", output}, &stdout, &stdout)
	if err == nil {
		t.Fatal("a check over an out of date brain must exit non-zero")
	}
	if !strings.Contains(stdout.String(), "publish: 1 solutions to write") {
		t.Errorf("check output = %q", stdout.String())
	}
	if _, statErr := os.Stat(brain); !os.IsNotExist(statErr) {
		t.Error("a check run must not create anything")
	}
}

func TestPublishRejectsTwoWaysOfNamingATarget(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	err := run(context.Background(), []string{"publish", "--section", "1.1", "1.1", "8"}, &stdout, &stdout)
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("err = %v", err)
	}
}

func TestCoverageCountsTheQueueAndItsMachineForm(t *testing.T) {
	t.Parallel()
	brain, source, output := publishTree(t)
	// A second exercise nobody has touched, so there is something to be missing.
	dir := filepath.Join(source, "content", "vol1", "exercises", "1.1")
	if err := os.WriteFile(filepath.Join(dir, "02.md"), []byte("**2.** [*20*] Prove it.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	flags := []string{"coverage", "--brain", brain, "--source", source, "--output", output}

	var stdout bytes.Buffer
	if err := run(context.Background(), flags, &stdout, &stdout); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"vol1", "1.1", "1 missing / 2"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("coverage output missing %q, got\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	if err := run(context.Background(), append(flags, "--missing"), &stdout, &stdout); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "1.1 2\n" {
		t.Fatalf("queue = %q, want the one unsolved exercise", stdout.String())
	}

	stdout.Reset()
	if err := run(context.Background(), append(flags, "--json"), &stdout, &stdout); err != nil {
		t.Fatal(err)
	}
	var report coverage.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Total != 2 || report.Solved != 1 || report.Missing != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestCoverageRejectsTwoWaysOfNamingASection(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	err := run(context.Background(), []string{"coverage", "--section", "1.1", "1.2.1"}, &stdout, &stdout)
	if err == nil || !strings.Contains(err.Error(), "not both") {
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
