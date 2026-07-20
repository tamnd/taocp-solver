package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompleteResponsesAPI(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-5.6" || body["input"] != "problem" || body["store"] != false {
			t.Errorf("unexpected body: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","model":"gpt-5.6","output":[{"type":"message","content":[{"type":"output_text","text":"solution"}]}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18,"input_tokens_details":{"cached_tokens":4,"cache_write_tokens":2},"output_tokens_details":{"reasoning_tokens":3}}}`))
	}))
	defer server.Close()

	client := &Client{URL: server.URL + "/v1/responses", APIKey: "secret", HTTPClient: server.Client()}
	response, err := client.Complete(context.Background(), Request{Model: "gpt-5.6", Input: "problem", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "solution" || response.InputTokens != 11 || response.OutputTokens != 7 {
		t.Fatalf("response = %+v", response)
	}
	if response.Usage.CachedInputTokens != 4 || response.Usage.CacheWriteTokens != 2 || response.Usage.ReasoningTokens != 3 || response.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestCompleteRetriesTransientFailure(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":{"message":"busy"}}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"output_text":"ok"}`))
	}))
	defer server.Close()
	client := &Client{
		URL: server.URL, HTTPClient: server.Client(), MaxRetries: 1,
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	response, err := client.Complete(context.Background(), Request{Model: "test", Input: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "ok" || calls.Load() != 2 {
		t.Fatalf("response = %+v, calls = %d", response, calls.Load())
	}
}

func TestCompleteDoesNotRetryBadRequest(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"bad field"}}`, http.StatusBadRequest)
	}))
	defer server.Close()
	client := &Client{URL: server.URL, HTTPClient: server.Client(), MaxRetries: 3}
	_, err := client.Complete(context.Background(), Request{Model: "test", Input: "x"})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
}

func TestCompleteSkipsRetryBeyondDelayLimit(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var slept atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		http.Error(w, `{"error":{"message":"daily quota"}}`, http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := &Client{
		URL: server.URL, HTTPClient: server.Client(), MaxRetries: 4, MaxRetryDelay: time.Minute,
		Sleep: func(context.Context, time.Duration) error { slept.Store(true); return nil },
	}
	_, err := client.Complete(context.Background(), Request{Model: "test", Input: "x"})
	if err == nil || calls.Load() != 1 || slept.Load() {
		t.Fatalf("err = %v, calls = %d, slept = %t", err, calls.Load(), slept.Load())
	}
}
