// Package route names the endpoints a solve can run on, orders them, and keeps
// a run moving when one of them stops answering.
//
// A single --base-url is fine when it works. It is useless the moment the
// account behind it runs out of quota, which is a normal daily event on free
// and subscription routes rather than an exceptional one. Naming the routes
// makes the fallback order reviewable instead of implicit.
package route

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/codex"
)

// Wire is the request format an endpoint speaks.
type Wire string

const (
	WireChat      Wire = "chat"      // POST /v1/chat/completions, streaming
	WireResponses Wire = "responses" // POST /v1/responses
	WireCodex     Wire = "codex"     // the Codex backend, reached with a stored credential
)

// Route is one endpoint the solver can send a model call to.
type Route struct {
	Name string `json:"name"`
	Wire Wire   `json:"wire"`
	// BaseURL may end at the server root or at /v1. WireCodex needs none.
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model"`
	// APIKeyEnv names the environment variable holding the key, so a route
	// file can be committed or shared without carrying a secret.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Auth is the credential path for WireCodex. Empty means the default.
	Auth            string   `json:"auth,omitempty"`
	Effort          string   `json:"effort,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Timeout         Duration `json:"timeout,omitempty"`
	// Rank orders the routes, lowest first. It is not a quality score, it is
	// the order to try things in.
	Rank int `json:"rank"`
	// Pricing names the model whose published rate card applies. Empty means
	// unpriced, and a report says unavailable rather than zero.
	Pricing  string `json:"pricing,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// Note carries why a route is ranked or disabled where it is. A disabled
	// row with no explanation reads as an oversight.
	Note string `json:"note,omitempty"`
}

// Duration is a time.Duration that round trips through JSON as "30m" rather
// than as a nanosecond count no one can read.
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("parse duration %q: %w", text, err)
		}
		*d = Duration(value)
		return nil
	}
	// A bare number is read as seconds, which is what someone hand editing a
	// route file is most likely to have meant.
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err != nil {
		return fmt.Errorf("duration must be a string like \"30m\" or a number of seconds")
	}
	*d = Duration(time.Duration(seconds * float64(time.Second)))
	return nil
}

// Validate reports what is missing from a route rather than letting it fail at
// the first call with a confusing transport error.
func (r Route) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("route has no name")
	}
	if strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("route %s has no model", r.Name)
	}
	switch r.Wire {
	case WireChat, WireResponses:
		if strings.TrimSpace(r.BaseURL) == "" {
			return fmt.Errorf("route %s speaks %s and needs a base_url", r.Name, r.Wire)
		}
	case WireCodex:
	default:
		return fmt.Errorf("route %s has unknown wire %q, want chat, responses, or codex", r.Name, r.Wire)
	}
	return nil
}

// Registry is an ordered set of routes.
type Registry struct {
	Routes []Route `json:"routes"`
}

// DefaultPath is where a personal route file lives.
func DefaultPath() string {
	if value := strings.TrimSpace(os.Getenv("TAOCP_ROUTES")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "taocp", "routes.json")
}

// Load reads a registry from disk.
func Load(path string) (Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Registry{}, err
	}
	var value Registry
	if err := json.Unmarshal(raw, &value); err != nil {
		return Registry{}, fmt.Errorf("decode route file %s: %w", path, err)
	}
	if err := value.Validate(); err != nil {
		return Registry{}, fmt.Errorf("route file %s: %w", path, err)
	}
	value.sort()
	return value, nil
}

// LoadOrDefault reads the named file, falls back to the personal route file,
// and finally to the built-in defaults. An explicitly named file that does not
// exist is an error, because silently ignoring it would run the wrong routes.
func LoadOrDefault(path string) (Registry, string, error) {
	if strings.TrimSpace(path) != "" {
		registry, err := Load(path)
		return registry, path, err
	}
	if personal := DefaultPath(); personal != "" {
		registry, err := Load(personal)
		if err == nil {
			return registry, personal, nil
		}
		if !os.IsNotExist(err) {
			return Registry{}, personal, err
		}
	}
	return Default(), "built-in", nil
}

// Write saves a registry for editing.
func (r Registry) Write(path string) error {
	if directory := filepath.Dir(path); directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func (r Registry) Validate() error {
	if len(r.Routes) == 0 {
		return fmt.Errorf("route file lists no routes")
	}
	seen := map[string]bool{}
	for _, value := range r.Routes {
		if err := value.Validate(); err != nil {
			return err
		}
		if seen[value.Name] {
			return fmt.Errorf("route %s is listed twice", value.Name)
		}
		seen[value.Name] = true
	}
	return nil
}

func (r *Registry) sort() {
	slices.SortStableFunc(r.Routes, func(a, b Route) int { return a.Rank - b.Rank })
}

// Enabled returns the usable routes in rank order. It sorts rather than
// assuming the caller did, because a registry built in code is easy to write
// out of order and the resulting failover would be silently wrong.
func (r Registry) Enabled() []Route {
	var out []Route
	for _, value := range r.Routes {
		if !value.Disabled {
			out = append(out, value)
		}
	}
	slices.SortStableFunc(out, func(a, b Route) int { return a.Rank - b.Rank })
	return out
}

// Find returns the route with the given name.
func (r Registry) Find(name string) (Route, bool) {
	for _, value := range r.Routes {
		if value.Name == name {
			return value, true
		}
	}
	return Route{}, false
}

// Select restricts the registry to the named routes, in the order given. The
// caller's order wins over rank, because naming routes explicitly is a
// statement about what to try first. A named route that is disabled in the
// file is included, since naming it is an override.
func (r Registry) Select(names []string) (Registry, error) {
	var out Registry
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		value, ok := r.Find(name)
		if !ok {
			return Registry{}, fmt.Errorf("unknown route %q, have %s", name, strings.Join(r.Names(), ", "))
		}
		value.Disabled = false
		out.Routes = append(out.Routes, value)
	}
	if len(out.Routes) == 0 {
		return Registry{}, fmt.Errorf("no routes selected")
	}
	return out, nil
}

// Names lists every route name in rank order.
func (r Registry) Names() []string {
	out := make([]string, 0, len(r.Routes))
	for _, value := range r.Routes {
		out = append(out, value.Name)
	}
	return out
}

// Ad hoc route rank. A --base-url on the command line is a deliberate
// instruction and outranks everything configured.
const AdHocRank = 0

// AdHoc builds the single route described by --base-url, --model and friends,
// so every existing command line keeps working unchanged.
func AdHoc(baseURL, model, apiKeyEnv, effort string) Route {
	return Route{
		Name: "ad-hoc", Wire: WireChat, BaseURL: baseURL, Model: model,
		APIKeyEnv: apiKeyEnv, Effort: effort, Rank: AdHocRank, Pricing: model,
	}
}

// Default is the built-in route order, best first.
//
// The ranks are evidence, not preference. The two free routes at 30 and 31 are
// the ones that have carried a complete slow-mode solve to a passing verdict.
// A route earns promotion the same way.
func Default() Registry {
	zen := env("TAOCP_ZEN_PROXY_URL", "http://127.0.0.1:8788/v1")
	local := env("TAOCP_LOCAL_PROXY_URL", "http://127.0.0.1:8789/v1")
	web2 := env("TAOCP_WEB_S2_URL", "")
	web3 := env("TAOCP_WEB_S3_URL", "")

	free := func(rank int, name, model, note string) Route {
		return Route{
			Name: name, Wire: WireChat, BaseURL: zen, Model: model,
			APIKeyEnv: "OPENCODE_API_KEY", Rank: rank, Pricing: model, Note: note,
		}
	}
	routes := []Route{
		{
			Name: "codex", Wire: WireCodex, Model: codex.DefaultModel, Rank: 10,
			Pricing: codex.DefaultModel, Effort: "high",
			Note: "subscription credential, quota limited per account",
		},
		{
			Name: "zen-paid", Wire: WireChat, BaseURL: zen, Model: "gpt-5.6-sol",
			APIKeyEnv: "OPENCODE_API_KEY", Rank: 20, Pricing: "gpt-5.6-sol",
			Note: "needs credits on the account",
		},
		free(30, "zen-free-nemotron", "nemotron-3-ultra-free", "full slow-mode solve proven"),
		free(31, "zen-free-deepseek", "deepseek-v4-flash-free", "full slow-mode solve proven, 1.7x the wall clock"),
		free(32, "zen-free-mimo", "mimo-v2.5-free", "correct and leanest on the proof probe, no full solve yet"),
		free(33, "zen-free-north-mini", "north-mini-code-free", "correct and fast, code tuned, no full solve yet"),
		free(34, "zen-free-ling", "ling-3.0-flash-free", "correct but burns 11x nemotron's tokens"),
		func() Route {
			value := free(35, "zen-free-laguna", "laguna-s-2.1-free", "provider rate limit on every probe so far")
			// Disabled rather than deleted. A provider rate limit is a state
			// that can clear, and a missing row would just look like an
			// oversight the next time someone reads this list.
			value.Disabled = true
			return value
		}(),
		{
			Name: "gamingpc", Wire: WireChat, BaseURL: local, Model: "gpt-oss:20b",
			Rank: 40, Note: "only when the tunnel is up, no published rate card",
		},
	}
	// The web proxies use gpt-5-5 because that is the strongest slug a free
	// account exposes. Asking that proxy for anything newer is a lie it
	// happily echoes back, since it ignores the model field entirely.
	if web2 != "" {
		routes = append(routes, Route{Name: "chatgptweb-s2", Wire: WireChat, BaseURL: web2, Model: "gpt-5-5", Rank: 90, Note: "browser proxy"})
	}
	if web3 != "" {
		routes = append(routes, Route{Name: "chatgptweb-s3", Wire: WireChat, BaseURL: web3, Model: "gpt-5-5", Rank: 91, Note: "browser proxy"})
	}
	registry := Registry{Routes: routes}
	registry.sort()
	return registry
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// Client builds the transport for a route.
func (r Route) Client(timeout time.Duration, maxRetries int) (api.Completer, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.Timeout > 0 {
		timeout = r.Timeout.Duration()
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	httpClient := &http.Client{Timeout: timeout}
	if r.Wire == WireCodex {
		auth := &codex.Auth{Path: r.Auth, HTTPClient: httpClient}
		return &codex.Client{
			Auth: auth, HTTPClient: httpClient,
			MaxRetries: maxRetries, Effort: r.Effort,
		}, nil
	}
	key := ""
	if r.APIKeyEnv != "" {
		key = strings.TrimSpace(os.Getenv(r.APIKeyEnv))
	}
	if r.Wire == WireResponses {
		return &api.Client{
			URL: r.endpoint("/responses"), APIKey: key, HTTPClient: httpClient,
			MaxRetries: maxRetries, MaxOutputTokens: r.MaxOutputTokens, UserAgent: userAgent,
		}, nil
	}
	return &api.ChatClient{
		URL: r.endpoint("/chat/completions"), APIKey: key, HTTPClient: httpClient,
		MaxRetries: maxRetries, MaxOutputTokens: r.MaxOutputTokens, UserAgent: userAgent,
	}, nil
}

const userAgent = "taocp-router"

// endpoint joins the base URL to a path, tolerating a base that already ends
// at /v1 and one that stops at the server root.
func (r Route) endpoint(path string) string {
	base := strings.TrimRight(r.BaseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + path
	}
	return base + "/v1" + path
}

// PricingModel is the model whose rate card applies to this route.
func (r Route) PricingModel() string {
	if strings.TrimSpace(r.Pricing) != "" {
		return r.Pricing
	}
	return r.Model
}
