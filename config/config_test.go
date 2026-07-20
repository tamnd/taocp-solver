package config

import "testing"

func TestResponsesURL(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"https://bridge.example":    "https://bridge.example/v1/responses",
		"https://proxy.example/v1/": "https://proxy.example/v1/responses",
	}
	for base, want := range tests {
		cfg := Config{BaseURL: base}
		if got := cfg.ResponsesURL(); got != want {
			t.Errorf("ResponsesURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestChatCompletionsURL(t *testing.T) {
	t.Parallel()
	if got := (Config{BaseURL: "http://localhost:8790/v1"}).ChatCompletionsURL(); got != "http://localhost:8790/v1/chat/completions" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeCandidateBounds(t *testing.T) {
	t.Parallel()
	cfg := Config{Candidates: 99, Timeout: 1}
	cfg.Normalize()
	if cfg.Candidates != 5 {
		t.Fatalf("candidates = %d", cfg.Candidates)
	}
	cfg.Candidates = 0
	cfg.Normalize()
	if cfg.Candidates != 1 {
		t.Fatalf("candidates = %d", cfg.Candidates)
	}
}
