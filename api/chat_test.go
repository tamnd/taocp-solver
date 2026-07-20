package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatClientStream(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `data: {"id":"chat_1","model":"gpt-5.6-sol","choices":[{"delta":{"content":"rigorous "}}]}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"solution"}}]}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, `data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":2},"completion_tokens_details":{"reasoning_tokens":3}}}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()
	client := &ChatClient{URL: server.URL + "/v1/chat/completions", HTTPClient: server.Client()}
	response, err := client.Complete(context.Background(), Request{Model: "gpt-5.6-sol", Instructions: "system", Input: "problem"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "rigorous solution" || response.InputTokens != 12 || response.OutputTokens != 5 {
		t.Fatalf("response = %+v", response)
	}
	if response.Usage.CachedInputTokens != 4 || response.Usage.CacheWriteTokens != 2 || response.Usage.ReasoningTokens != 3 || response.Usage.TotalTokens != 17 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}
