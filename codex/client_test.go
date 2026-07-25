package codex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/taocp-solver/api"
)

// The exact body the live endpoint returned on 2026-07-25 with an exhausted
// Plus plan. It is the fixture the quota classification exists for.
const quotaBody = `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":1785326082,"eligible_promo":null,"resets_in_seconds":376150}}`

// liveAuth writes a credential good for hours so a client test never refreshes.
func liveAuth(t *testing.T) *Auth {
	t.Helper()
	now := time.Now()
	path := filepath.Join(t.TempDir(), "auth.json")
	body := `{"tokens":{"access_token":"` + accessToken(now.Add(4*time.Hour)) +
		`","id_token":"` + idToken("plus", "acct-1") + `","account_id":"acct-1"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Auth{Path: path}
}

// stream builds an SSE body out of raw event payloads.
func stream(events ...string) string {
	var out strings.Builder
	for _, event := range events {
		out.WriteString("data: " + event + "\n\n")
	}
	return out.String()
}

const completedEvent = `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-luna",` +
	`"usage":{"input_tokens":120,"output_tokens":80,"total_tokens":200,` +
	`"input_tokens_details":{"cached_tokens":40},"output_tokens_details":{"reasoning_tokens":30}}}}`

// codexServer answers every request with one canned reply.
func codexServer(t *testing.T, status int, body string, headers map[string]string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, value := range headers {
			w.Header().Set(name, value)
		}
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return &Client{Auth: liveAuth(t), URL: server.URL}
}

func TestCompleteParsesStreamAndUsage(t *testing.T) {
	t.Parallel()
	body := stream(
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"thinking about it"}`,
		`{"type":"response.output_text.delta","delta":"The sum "}`,
		`{"type":"response.output_text.delta","delta":"is 42."}`,
		completedEvent,
	)
	client := codexServer(t, http.StatusOK, body, nil)

	response, err := client.Complete(context.Background(), api.Request{
		Model: "gpt-5.6-luna", Instructions: "be brief", Input: "add them up",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "The sum is 42." {
		t.Errorf("text = %q", response.Text)
	}
	// Reasoning text is not part of the answer. A proof that quotes the model's
	// own deliberation back at the reader is not publishable.
	if strings.Contains(response.Text, "thinking") {
		t.Error("reasoning text leaked into the answer")
	}
	want := api.Usage{
		InputTokens: 120, CachedInputTokens: 40, OutputTokens: 80,
		ReasoningTokens: 30, TotalTokens: 200,
	}
	if response.Usage != want {
		t.Errorf("usage = %+v, want %+v", response.Usage, want)
	}
}

func TestCompleteSendsTheHeadersTheBackendRequires(t *testing.T) {
	t.Parallel()
	var captured *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Clone(r.Context())
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream(`{"type":"response.output_text.delta","delta":"ok"}`, completedEvent)))
	}))
	defer server.Close()
	client := &Client{Auth: liveAuth(t), URL: server.URL}

	if _, err := client.Complete(context.Background(), api.Request{Input: "hi"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	for name, want := range map[string]string{
		"chatgpt-account-id": "acct-1",
		"OpenAI-Beta":        "responses=experimental",
		"originator":         "codex_cli_rs",
	} {
		if got := captured.Header.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if got := captured.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
		t.Errorf("Authorization = %q", got)
	}
	if captured.Header.Get("session_id") == "" {
		t.Error("session_id is empty")
	}
}

func TestTruncatedStreamIsAnError(t *testing.T) {
	t.Parallel()
	// Everything but response.completed. A partial proof must never be returned
	// as if it were whole.
	body := stream(
		`{"type":"response.output_text.delta","delta":"Suppose n is even, then"}`,
	)
	client := codexServer(t, http.StatusOK, body, nil)

	_, err := client.Complete(context.Background(), api.Request{Input: "prove it"})
	if err == nil {
		t.Fatal("a stream ending before response.completed must be an error")
	}
	if !strings.Contains(err.Error(), "response.completed") {
		t.Errorf("error = %q, want it to name the missing event", err)
	}
}

func TestErrorEventMidStreamIsAnError(t *testing.T) {
	t.Parallel()
	body := stream(
		`{"type":"response.output_text.delta","delta":"partial"}`,
		`{"type":"error","error":{"type":"server_error","message":"upstream exploded"}}`,
	)
	client := codexServer(t, http.StatusOK, body, nil)

	_, err := client.Complete(context.Background(), api.Request{Input: "go"})
	if err == nil || !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("error = %v, want the upstream message", err)
	}
}

func TestFailedResponseEventIsAnError(t *testing.T) {
	t.Parallel()
	body := stream(`{"type":"response.failed","response":{"status":"failed",` +
		`"error":{"message":"content policy"}}}`)
	client := codexServer(t, http.StatusOK, body, nil)

	_, err := client.Complete(context.Background(), api.Request{Input: "go"})
	if err == nil || !strings.Contains(err.Error(), "content policy") {
		t.Fatalf("error = %v, want the failure reason", err)
	}
}

func TestExhaustedPlanBecomesQuotaError(t *testing.T) {
	t.Parallel()
	client := codexServer(t, http.StatusTooManyRequests, quotaBody, map[string]string{
		"x-codex-plan-type":                   "plus",
		"x-codex-primary-used-percent":        "100",
		"x-codex-primary-window-minutes":      "10080",
		"x-codex-primary-reset-at":            "1785326082",
		"x-codex-secondary-window-minutes":    "0",
		"x-codex-primary-reset-after-seconds": "376150",
	})
	client.MaxRetries = 3

	_, err := client.Complete(context.Background(), api.Request{Input: "go"})
	var quota *QuotaError
	if !errors.As(err, &quota) {
		t.Fatalf("error = %v (%T), want a *QuotaError", err, err)
	}
	if quota.PlanType != "plus" {
		t.Errorf("plan = %q, want plus", quota.PlanType)
	}
	if got := quota.ResetsAt.Unix(); got != 1785326082 {
		t.Errorf("resets_at = %d, want 1785326082", got)
	}

	limits := client.Limits()
	if limits.Primary == nil {
		t.Fatal("primary window missing; the headers ride along on the 429")
	}
	if limits.Primary.UsedPercent != 100 || limits.Primary.WindowMinutes != 10080 {
		t.Errorf("primary = %+v", limits.Primary)
	}
	// A secondary window of zero minutes means the account does not run one.
	if limits.Secondary != nil {
		t.Errorf("secondary = %+v, want nil for a zero-minute window", limits.Secondary)
	}
}

// A quota wall must not be retried inside the request. The window is days wide,
// so the caller decides whether to sleep or change route.
func TestQuotaErrorIsNotRetried(t *testing.T) {
	t.Parallel()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(quotaBody))
	}))
	defer server.Close()
	client := &Client{Auth: liveAuth(t), URL: server.URL, MaxRetries: 3,
		Sleep: func(context.Context, time.Duration) error { return nil }}

	if _, err := client.Complete(context.Background(), api.Request{Input: "go"}); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1", calls)
	}
}

// An ordinary 429 with no usage_limit_reached is a transient limiter, and that
// one is worth retrying.
func TestPlainRateLimitIsRetried(t *testing.T) {
	t.Parallel()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream(`{"type":"response.output_text.delta","delta":"ok"}`, completedEvent)))
	}))
	defer server.Close()
	var slept time.Duration
	client := &Client{Auth: liveAuth(t), URL: server.URL, MaxRetries: 2,
		Sleep: func(_ context.Context, d time.Duration) error { slept += d; return nil }}

	response, err := client.Complete(context.Background(), api.Request{Input: "go"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "ok" || calls != 2 {
		t.Errorf("text = %q after %d calls", response.Text, calls)
	}
	if slept != time.Second {
		t.Errorf("slept %s, want the 1s Retry-After", slept)
	}
}

// A 400 naming the model is a slug problem, not a quota problem. Misreading
// those as entitlement failures is what made an earlier probe round look like
// the plan was locked out.
func TestUnknownModelIsNotAQuotaError(t *testing.T) {
	t.Parallel()
	body := `{"error":{"type":"invalid_request_error","message":"The model ` +
		`gpt-5.6-sol is not supported when using Codex with a ChatGPT account"}}`
	client := codexServer(t, http.StatusBadRequest, body, nil)

	_, err := client.Complete(context.Background(), api.Request{Model: "gpt-5.6-sol", Input: "go"})
	var quota *QuotaError
	if errors.As(err, &quota) {
		t.Fatal("a model rejection was classified as a quota wall")
	}
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v, want the upstream message", err)
	}
}

func TestEmptyInputIsRejectedWithoutACall(t *testing.T) {
	t.Parallel()
	client := &Client{Auth: liveAuth(t), URL: "http://127.0.0.1:1"}
	if _, err := client.Complete(context.Background(), api.Request{Input: "  "}); err == nil {
		t.Fatal("expected an error for empty input")
	}
}
