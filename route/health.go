package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/taocp-solver/api"
	"github.com/tamnd/taocp-solver/codex"
)

// State is what a probe found. The distinctions exist because the remedies
// differ: no credits is a wait, a bad key is a login, and a deleted model is
// an edit to the route file.
type State string

const (
	StateLive         State = "live"
	StateQuota        State = "quota"
	StateUnauthorized State = "unauthorized"
	StateUnreachable  State = "unreachable"
	StateBroken       State = "broken"
	StateGone         State = "gone"
	StateUnknown      State = "unknown"
)

// Usable reports whether a route in this state is worth sending work to.
func (s State) Usable() bool { return s == StateLive || s == StateUnknown }

// Health is one probe result.
type Health struct {
	Route     string        `json:"route"`
	State     State         `json:"state"`
	Latency   time.Duration `json:"latency"`
	Detail    string        `json:"detail"`
	ResetsAt  time.Time     `json:"resets_at,omitempty"`
	Model     string        `json:"model,omitempty"`
	CheckedAt time.Time     `json:"checked_at"`
	// Catalogue is the model list the route advertises, when it has one.
	Catalogue []string `json:"catalogue,omitempty"`
	// Drift is set when the configured model is absent from the catalogue.
	Drift string `json:"drift,omitempty"`
}

// Signal is a classified provider response.
type Signal struct {
	State    State
	Detail   string
	ResetsAt time.Time
}

// quotaMarkers are copied from live provider bodies rather than guessed. Each
// one is a different vendor's way of saying the same thing.
var quotaMarkers = []string{
	"usage_limit_reached",
	"insufficient balance",
	"creditserror",
	"rate_limit_exceeded",
	"provider_rate_limit_exceeded",
	"quota",
}

var goneMarkers = []string{
	"is not supported",
	"model_not_found",
	"unknown model",
}

// Classify maps a provider signal to a state. Order matters: a body that names
// a missing model is a route file problem even when it arrives with a status
// that would otherwise read as something else.
func Classify(status int, body string, err error) Signal {
	if err != nil {
		return classifyTransport(err)
	}
	lower := strings.ToLower(body)
	resets := resetsFrom(body)
	switch {
	case containsAny(lower, goneMarkers):
		return Signal{State: StateGone, Detail: firstMessage(body, "model is not served here")}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Signal{State: StateUnauthorized, Detail: firstMessage(body, "credential rejected")}
	case status == http.StatusTooManyRequests || containsAny(lower, quotaMarkers):
		return Signal{State: StateQuota, Detail: firstMessage(body, "out of quota"), ResetsAt: resets}
	case strings.Contains(lower, "no chatgpt response found"):
		return Signal{State: StateBroken, Detail: "no ChatGPT response found"}
	case status >= 400:
		return Signal{State: StateBroken, Detail: firstMessage(body, fmt.Sprintf("upstream returned %d", status))}
	case strings.TrimSpace(body) == "":
		// A 200 with nothing in it is worse than an error, because it looks
		// like an answer. It must never reach the selector as a candidate.
		return Signal{State: StateBroken, Detail: "empty response"}
	}
	return Signal{State: StateLive, Detail: "ok"}
}

// classifyTransport reads a Go-side error. The api clients fold the upstream
// status and body into their error text, so this handles both a dial failure
// and a relayed provider message.
func classifyTransport(err error) Signal {
	var quota *codex.QuotaError
	if errors.As(err, &quota) {
		return Signal{State: StateQuota, Detail: quota.Message, ResetsAt: quota.ResetsAt}
	}
	if errors.Is(err, context.Canceled) {
		return Signal{State: StateUnknown, Detail: err.Error()}
	}
	text := err.Error()
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, goneMarkers):
		return Signal{State: StateGone, Detail: text}
	case containsAny(lower, []string{"401", "403", "unauthorized", "forbidden", "invalid_api_key"}):
		return Signal{State: StateUnauthorized, Detail: text}
	case containsAny(lower, quotaMarkers) || strings.Contains(lower, "429"):
		return Signal{State: StateQuota, Detail: text, ResetsAt: resetsFrom(text)}
	case errors.Is(err, context.DeadlineExceeded) ||
		containsAny(lower, []string{"no such host", "connection refused", "dial tcp", "tls:", "i/o timeout", "timeout"}):
		return Signal{State: StateUnreachable, Detail: text}
	}
	return Signal{State: StateBroken, Detail: text}
}

func containsAny(lower string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

var (
	// The quotes are optional because this also runs over an error string
	// that folded the body into a sentence.
	resetsAtPattern   = regexp.MustCompile(`"?resets_at"?\s*[:= ]\s*(\d+)`)
	retryDelayPattern = regexp.MustCompile(`(?i)"?retry(?:_delay|_after|-after|AfterSeconds)"?\s*[:=]\s*"?(\d+)`)
)

// resetsFrom digs the reset instant out of whatever shape the provider used.
// Providers that offer nothing leave it zero, and a caller must treat a zero
// instant as unknown rather than as the epoch.
func resetsFrom(body string) time.Time {
	if match := resetsAtPattern.FindStringSubmatch(body); match != nil {
		if seconds, err := strconv.ParseInt(match[1], 10, 64); err == nil && seconds > 0 {
			return time.Unix(seconds, 0).UTC()
		}
	}
	if match := retryDelayPattern.FindStringSubmatch(body); match != nil {
		if seconds, err := strconv.Atoi(match[1]); err == nil && seconds > 0 {
			return time.Now().UTC().Add(time.Duration(seconds) * time.Second)
		}
	}
	return time.Time{}
}

// firstMessage pulls a human sentence out of an error body, falling back to a
// description when the body is not the shape we expected.
func firstMessage(body, fallback string) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err == nil {
		if text := strings.TrimSpace(envelope.Error.Message); text != "" {
			return text
		}
		if text := strings.TrimSpace(envelope.Message); text != "" {
			return text
		}
	}
	text := strings.TrimSpace(body)
	if text == "" {
		return fallback
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return strings.Join(strings.Fields(text), " ")
}

// probePrompt is fixed and trivial on purpose. Probing the whole registry
// should cost a few hundred tokens, not a solve. It is the prompt that keeps
// the cost down rather than an output ceiling, because a low ceiling on a
// reasoning model returns empty text and would read as a broken route.
const probePrompt = "Reply with the single word ok."

// Prober runs health checks.
type Prober struct {
	HTTPClient *http.Client
	Timeout    time.Duration
	Now        func() time.Time
}

func (p Prober) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p Prober) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// Probe checks one route. It runs a catalogue call where the wire has one,
// then a trivial completion, because a catalogue that lists a model says
// nothing about whether the account may call it.
func (p Prober) Probe(ctx context.Context, value Route) Health {
	started := p.now()
	health := Health{Route: value.Name, Model: value.Model, CheckedAt: started}
	finish := func(signal Signal) Health {
		health.State = signal.State
		health.Detail = signal.Detail
		health.ResetsAt = signal.ResetsAt
		health.Latency = p.now().Sub(started)
		return health
	}
	if err := value.Validate(); err != nil {
		return finish(Signal{State: StateBroken, Detail: err.Error()})
	}

	if value.Wire == WireCodex {
		signal := p.probeCredential(ctx, value)
		if signal.State != StateLive {
			return finish(signal)
		}
		health.Detail = signal.Detail
	} else {
		catalogue, signal := p.catalogue(ctx, value)
		health.Catalogue = catalogue
		if signal.State == StateUnreachable || signal.State == StateUnauthorized {
			return finish(signal)
		}
		// A catalogue that answers and does not list the model is the clearest
		// possible statement that the route file is stale.
		if len(catalogue) > 0 && !containsString(catalogue, value.Model) {
			health.Drift = driftMessage(value, catalogue)
			return finish(Signal{State: StateGone, Detail: health.Drift})
		}
	}

	client, err := value.Client(p.completionTimeout(), 0)
	if err != nil {
		return finish(Signal{State: StateBroken, Detail: err.Error()})
	}
	request := api.Request{Model: value.Model, Instructions: "Answer in one word.", Input: probePrompt, Effort: value.Effort}
	response, err := client.Complete(ctx, request)
	if err != nil {
		return finish(classifyTransport(err))
	}
	if strings.TrimSpace(response.Text) == "" {
		return finish(Signal{State: StateBroken, Detail: "completed with empty text"})
	}
	detail := "ok"
	if health.Detail != "" {
		detail = health.Detail
	}
	return finish(Signal{State: StateLive, Detail: detail})
}

func (p Prober) completionTimeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 60 * time.Second
}

// probeCredential is the catalogue step for the Codex wire: read the stored
// credential, refresh it if it is close to expiry, and report the plan.
func (p Prober) probeCredential(ctx context.Context, value Route) Signal {
	auth := &codex.Auth{Path: value.Auth, HTTPClient: p.httpClient()}
	token, err := auth.Token(ctx)
	if err != nil {
		return Signal{State: StateUnauthorized, Detail: err.Error()}
	}
	plan := token.PlanType
	if plan == "" {
		plan = "unknown plan"
	}
	return Signal{State: StateLive, Detail: fmt.Sprintf("plan %s, token valid until %s",
		plan, token.ExpiresAt.UTC().Format(time.RFC3339))}
}

// catalogue calls GET /v1/models. A route whose catalogue call fails is not
// condemned on that alone, because some compatible servers do not implement it.
func (p Prober) catalogue(ctx context.Context, value Route) ([]string, Signal) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, value.endpoint("/models"), nil)
	if err != nil {
		return nil, Signal{State: StateBroken, Detail: err.Error()}
	}
	if value.APIKeyEnv != "" {
		if key := strings.TrimSpace(os.Getenv(value.APIKeyEnv)); key != "" {
			request.Header.Set("Authorization", "Bearer "+key)
		}
	}
	response, err := p.httpClient().Do(request)
	if err != nil {
		return nil, classifyTransport(err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, Signal{State: StateBroken, Detail: err.Error()}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, Classify(response.StatusCode, string(raw), nil)
	}
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, Signal{State: StateBroken, Detail: "catalogue is not valid JSON: " + err.Error()}
	}
	models := make([]string, 0, len(envelope.Data))
	for _, entry := range envelope.Data {
		if entry.ID != "" {
			models = append(models, entry.ID)
		}
	}
	return models, Signal{State: StateLive, Detail: "ok"}
}

func driftMessage(value Route, catalogue []string) string {
	free := make([]string, 0, len(catalogue))
	for _, model := range catalogue {
		if strings.HasSuffix(model, "-free") {
			free = append(free, model)
		}
	}
	message := fmt.Sprintf("model %s is not in the %s catalogue", value.Model, value.BaseURL)
	if len(free) > 0 {
		message += fmt.Sprintf("; %d free models available: %s", len(free), strings.Join(free, ", "))
	} else if len(catalogue) > 0 {
		message += fmt.Sprintf("; %d models available", len(catalogue))
	}
	return message
}

func containsString(list []string, want string) bool {
	for _, value := range list {
		if value == want {
			return true
		}
	}
	return false
}
