package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultModel          = "gpt-5.6"
	DefaultMaxCorrections = 2
)

// Config contains every external dependency of a solver run. Environment
// variables provide useful defaults, while command flags can override them.
type Config struct {
	BaseURL        string
	APIKey         string
	Model          string
	TAOCPRoot      string
	OutputRoot     string
	Timeout        time.Duration
	MaxCorrections int
	MaxRetries     int
	Parallel       int
}

func FromEnv() Config {
	home, _ := os.UserHomeDir()
	return Config{
		BaseURL:        firstEnv("TAOCP_SOLVER_BASE_URL", "OPENAI_BASE_URL"),
		APIKey:         firstEnv("TAOCP_SOLVER_API_KEY", "OPENAI_API_KEY"),
		Model:          env("TAOCP_SOLVER_MODEL", DefaultModel),
		TAOCPRoot:      env("TAOCP_SOLVER_SOURCE", filepath.Join(home, "github", "tamnd", "taocp")),
		OutputRoot:     env("TAOCP_SOLVER_OUTPUT", filepath.Join(home, "data", "taocp-solver")),
		Timeout:        envDuration("TAOCP_SOLVER_TIMEOUT", 30*time.Minute),
		MaxCorrections: envInt("TAOCP_SOLVER_MAX_CORRECTIONS", DefaultMaxCorrections),
		MaxRetries:     envInt("TAOCP_SOLVER_MAX_RETRIES", 4),
		Parallel:       envInt("TAOCP_SOLVER_PARALLEL", min(2, runtime.NumCPU())),
	}
}

func (c *Config) Normalize() {
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Minute
	}
	if c.MaxCorrections < 0 {
		c.MaxCorrections = 0
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.Parallel < 1 {
		c.Parallel = 1
	}
}

func (c Config) Validate(requireAPI bool) error {
	if c.TAOCPRoot == "" {
		return errors.New("source repository path is empty")
	}
	if requireAPI && c.BaseURL == "" {
		return errors.New("API base URL is required; set TAOCP_SOLVER_BASE_URL or pass --base-url")
	}
	if requireAPI && c.Model == "" {
		return errors.New("model is required")
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}

// ResponsesURL accepts either a server root or a base ending in /v1.
func (c Config) ResponsesURL() string {
	base := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/responses"
	}
	return base + "/v1/responses"
}

// ChatCompletionsURL is the bridge-compatible endpoint used by the solver.
// The local subscription bridge translates this request to the Responses wire.
func (c Config) ChatCompletionsURL() string {
	base := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return d
}

func ParseDuration(value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", value, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration must be positive: %q", value)
	}
	return d, nil
}
