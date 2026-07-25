package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/taocp-solver/api"
)

// Bridge serves an OpenAI-compatible surface in front of one Codex credential.
//
// It exists because the documented reproduction commands in this repo point a
// solver at a local port. Three ways to use one credential fall out of it: in
// process through Client, over the port for any OpenAI-compatible client, and
// as a drop-in target for the Codex CLI itself through /v1/responses.
type Bridge struct {
	Client *Client
	APIKey string
	Model  string
	Logf   func(format string, args ...any)

	mu        sync.Mutex
	lastError string
	lastAt    time.Time
}

func (b *Bridge) model() string {
	if strings.TrimSpace(b.Model) != "" {
		return b.Model
	}
	return DefaultModel
}

func (b *Bridge) client() *Client {
	if b.Client == nil {
		b.Client = &Client{}
	}
	return b.Client
}

func (b *Bridge) logf(format string, args ...any) {
	if b.Logf != nil {
		b.Logf(format, args...)
	}
}

func (b *Bridge) noteError(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	b.lastError = err.Error()
	b.lastAt = time.Now()
	b.mu.Unlock()
}

// Handler is the bridge's HTTP surface.
func (b *Bridge) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", b.guard(b.handleChat))
	mux.HandleFunc("POST /v1/responses", b.guard(b.handleResponses))
	mux.HandleFunc("GET /v1/models", b.guard(b.handleModels))
	mux.HandleFunc("GET /v1/health", b.handleHealth)
	return mux
}

// guard enforces the API key when one is configured. Health is deliberately
// outside it so a monitor can see the bridge without holding the key.
func (b *Bridge) guard(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if b.APIKey == "" {
			next(w, r)
			return
		}
		presented := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if presented != b.APIKey {
			writeError(w, http.StatusUnauthorized, "invalid api key")
			return
		}
		next(w, r)
	}
}

type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Stream          bool   `json:"stream"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// flatten turns chat messages into the instructions plus input pair the
// Responses wire wants. System and developer messages become instructions;
// everything else is the input, labelled when there is more than one turn so
// the model can still see who said what.
func (c chatRequest) flatten() (instructions, input string) {
	var system, turns []string
	for _, message := range c.Messages {
		text := messageText(message.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		switch message.Role {
		case "system", "developer":
			system = append(system, text)
		default:
			turns = append(turns, text)
		}
	}
	if len(turns) == 1 {
		return strings.Join(system, "\n\n"), turns[0]
	}
	var labelled []string
	for _, message := range c.Messages {
		text := messageText(message.Content)
		if strings.TrimSpace(text) == "" || message.Role == "system" || message.Role == "developer" {
			continue
		}
		role := message.Role
		if role == "" {
			role = "user"
		}
		labelled = append(labelled, role+": "+text)
	}
	return strings.Join(system, "\n\n"), strings.Join(labelled, "\n\n")
}

// messageText accepts both a plain string and the content-part array form,
// because clients disagree about which one they send.
func messageText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, part := range parts {
		if part.Text != "" {
			out = append(out, part.Text)
		}
	}
	return strings.Join(out, "\n")
}

func (b *Bridge) handleChat(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read request: "+err.Error())
		return
	}
	var request chatRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		writeError(w, http.StatusBadRequest, "decode request: "+err.Error())
		return
	}
	instructions, input := request.flatten()
	if strings.TrimSpace(input) == "" {
		writeError(w, http.StatusBadRequest, "no user message in request")
		return
	}
	model := strings.TrimSpace(request.Model)
	if model == "" || model == "default" {
		model = b.model()
	}

	response, err := b.client().Complete(r.Context(), api.Request{
		Model:        model,
		Instructions: instructions,
		Input:        input,
		Effort:       request.ReasoningEffort,
	})
	if err != nil {
		b.noteError(err)
		b.logf("chat completion failed: %v", err)
		writeUpstreamError(w, err)
		return
	}
	if request.Stream {
		writeChatStream(w, response, model)
		return
	}
	writeJSON(w, http.StatusOK, chatBody(response, model, "stop"))
}

// handleResponses carries a Codex CLI request straight through, so pointing the
// CLI at this bridge works without translating anything but the credential.
func (b *Bridge) handleResponses(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read request: "+err.Error())
		return
	}
	upstream, err := b.client().Raw(r.Context(), raw)
	if err != nil {
		b.noteError(err)
		writeUpstreamError(w, err)
		return
	}
	defer func() { _ = upstream.Body.Close() }()

	for _, name := range []string{"Content-Type", "Cache-Control"} {
		if value := upstream.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	// The rate-limit headers are the only place quota state is published, so
	// they are passed on rather than swallowed.
	for name, values := range upstream.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-codex-") {
			w.Header()[name] = values
		}
	}
	w.WriteHeader(upstream.StatusCode)
	flushCopy(w, upstream.Body)
}

func (b *Bridge) handleModels(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	data := make([]map[string]any, 0, len(Models))
	for _, model := range Models {
		data = append(data, map[string]any{
			"id": model, "object": "model", "created": now, "owned_by": "openai",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// Health is what /v1/health reports.
type Health struct {
	OK          bool      `json:"ok"`
	PlanType    string    `json:"plan_type,omitempty"`
	AccountID   string    `json:"account_id,omitempty"`
	TokenExpiry time.Time `json:"token_expires_at,omitzero"`
	Limits      Limits    `json:"limits,omitzero"`
	Quota       string    `json:"quota,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
	Model       string    `json:"default_model"`
}

func (b *Bridge) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := Health{Model: b.model()}
	// Reading the credential must not refresh it: a monitor polling health
	// every few seconds should not drive token traffic.
	token := b.client().auth().Cached()
	if token.Access == "" {
		if fresh, err := b.client().auth().Token(r.Context()); err == nil {
			token = fresh
		} else {
			health.LastError = err.Error()
			writeJSON(w, http.StatusServiceUnavailable, health)
			return
		}
	}
	health.OK = !token.Expired(time.Now())
	health.PlanType = token.PlanType
	health.AccountID = token.AccountID
	health.TokenExpiry = token.ExpiresAt
	health.Limits = b.client().Limits()
	health.Quota = quotaSummary(health.Limits)

	b.mu.Lock()
	health.LastError, health.LastErrorAt = b.lastError, b.lastAt
	b.mu.Unlock()

	status := http.StatusOK
	if !health.OK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, health)
}

// quotaSummary is the short human answer to "can this thing work right now".
func quotaSummary(limits Limits) string {
	if limits.ReadAt.IsZero() {
		return "unknown until the first request"
	}
	var parts []string
	for name, window := range map[string]*Window{"primary": limits.Primary, "secondary": limits.Secondary} {
		if window == nil {
			continue
		}
		part := fmt.Sprintf("%s %.0f%% of %dm used", name, window.UsedPercent, window.WindowMinutes)
		if !window.ResetsAt.IsZero() {
			part += ", resets " + window.ResetsAt.UTC().Format(time.RFC3339)
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "no windows reported"
	}
	return strings.Join(parts, "; ")
}

// Serve runs the bridge until the context is cancelled.
//
// A key is optional on a loopback listener and required off it. A bridge that
// hands a subscription to the whole network without a key is not a convenience.
func (b *Bridge) Serve(ctx context.Context, host string, port int, ready func(string)) error {
	if host == "" {
		host = "127.0.0.1"
	}
	if !isLoopback(host) && b.APIKey == "" {
		return fmt.Errorf("--host %s is not loopback, so --api-key is required", host)
	}
	address := net.JoinHostPort(host, fmt.Sprint(port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           b.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}
	if ready != nil {
		ready(listener.Addr().String())
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	address, err := net.ResolveIPAddr("ip", host)
	if err != nil {
		return false
	}
	return address.IP.IsLoopback()
}

func chatBody(response api.Response, model, finish string) map[string]any {
	id := response.ID
	if id == "" {
		id = "chatcmpl-" + newUUID()
	}
	return map[string]any{
		"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": response.Text},
			"finish_reason": finish,
		}},
		"usage": chatUsage(response.Usage),
	}
}

func chatUsage(usage api.Usage) map[string]any {
	return map[string]any{
		"prompt_tokens":     usage.InputTokens,
		"completion_tokens": usage.OutputTokens,
		"total_tokens":      usage.TotalTokens,
		"prompt_tokens_details": map[string]any{
			"cached_tokens":      usage.CachedInputTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
		},
		"completion_tokens_details": map[string]any{
			"reasoning_tokens": usage.ReasoningTokens,
		},
	}
}

// writeChatStream sends the finished answer as a well-formed stream.
//
// The text arrives in one chunk rather than token by token. Upstream is already
// consumed by the time the answer is known, and a client that asked for a
// stream needs valid stream framing, not simulated typing.
func writeChatStream(w http.ResponseWriter, response api.Response, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	id := response.ID
	if id == "" {
		id = "chatcmpl-" + newUUID()
	}
	created := time.Now().Unix()
	chunk := func(delta map[string]any, finish any, usage map[string]any) {
		body := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		if usage != nil {
			body["usage"] = usage
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		flush(w)
	}
	chunk(map[string]any{"role": "assistant", "content": ""}, nil, nil)
	chunk(map[string]any{"content": response.Text}, nil, nil)
	chunk(map[string]any{}, "stop", chatUsage(response.Usage))
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flush(w)
}

// writeUpstreamError keeps an exhausted plan distinguishable from a broken one,
// so a client can tell "come back Wednesday" from "this is misconfigured".
func writeUpstreamError(w http.ResponseWriter, err error) {
	var quota *QuotaError
	if errors.As(err, &quota) {
		if !quota.ResetsAt.IsZero() {
			w.Header().Set("Retry-After", fmt.Sprint(int(quota.RetryAfter(time.Now()).Seconds())))
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]any{
			"type": "usage_limit_reached", "message": quota.Error(),
			"plan_type": quota.PlanType, "resets_at": quota.ResetsAt.Unix(),
		}})
		return
	}
	writeError(w, http.StatusBadGateway, err.Error())
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func flushCopy(w http.ResponseWriter, reader io.Reader) {
	buf := make([]byte, 8192)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			flush(w)
		}
		if err != nil {
			return
		}
	}
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
