package route

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Every body below is copied from a live probe on 2026-07-25. Paraphrasing
// them would defeat the purpose: the point of this table is that it matches
// what the providers actually send, not what their documentation claims.
const (
	zenCreditsBody = `{"error":{"message":"CreditsError: Insufficient balance. Please add credits to your account.","type":"invalid_request_error"}}`

	codexQuotaBody = `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":1785326082,"eligible_promo":null,"resets_in_seconds":376150}}`

	proxyDownBody = `{"error":{"message":"No ChatGPT response found","type":"server_error"}}`

	goneModelBody = `{"error":{"message":"ModelError: Model hy3-free is not supported","type":"invalid_request_error"}}`

	lagunaBody = `{"error":{"message":"provider_rate_limit_exceeded","type":"rate_limit_error"}}`

	unauthorizedBody = `{"error":{"message":"Invalid API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`
)

func TestClassifyRecordedProviderBodies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   string
		want   State
		resets int64
	}{
		{"zen out of credits", http.StatusBadRequest, zenCreditsBody, StateQuota, 0},
		{"codex weekly limit", http.StatusTooManyRequests, codexQuotaBody, StateQuota, 1785326082},
		{"browser proxy is down", http.StatusServiceUnavailable, proxyDownBody, StateBroken, 0},
		{"model was deleted", http.StatusBadRequest, goneModelBody, StateGone, 0},
		{"provider rate limit", http.StatusTooManyRequests, lagunaBody, StateQuota, 0},
		{"bad key", http.StatusUnauthorized, unauthorizedBody, StateUnauthorized, 0},
		{"a 200 with nothing in it", http.StatusOK, "", StateBroken, 0},
		{"a normal answer", http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`, StateLive, 0},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			signal := Classify(test.status, test.body, nil)
			if signal.State != test.want {
				t.Errorf("state = %s, want %s (detail %q)", signal.State, test.want, signal.Detail)
			}
			if test.resets == 0 {
				if !signal.ResetsAt.IsZero() {
					t.Errorf("resets_at = %s, want zero when the provider offers nothing", signal.ResetsAt)
				}
				return
			}
			if got := signal.ResetsAt.Unix(); got != test.resets {
				t.Errorf("resets_at = %d, want %d", got, test.resets)
			}
		})
	}
}

// A deleted model has to be read as a route file problem even though it
// arrives with a status that would otherwise say something else. Getting this
// backwards produces one puzzling failure per call instead of one clear line.
func TestGoneOutranksTheStatusCode(t *testing.T) {
	t.Parallel()
	if got := Classify(http.StatusTooManyRequests, goneModelBody, nil).State; got != StateGone {
		t.Errorf("state = %s, want gone even on a 429", got)
	}
}

func TestClassifyMessageIsTheProvidersOwn(t *testing.T) {
	t.Parallel()
	signal := Classify(http.StatusBadRequest, zenCreditsBody, nil)
	if !strings.Contains(signal.Detail, "Insufficient balance") {
		t.Errorf("detail = %q, want the provider's sentence", signal.Detail)
	}
}

func TestClassifyTransportErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want State
	}{
		{"host is gone", errors.New(`dial tcp 127.0.0.1:8789: connect: connection refused`), StateUnreachable},
		{"dns", errors.New(`Get "http://nope/v1/models": dial tcp: lookup nope: no such host`), StateUnreachable},
		{"deadline", context.DeadlineExceeded, StateUnreachable},
		{"relayed 429", fmt.Errorf("chat completions API returned 429 Too Many Requests: %s", lagunaBody), StateQuota},
		{"relayed 401", fmt.Errorf("chat completions API returned 401 Unauthorized: %s", unauthorizedBody), StateUnauthorized},
		{"relayed model error", fmt.Errorf("chat completions API returned 400: %s", goneModelBody), StateGone},
		// Free routes sometimes emit a raw control character inside
		// reasoning_content, which makes encoding/json reject the whole body.
		// That has to fail over as broken. Reading it as an empty answer would
		// let a decode bug reach the selector as a contentless candidate.
		{"undecodable stream", errors.New("decode chat stream: invalid character '\\n' in string literal"), StateBroken},
		// A cancelled context is the operator, not the provider. Punishing a
		// route with a cooldown for it would be wrong.
		{"cancelled", context.Canceled, StateUnknown},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyTransport(test.err).State; got != test.want {
				t.Errorf("state = %s, want %s", got, test.want)
			}
		})
	}
}

func TestClassifyReadsRetryDelayWhenThereIsNoResetsAt(t *testing.T) {
	t.Parallel()
	body := `{"error":{"message":"rate limit","type":"rate_limit_error"},"retry_after":120}`
	signal := Classify(http.StatusTooManyRequests, body, nil)
	if signal.ResetsAt.IsZero() {
		t.Fatal("a retry delay must become a reset instant")
	}
	if delay := time.Until(signal.ResetsAt); delay < 110*time.Second || delay > 130*time.Second {
		t.Errorf("reset is %s away, want about two minutes", delay)
	}
}

// probeServer answers the catalogue and completion calls a probe makes.
func probeServer(t *testing.T, models []string, status int, completion string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			entries := make([]string, 0, len(models))
			for _, model := range models {
				entries = append(entries, fmt.Sprintf(`{"id":%q}`, model))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(entries, ","))
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(completion))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", completion)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

func TestProbeReportsLiveOnAGoodRoute(t *testing.T) {
	t.Parallel()
	url := probeServer(t, []string{"nemotron-3-ultra-free"}, http.StatusOK, "ok")
	health := Prober{}.Probe(context.Background(),
		Route{Name: "zen-free-nemotron", Wire: WireChat, BaseURL: url, Model: "nemotron-3-ultra-free"})

	if health.State != StateLive {
		t.Fatalf("state = %s: %s", health.State, health.Detail)
	}
	if health.Route != "zen-free-nemotron" || health.Model != "nemotron-3-ultra-free" {
		t.Errorf("health = %+v", health)
	}
	if health.CheckedAt.IsZero() {
		t.Error("no check time recorded")
	}
}

func TestProbeReportsDriftWhenTheCatalogueLacksTheModel(t *testing.T) {
	t.Parallel()
	catalogue := []string{"deepseek-v4-flash-free", "mimo-v2.5-free", "ling-3.0-flash-free",
		"nemotron-3-ultra-free", "north-mini-code-free", "laguna-s-2.1-free"}
	url := probeServer(t, catalogue, http.StatusOK, "ok")
	health := Prober{}.Probe(context.Background(),
		Route{Name: "zen-free-hy3", Wire: WireChat, BaseURL: url, Model: "hy3-free"})

	if health.State != StateGone {
		t.Fatalf("state = %s, want gone: %s", health.State, health.Detail)
	}
	if !strings.Contains(health.Drift, "hy3-free is not in the") {
		t.Errorf("drift = %q", health.Drift)
	}
	if !strings.Contains(health.Drift, "6 free models available") {
		t.Errorf("drift = %q, want the available free models listed", health.Drift)
	}
}

func TestProbeReportsQuotaFromTheCompletionStep(t *testing.T) {
	t.Parallel()
	// The catalogue lists the model and the account still cannot call it.
	// That is exactly why the probe has two steps.
	url := probeServer(t, []string{"gpt-5.6-sol"}, http.StatusBadRequest, zenCreditsBody)
	health := Prober{}.Probe(context.Background(),
		Route{Name: "zen-paid", Wire: WireChat, BaseURL: url, Model: "gpt-5.6-sol"})

	if health.State != StateQuota {
		t.Fatalf("state = %s, want quota: %s", health.State, health.Detail)
	}
}

func TestProbeReportsUnreachableWhenNothingIsListening(t *testing.T) {
	t.Parallel()
	health := Prober{Timeout: time.Second}.Probe(context.Background(),
		Route{Name: "gamingpc", Wire: WireChat, BaseURL: "http://127.0.0.1:1/v1", Model: "gpt-oss:20b"})

	if health.State != StateUnreachable {
		t.Fatalf("state = %s, want unreachable: %s", health.State, health.Detail)
	}
}

func TestProbeTreatsAnEmptyAnswerAsBroken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	health := Prober{}.Probe(context.Background(),
		Route{Name: "empty", Wire: WireChat, BaseURL: server.URL + "/v1", Model: "m"})
	if health.State != StateBroken {
		t.Fatalf("state = %s, want broken: %s", health.State, health.Detail)
	}
}

// A server with no /v1/models is not condemned for that alone, because plenty
// of compatible servers do not implement it.
func TestProbeSurvivesAMissingCatalogue(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	health := Prober{}.Probe(context.Background(),
		Route{Name: "spare", Wire: WireChat, BaseURL: server.URL + "/v1", Model: "m"})
	if health.State != StateLive {
		t.Fatalf("state = %s, want live: %s", health.State, health.Detail)
	}
}

func TestProbeRejectsAnInvalidRoute(t *testing.T) {
	t.Parallel()
	health := Prober{}.Probe(context.Background(), Route{Name: "broken", Wire: WireChat, Model: "m"})
	if health.State != StateBroken || !strings.Contains(health.Detail, "base_url") {
		t.Fatalf("health = %+v", health)
	}
}

func TestAGatewayErrorPageStaysOnOneLine(t *testing.T) {
	t.Parallel()
	page := "chat completions API returned 502 Bad Gateway: <!DOCTYPE html>\n<html>\n<head>\n<title>502</title>\n</head>\n" +
		strings.Repeat("<div>the gateway is down</div>\n", 40)
	for name, detail := range map[string]string{
		"transport": classifyTransport(errors.New(page)).Detail,
		"body":      Classify(502, page, nil).Detail,
	} {
		if strings.Contains(detail, "\n") {
			t.Errorf("%s detail is %d lines, and a log with markup in it is unreadable", name, strings.Count(detail, "\n")+1)
		}
		if len(detail) > 210 {
			t.Errorf("%s detail is %d characters, which is a paragraph rather than a log line", name, len(detail))
		}
	}
}
