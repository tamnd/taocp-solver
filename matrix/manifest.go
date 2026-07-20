package matrix

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tamnd/taocp-solver/solver"
)

type Exercise struct {
	Section string `json:"section"`
	Number  int    `json:"number"`
	Level   int    `json:"level"`
	Focus   string `json:"focus"`
}

type ModelProfile struct {
	Name      string        `json:"name"`
	Provider  string        `json:"provider"`
	Model     string        `json:"model"`
	BaseURL   string        `json:"base_url"`
	APIKeyEnv string        `json:"api_key_env,omitempty"`
	Protocol  string        `json:"protocol"`
	CostBasis string        `json:"cost_basis"`
	Modes     []solver.Mode `json:"modes"`
}

type Manifest struct {
	Exercises []Exercise     `json:"exercises"`
	Models    []ModelProfile `json:"models"`
	Evaluator ModelProfile   `json:"evaluator"`
}

func LoadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var value Manifest
	if err := json.Unmarshal(raw, &value); err != nil {
		return Manifest{}, fmt.Errorf("decode matrix manifest: %w", err)
	}
	return value, value.Validate()
}

func (m Manifest) Validate() error {
	if len(m.Exercises) == 0 || len(m.Models) == 0 {
		return fmt.Errorf("matrix manifest requires exercises and models")
	}
	seen := map[string]bool{}
	profiles := append(append([]ModelProfile{}, m.Models...), m.Evaluator)
	for index, profile := range profiles {
		if strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.Model) == "" || strings.TrimSpace(profile.BaseURL) == "" {
			return fmt.Errorf("matrix model profile is incomplete: %+v", profile)
		}
		if index < len(m.Models) && seen[profile.Name] {
			return fmt.Errorf("duplicate matrix model name %q", profile.Name)
		}
		if index < len(m.Models) {
			seen[profile.Name] = true
		}
		if profile.Protocol != "chat" && profile.Protocol != "responses" {
			return fmt.Errorf("model %s: protocol must be chat or responses", profile.Name)
		}
		for _, mode := range profile.Modes {
			if mode != solver.ModeFast && mode != solver.ModeSlow {
				return fmt.Errorf("model %s: mode must be fast or slow", profile.Name)
			}
		}
	}
	for _, ex := range m.Exercises {
		if ex.Section == "" || ex.Number < 1 {
			return fmt.Errorf("invalid matrix exercise %+v", ex)
		}
	}
	return nil
}

func DefaultManifest() Manifest {
	zenURL := env("TAOCP_ZEN_PROXY_URL", "http://127.0.0.1:8788/v1")
	localURL := env("TAOCP_LOCAL_PROXY_URL", "http://127.0.0.1:8789/v1")
	bridgeURL := env("TAOCP_BRIDGE_URL", "http://127.0.0.1:8790/v1")
	fast := []solver.Mode{solver.ModeFast}
	profiles := []ModelProfile{
		profile("deepseek-v4-flash-free", "zen", "deepseek-v4-flash-free", zenURL, "OPENCODE_API_KEY", "free", fast),
		profile("mimo-v2.5-free", "zen", "mimo-v2.5-free", zenURL, "OPENCODE_API_KEY", "free", fast),
		profile("hy3-free", "zen", "hy3-free", zenURL, "OPENCODE_API_KEY", "free", fast),
		profile("nemotron-3-ultra-free", "zen", "nemotron-3-ultra-free", zenURL, "OPENCODE_API_KEY", "free", fast),
		profile("north-mini-code-free", "zen", "north-mini-code-free", zenURL, "OPENCODE_API_KEY", "free", fast),
	}
	for _, model := range []string{
		"gpt-oss:20b", "qwen2.5-coder:32b", "deepseek-v2:16b", "deepseek-coder-v2:16b",
		"deepseek-r1:32b", "qwen3:32b", "qwen3:30b-a3b", "qwen3.6:27b", "devstral:24b",
		"llama3.1:8b", "qwen3:8b", "qwen3-coder:30b",
	} {
		profiles = append(profiles, profile(model, "gamingpc", model, localURL, "", "local", fast))
	}
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"} {
		modes := fast
		if model == "gpt-5.6-sol" {
			modes = []solver.Mode{solver.ModeFast, solver.ModeSlow}
		}
		profiles = append(profiles, profile(model, "bridge", model, bridgeURL, "", "official-list", modes))
	}
	return Manifest{
		Exercises: []Exercise{
			{Section: "1.2.1", Number: 1, Level: 5, Focus: "direct induction"},
			{Section: "1.2.1", Number: 2, Level: 15, Focus: "find a proof flaw"},
			{Section: "1.2.1", Number: 8, Level: 25, Focus: "identity proof"},
			{Section: "1.2.1", Number: 11, Level: 30, Focus: "symbolic derivation"},
			{Section: "7.2.1.2", Number: 93, Level: 35, Focus: "algorithm and proof"},
		},
		Models:    profiles,
		Evaluator: profile("evaluator-gpt-5.6-sol", "bridge", "gpt-5.6-sol", bridgeURL, "", "official-list", nil),
	}
}

func profile(name, provider, model, baseURL, keyEnv, cost string, modes []solver.Mode) ModelProfile {
	return ModelProfile{Name: name, Provider: provider, Model: model, BaseURL: baseURL, APIKeyEnv: keyEnv, Protocol: "chat", CostBasis: cost, Modes: modes}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
