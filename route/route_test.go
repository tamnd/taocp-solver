package route

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/taocp-solver/api"
)

func TestRegistryRoundTripsAndSortsByRank(t *testing.T) {
	t.Parallel()
	registry := Registry{Routes: []Route{
		{Name: "slow", Wire: WireChat, BaseURL: "http://b", Model: "m2", Rank: 90},
		{Name: "fast", Wire: WireChat, BaseURL: "http://a", Model: "m1", Rank: 10,
			Timeout: Duration(90 * time.Second)},
	}}
	path := filepath.Join(t.TempDir(), "routes.json")
	if err := registry.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// The duration must be readable by whoever edits the file next.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"timeout": "1m30s"`) {
		t.Errorf("timeout is not written as a readable duration:\n%s", raw)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.Names(); got[0] != "fast" || got[1] != "slow" {
		t.Errorf("names = %v, want rank order", got)
	}
	if loaded.Routes[0].Timeout.Duration() != 90*time.Second {
		t.Errorf("timeout = %s", loaded.Routes[0].Timeout.Duration())
	}
}

func TestDurationAcceptsSecondsAsWellAsAString(t *testing.T) {
	t.Parallel()
	var value struct {
		Timeout Duration `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(`{"timeout": 45}`), &value); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if value.Timeout.Duration() != 45*time.Second {
		t.Errorf("timeout = %s, want 45s", value.Timeout.Duration())
	}
	if err := json.Unmarshal([]byte(`{"timeout": "banana"}`), &value); err == nil {
		t.Error("an unparseable duration must be an error")
	}
}

func TestSelectKeepsTheCallersOrderAndOverridesDisabled(t *testing.T) {
	t.Parallel()
	registry := Registry{Routes: []Route{
		{Name: "a", Wire: WireChat, BaseURL: "http://a", Model: "m", Rank: 10},
		{Name: "b", Wire: WireChat, BaseURL: "http://b", Model: "m", Rank: 20},
		{Name: "c", Wire: WireChat, BaseURL: "http://c", Model: "m", Rank: 30, Disabled: true},
	}}

	selected, err := registry.Select([]string{"c", "a"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got := selected.Names(); len(got) != 2 || got[0] != "c" || got[1] != "a" {
		t.Errorf("names = %v, want the order asked for", got)
	}
	// Naming a disabled route is an override, not a request to skip it.
	if selected.Routes[0].Disabled {
		t.Error("a route named explicitly must not stay disabled")
	}
	if _, err := registry.Select([]string{"nope"}); err == nil {
		t.Error("an unknown route name must be an error")
	} else if !strings.Contains(err.Error(), "a, b, c") {
		t.Errorf("error = %q, want it to list the known routes", err)
	}
}

func TestEnabledSkipsDisabledRoutes(t *testing.T) {
	t.Parallel()
	registry := Default()
	names := map[string]bool{}
	for _, value := range registry.Enabled() {
		names[value.Name] = true
	}
	if names["zen-free-laguna"] {
		t.Error("a disabled route was returned by Enabled")
	}
	if !names["codex"] {
		t.Error("the top-ranked route is missing")
	}
	// The disabled row still has to be present in the file, with its reason,
	// or the next reader will think it was never considered.
	laguna, ok := registry.Find("zen-free-laguna")
	if !ok {
		t.Fatal("the disabled route was deleted rather than disabled")
	}
	if laguna.Note == "" {
		t.Error("a disabled route with no note reads as an oversight")
	}
}

func TestDefaultRegistryIsValidAndOrdered(t *testing.T) {
	t.Parallel()
	registry := Default()
	if err := registry.Validate(); err != nil {
		t.Fatalf("the built-in routes do not validate: %v", err)
	}
	last := -1
	for _, value := range registry.Routes {
		if value.Rank < last {
			t.Fatalf("route %s at rank %d follows rank %d", value.Name, value.Rank, last)
		}
		last = value.Rank
	}
}

func TestValidateNamesWhatIsMissing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		route Route
		want  string
	}{
		{"no name", Route{Wire: WireChat, Model: "m", BaseURL: "http://a"}, "no name"},
		{"no model", Route{Name: "a", Wire: WireChat, BaseURL: "http://a"}, "no model"},
		{"chat without a base url", Route{Name: "a", Wire: WireChat, Model: "m"}, "base_url"},
		{"unknown wire", Route{Name: "a", Wire: "smoke", Model: "m"}, "unknown wire"},
		// The Codex wire reads a stored credential, so it needs no base URL.
		{"codex without a base url", Route{Name: "a", Wire: WireCodex, Model: "m"}, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.route.Validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestRegistryRejectsDuplicateNames(t *testing.T) {
	t.Parallel()
	registry := Registry{Routes: []Route{
		{Name: "a", Wire: WireChat, BaseURL: "http://a", Model: "m"},
		{Name: "a", Wire: WireChat, BaseURL: "http://b", Model: "m"},
	}}
	if err := registry.Validate(); err == nil {
		t.Fatal("two routes with one name must be an error")
	}
}

func TestEndpointToleratesBothBaseURLShapes(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"http://host:8790", "http://host:8790/", "http://host:8790/v1", "http://host:8790/v1/"} {
		value := Route{BaseURL: base}
		if got := value.endpoint("/chat/completions"); got != "http://host:8790/v1/chat/completions" {
			t.Errorf("base %q gave %q", base, got)
		}
	}
}

func TestClientMatchesTheWire(t *testing.T) {
	t.Parallel()
	chat, err := Route{Name: "a", Wire: WireChat, BaseURL: "http://a", Model: "m"}.Client(0, 0)
	if err != nil {
		t.Fatalf("chat client: %v", err)
	}
	if _, ok := chat.(*api.ChatClient); !ok {
		t.Errorf("chat wire built %T", chat)
	}
	responses, err := Route{Name: "a", Wire: WireResponses, BaseURL: "http://a", Model: "m"}.Client(0, 0)
	if err != nil {
		t.Fatalf("responses client: %v", err)
	}
	if _, ok := responses.(*api.Client); !ok {
		t.Errorf("responses wire built %T", responses)
	}
	if _, err := (Route{Name: "a", Wire: "smoke", Model: "m"}).Client(0, 0); err == nil {
		t.Error("an unknown wire must not produce a client")
	}
}

func TestPricingModelFallsBackToTheModel(t *testing.T) {
	t.Parallel()
	if got := (Route{Model: "m", Pricing: "gpt-5.6-sol"}).PricingModel(); got != "gpt-5.6-sol" {
		t.Errorf("pricing model = %q", got)
	}
	if got := (Route{Model: "m"}).PricingModel(); got != "m" {
		t.Errorf("pricing model = %q, want the model itself", got)
	}
}

func TestLoadOrDefaultFailsLoudlyOnANamedFile(t *testing.T) {
	t.Parallel()
	// Silently falling back would run a different set of routes than the one
	// the operator asked for, which is the worst possible way to be helpful.
	if _, _, err := LoadOrDefault(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("a named route file that does not exist must be an error")
	}
	registry, source, err := LoadOrDefault("")
	if err != nil {
		t.Fatalf("LoadOrDefault: %v", err)
	}
	if len(registry.Routes) == 0 || source == "" {
		t.Errorf("registry = %d routes from %q", len(registry.Routes), source)
	}
}

func TestAdHocRouteOutranksEverything(t *testing.T) {
	t.Parallel()
	value := AdHoc("http://localhost:8790/v1", "gpt-5.6-luna", "TAOCP_SOLVER_API_KEY", "high")
	if err := value.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, other := range Default().Routes {
		if other.Rank <= value.Rank {
			t.Fatalf("route %s at rank %d is not outranked by the ad hoc route", other.Name, other.Rank)
		}
	}
}
