package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bridgeOver stands a bridge in front of a fake Codex backend and returns the
// bridge's own test server plus the request bodies the backend saw.
func bridgeOver(t *testing.T, upstream http.HandlerFunc, apiKey string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = append(seen, string(raw))
		upstream(w, r)
	}))
	t.Cleanup(backend.Close)
	bridge := &Bridge{
		Client: &Client{Auth: liveAuth(t), URL: backend.URL},
		APIKey: apiKey,
	}
	front := httptest.NewServer(bridge.Handler())
	t.Cleanup(front.Close)
	return front, &seen
}

func okStream(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-codex-primary-used-percent", "12")
	w.Header().Set("x-codex-primary-window-minutes", "10080")
	_, _ = w.Write([]byte(stream(
		`{"type":"response.output_text.delta","delta":"Nicomachus holds."}`,
		completedEvent,
	)))
}

func post(t *testing.T, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestChatCompletionsTranslatesToResponses(t *testing.T) {
	t.Parallel()
	front, seen := bridgeOver(t, okStream, "")

	resp := post(t, front.URL+"/v1/chat/completions", `{
      "model": "gpt-5.6-luna",
      "messages": [
        {"role": "system", "content": "You are terse."},
        {"role": "user", "content": "Prove the cube-sum identity."}
      ]}`, nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %s: %s", resp.Status, raw)
	}

	// The system message becomes instructions and the user message becomes the
	// single input turn, which is what the Responses wire expects.
	var sent wireRequest
	if err := json.Unmarshal([]byte((*seen)[0]), &sent); err != nil {
		t.Fatalf("upstream body does not parse: %v", err)
	}
	if sent.Instructions != "You are terse." {
		t.Errorf("instructions = %q", sent.Instructions)
	}
	if len(sent.Input) != 1 || sent.Input[0].Content[0].Text != "Prove the cube-sum identity." {
		t.Errorf("input = %+v", sent.Input)
	}
	if !sent.Stream || sent.Store {
		t.Errorf("stream = %v, store = %v, want true and false", sent.Stream, sent.Store)
	}

	var body struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			PromptDetails    struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "chat.completion" || len(body.Choices) != 1 {
		t.Fatalf("body = %+v", body)
	}
	if body.Choices[0].Message.Content != "Nicomachus holds." {
		t.Errorf("content = %q", body.Choices[0].Message.Content)
	}
	if body.Choices[0].Message.Role != "assistant" || body.Choices[0].FinishReason != "stop" {
		t.Errorf("choice = %+v", body.Choices[0])
	}
	// Usage has to survive the translation or every cost figure downstream is
	// invented rather than measured.
	if body.Usage.PromptTokens != 120 || body.Usage.CompletionTokens != 80 ||
		body.Usage.TotalTokens != 200 {
		t.Errorf("usage = %+v", body.Usage)
	}
	if body.Usage.PromptDetails.CachedTokens != 40 {
		t.Errorf("cached tokens = %d, want 40", body.Usage.PromptDetails.CachedTokens)
	}
	if body.Usage.CompletionDetails.ReasoningTokens != 30 {
		t.Errorf("reasoning tokens = %d, want 30", body.Usage.CompletionDetails.ReasoningTokens)
	}
}

func TestChatCompletionsStreamShape(t *testing.T) {
	t.Parallel()
	front, _ := bridgeOver(t, okStream, "")

	resp := post(t, front.URL+"/v1/chat/completions",
		`{"messages":[{"role":"user","content":"go"}],"stream":true}`, nil)
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Errorf("content type = %q", got)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Errorf("stream does not end with [DONE]:\n%s", body)
	}

	var text strings.Builder
	var sawStop bool
	var usageTotal int
	for _, line := range strings.Split(body, "\n") {
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if !strings.HasPrefix(line, "data:") || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("chunk %q does not parse: %v", data, err)
		}
		if chunk.Object != "chat.completion.chunk" {
			t.Errorf("object = %q", chunk.Object)
		}
		for _, choice := range chunk.Choices {
			text.WriteString(choice.Delta.Content)
			if choice.FinishReason != nil && *choice.FinishReason == "stop" {
				sawStop = true
			}
		}
		if chunk.Usage != nil {
			usageTotal = chunk.Usage.TotalTokens
		}
	}
	if text.String() != "Nicomachus holds." {
		t.Errorf("assembled text = %q", text.String())
	}
	if !sawStop {
		t.Error("no chunk carried finish_reason stop")
	}
	if usageTotal != 200 {
		t.Errorf("usage total = %d, want 200", usageTotal)
	}
}

// Content sent as an array of parts is as common as a plain string, and a
// bridge that only reads one of the two silently drops the prompt.
func TestChatAcceptsContentParts(t *testing.T) {
	t.Parallel()
	front, seen := bridgeOver(t, okStream, "")

	resp := post(t, front.URL+"/v1/chat/completions", `{"messages":[
      {"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}]}`, nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %s: %s", resp.Status, raw)
	}
	if !strings.Contains((*seen)[0], "first") || !strings.Contains((*seen)[0], "second") {
		t.Errorf("upstream input lost a content part: %s", (*seen)[0])
	}
}

func TestChatWithNoUserMessageIsRejected(t *testing.T) {
	t.Parallel()
	front, _ := bridgeOver(t, okStream, "")
	resp := post(t, front.URL+"/v1/chat/completions",
		`{"messages":[{"role":"system","content":"only instructions"}]}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %s, want 400", resp.Status)
	}
}

func TestResponsesPassesThroughAndForwardsLimitHeaders(t *testing.T) {
	t.Parallel()
	front, seen := bridgeOver(t, okStream, "")

	body := `{"model":"gpt-5.6-luna","input":[{"type":"message","role":"user",` +
		`"content":[{"type":"input_text","text":"straight through"}]}],"stream":true}`
	resp := post(t, front.URL+"/v1/responses", body, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", resp.Status)
	}
	// Passthrough means byte-for-byte, so a Codex CLI request keeps whatever
	// fields this package has never heard of.
	if (*seen)[0] != body {
		t.Errorf("upstream body was rewritten:\n got %s\nwant %s", (*seen)[0], body)
	}
	if got := resp.Header.Get("x-codex-primary-used-percent"); got != "12" {
		t.Errorf("limit header = %q, want it forwarded", got)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "response.completed") {
		t.Errorf("stream body was not relayed: %s", raw)
	}
}

func TestAPIKeyIsEnforcedExceptOnHealth(t *testing.T) {
	t.Parallel()
	front, _ := bridgeOver(t, okStream, "sk-local")

	resp := post(t, front.URL+"/v1/chat/completions",
		`{"messages":[{"role":"user","content":"go"}]}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a key: status = %s, want 401", resp.Status)
	}
	resp = post(t, front.URL+"/v1/chat/completions",
		`{"messages":[{"role":"user","content":"go"}]}`,
		map[string]string{"Authorization": "Bearer sk-wrong"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("with a wrong key: status = %s, want 401", resp.Status)
	}
	resp = post(t, front.URL+"/v1/chat/completions",
		`{"messages":[{"role":"user","content":"go"}]}`,
		map[string]string{"Authorization": "Bearer sk-local"})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("with the right key: status = %s, want 200", resp.Status)
	}
	// A monitor should be able to see the bridge is alive without the key.
	health, err := http.Get(front.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = health.Body.Close() }()
	if health.StatusCode != http.StatusOK {
		t.Errorf("health without a key: status = %s, want 200", health.Status)
	}
}

func TestHealthReportsPlanAndQuota(t *testing.T) {
	t.Parallel()
	front, _ := bridgeOver(t, okStream, "")
	// Health knows nothing about quota until a request has been made, because
	// the numbers only exist as headers on a real response.
	post(t, front.URL+"/v1/chat/completions", `{"messages":[{"role":"user","content":"go"}]}`, nil)

	resp, err := http.Get(front.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var health Health
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if !health.OK {
		t.Errorf("health = %+v, want ok", health)
	}
	if health.PlanType != "plus" || health.AccountID != "acct-1" {
		t.Errorf("plan = %q, account = %q", health.PlanType, health.AccountID)
	}
	if health.TokenExpiry.IsZero() {
		t.Error("token expiry missing")
	}
	if health.Limits.Primary == nil || health.Limits.Primary.UsedPercent != 12 {
		t.Errorf("limits = %+v", health.Limits)
	}
	if !strings.Contains(health.Quota, "12%") {
		t.Errorf("quota summary = %q", health.Quota)
	}
}

func TestHealthReportsTheLastFailure(t *testing.T) {
	t.Parallel()
	front, _ := bridgeOver(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(quotaBody))
	}, "")

	resp := post(t, front.URL+"/v1/chat/completions",
		`{"messages":[{"role":"user","content":"go"}]}`, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %s, want 429 for an exhausted plan", resp.Status)
	}
	// A client has to be able to tell "come back later" from "misconfigured".
	if resp.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After on a quota rejection")
	}
	var body struct {
		Error struct {
			Type     string `json:"type"`
			ResetsAt int64  `json:"resets_at"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != "usage_limit_reached" {
		t.Errorf("error type = %q", body.Error.Type)
	}
	if body.Error.ResetsAt != 1785326082 {
		t.Errorf("resets_at = %d, want 1785326082", body.Error.ResetsAt)
	}

	health, err := http.Get(front.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = health.Body.Close() }()
	var decoded Health
	if err := json.NewDecoder(health.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded.LastError, "usage limit") {
		t.Errorf("last error = %q", decoded.LastError)
	}
}

func TestModelsListsTheSlugsThatWork(t *testing.T) {
	t.Parallel()
	front, _ := bridgeOver(t, okStream, "")
	resp, err := http.Get(front.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != len(Models) {
		t.Fatalf("listed %d models, want %d", len(body.Data), len(Models))
	}
	found := map[string]bool{}
	for _, item := range body.Data {
		found[item.ID] = true
	}
	if !found[DefaultModel] {
		t.Errorf("the default model %q is not in the list", DefaultModel)
	}
	// gpt-5.6-sol is rejected for ChatGPT-account auth on every plan, so
	// advertising it would send every client into a 400.
	if found["gpt-5.6-sol"] {
		t.Error("gpt-5.6-sol is advertised but is not usable with this credential")
	}
}

func TestNonLoopbackHostRequiresAKey(t *testing.T) {
	t.Parallel()
	bridge := &Bridge{Client: &Client{Auth: liveAuth(t)}}
	err := bridge.Serve(context.Background(), "0.0.0.0", 0, nil)
	if err == nil {
		t.Fatal("a bridge open to the network without a key must refuse to start")
	}
	if !strings.Contains(err.Error(), "api-key") {
		t.Errorf("error = %q, want it to name the missing flag", err)
	}
}

func TestServeStopsWithTheContext(t *testing.T) {
	t.Parallel()
	bridge := &Bridge{Client: &Client{Auth: liveAuth(t)}}
	ctx, cancel := context.WithCancel(context.Background())
	addresses := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- bridge.Serve(ctx, "127.0.0.1", 0, func(a string) { addresses <- a }) }()

	select {
	case <-addresses:
	case <-time.After(5 * time.Second):
		t.Fatal("bridge never reported a listening address")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after cancellation")
	}
}
