// Package pricing calculates standard API list cost from provider-reported
// token usage. It does not include Batch, Priority, tool-call, or negotiated
// pricing.
package pricing

import (
	"strings"

	"github.com/tamnd/taocp-solver/api"
)

const (
	SourceURL            = "https://developers.openai.com/api/docs/pricing"
	ZenSourceURL         = "https://opencode.ai/docs/zen"
	Hy3SourceURL         = "https://cloud.tencent.com/document/product/1759/127342"
	DeepSeekSourceURL    = "https://api-docs.deepseek.com/quick_start/pricing"
	MiMoSourceURL        = "https://mimo.mi.com/docs/en-US/pricing"
	CohereNorthSourceURL = "https://docs.cohere.com/docs/north-mini-code-1.0"
	OpenRouterSourceURL  = "https://openrouter.ai/api/v1/models"
	LongContextThreshold = 272000
	tokensPerMillion     = 1000000.0
)

type Rates struct {
	Model                 string  `json:"model"`
	InputPerMillion       float64 `json:"input_per_million_usd"`
	CachedInputPerMillion float64 `json:"cached_input_per_million_usd"`
	CacheWritePerMillion  float64 `json:"cache_write_per_million_usd"`
	CacheWriteAvailable   bool    `json:"cache_write_price_available"`
	OutputPerMillion      float64 `json:"output_per_million_usd"`
	LongInputMultiplier   float64 `json:"long_input_multiplier"`
	LongOutputMultiplier  float64 `json:"long_output_multiplier"`
	LongContextThreshold  int     `json:"long_context_threshold"`
}

// ListPrice records a provider's published per-million-token prices in the
// original billing currency. It can describe a free route without treating a
// missing price as zero.
type ListPrice struct {
	Available                 bool    `json:"available"`
	Provider                  string  `json:"provider"`
	Model                     string  `json:"model"`
	Currency                  string  `json:"currency"`
	InputPerMillion           float64 `json:"input_per_million"`
	CachedInputPerMillion     float64 `json:"cached_input_per_million"`
	CachedInputPriceAvailable bool    `json:"cached_input_price_available"`
	CacheWritePerMillion      float64 `json:"cache_write_per_million"`
	CacheWritePriceAvailable  bool    `json:"cache_write_price_available"`
	OutputPerMillion          float64 `json:"output_per_million"`
	Source                    string  `json:"source"`
	Note                      string  `json:"note,omitempty"`
}

type Cost struct {
	Available        bool    `json:"available"`
	Currency         string  `json:"currency"`
	PricingModel     string  `json:"pricing_model,omitempty"`
	Source           string  `json:"source,omitempty"`
	LongContext      bool    `json:"long_context"`
	UncachedInputUSD float64 `json:"uncached_input_usd"`
	CachedInputUSD   float64 `json:"cached_input_usd"`
	CacheWriteUSD    float64 `json:"cache_write_usd"`
	OutputUSD        float64 `json:"output_usd"`
	TotalUSD         float64 `json:"total_usd"`
}

var official = map[string]Rates{
	"gpt-5.6-sol":   ratesWithCacheWrite("gpt-5.6-sol", 5, 0.5, 30),
	"gpt-5.6-terra": ratesWithCacheWrite("gpt-5.6-terra", 2.5, 0.25, 15),
	"gpt-5.6-luna":  ratesWithCacheWrite("gpt-5.6-luna", 1, 0.1, 6),
	"gpt-5.5":       rates("gpt-5.5", 5, 0.5, 30),
	"gpt-5.4":       rates("gpt-5.4", 2.5, 0.25, 15),
	"gpt-5.4-mini":  standardRates("gpt-5.4-mini", 0.75, 0.075, 4.5),
}

var published = map[string]ListPrice{
	"deepseek-v4-flash-free": paidCard("DeepSeek", "DeepSeek-V4-Flash", 0.14, 0.0028, 0.28, DeepSeekSourceURL, "Direct paid API rate used to value the Zen execution."),
	"mimo-v2.5-free":         paidCard("Xiaomi", "MiMo-V2.5", 0.14, 0.0028, 0.28, MiMoSourceURL, "Direct paid API rate used to value the Zen execution."),
	"nemotron-3-ultra-free":  paidCard("OpenRouter", "NVIDIA Nemotron 3 Ultra", 0.60, 0.20, 3.60, OpenRouterSourceURL, "OpenRouter paid-route list rate used because NVIDIA prices production NIM by GPU, not by token."),
	"north-mini-code-free": {
		Provider: "Cohere", Model: "North Mini Code", Currency: "USD", Source: CohereNorthSourceURL,
		Note: "Cohere publishes this model as free for trial and production keys. No paid per-token list rate is published.",
	},
	"hy3-free": {
		Available: true, Provider: "OpenRouter", Model: "Tencent Hy3", Currency: "USD",
		InputPerMillion: 0.20, CachedInputPerMillion: 0.05, CachedInputPriceAvailable: true,
		OutputPerMillion: 0.80, Source: OpenRouterSourceURL,
		Note: "Paid OpenRouter route used to value the Zen execution. Tencent's direct rate is published in CNY.",
	},
}

var paidEquivalent = map[string]Rates{
	"deepseek-v4-flash-free": standardRates("DeepSeek-V4-Flash", 0.14, 0.0028, 0.28),
	"mimo-v2.5-free":         standardRates("MiMo-V2.5", 0.14, 0.0028, 0.28),
	"nemotron-3-ultra-free":  standardRates("NVIDIA Nemotron 3 Ultra", 0.60, 0.20, 3.60),
	"hy3-free":               standardRates("Tencent Hy3", 0.20, 0.05, 0.80),
}

func paidCard(provider, model string, input, cached, output float64, source, note string) ListPrice {
	return ListPrice{
		Available: true, Provider: provider, Model: model, Currency: "USD",
		InputPerMillion: input, CachedInputPerMillion: cached, CachedInputPriceAvailable: true,
		OutputPerMillion: output, Source: source, Note: note,
	}
}

// PublishedListPrice returns the current provider-published rate card. OpenAI
// entries use standard API prices. Zen aliases use paid-equivalent rates for
// the same underlying model.
func PublishedListPrice(model string) (ListPrice, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "gpt-5.6" {
		model = "gpt-5.6-sol"
	}
	if value, ok := published[model]; ok {
		return value, true
	}
	if value, ok := Lookup(model); ok {
		return ListPrice{
			Available: true, Provider: "OpenAI", Model: value.Model, Currency: "USD",
			InputPerMillion:       value.InputPerMillion,
			CachedInputPerMillion: value.CachedInputPerMillion, CachedInputPriceAvailable: true,
			CacheWritePerMillion: value.CacheWritePerMillion, CacheWritePriceAvailable: value.CacheWriteAvailable,
			OutputPerMillion: value.OutputPerMillion, Source: SourceURL,
		}, true
	}
	return ListPrice{}, false
}

func standardRates(model string, input, cached, output float64) Rates {
	value := rates(model, input, cached, output)
	value.LongInputMultiplier = 1
	value.LongOutputMultiplier = 1
	value.LongContextThreshold = 0
	return value
}

func rates(model string, input, cached, output float64) Rates {
	return Rates{
		Model: model, InputPerMillion: input, CachedInputPerMillion: cached,
		OutputPerMillion:    output,
		LongInputMultiplier: 2, LongOutputMultiplier: 1.5,
		LongContextThreshold: LongContextThreshold,
	}
}

func ratesWithCacheWrite(model string, input, cached, output float64) Rates {
	value := rates(model, input, cached, output)
	value.CacheWritePerMillion = input * 1.25
	value.CacheWriteAvailable = true
	return value
}

// Lookup returns official standard rates for supported GPT models. The
// unsuffixed gpt-5.6 alias is priced as GPT-5.6 Sol.
func Lookup(model string) (Rates, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "gpt-5.6" {
		model = "gpt-5.6-sol"
	}
	if value, ok := official[model]; ok {
		return value, true
	}
	var match Rates
	matchedLength := 0
	for name, value := range official {
		if strings.HasPrefix(model, name+"-") && len(name) > matchedLength {
			match = value
			matchedLength = len(name)
		}
	}
	return match, matchedLength > 0
}

func Calculate(model string, usage api.Usage) Cost {
	key := strings.ToLower(strings.TrimSpace(model))
	rates, ok := paidEquivalent[key]
	if !ok {
		rates, ok = Lookup(model)
	}
	if !ok {
		return Cost{Currency: "USD"}
	}
	usage = usage.Normalized()
	inputMultiplier := 1.0
	outputMultiplier := 1.0
	long := rates.LongContextThreshold > 0 && usage.InputTokens > rates.LongContextThreshold
	if long {
		inputMultiplier = rates.LongInputMultiplier
		outputMultiplier = rates.LongOutputMultiplier
	}
	cost := Cost{
		Available: true, Currency: "USD", PricingModel: rates.Model,
		Source: sourceForModel(key), LongContext: long,
		UncachedInputUSD: dollars(usage.UncachedInputTokens(), rates.InputPerMillion*inputMultiplier),
		CachedInputUSD:   dollars(usage.CachedInputTokens, rates.CachedInputPerMillion*inputMultiplier),
		CacheWriteUSD:    dollars(usage.CacheWriteTokens, rates.CacheWritePerMillion*inputMultiplier),
		OutputUSD:        dollars(usage.OutputTokens, rates.OutputPerMillion*outputMultiplier),
	}
	cost.TotalUSD = cost.UncachedInputUSD + cost.CachedInputUSD + cost.CacheWriteUSD + cost.OutputUSD
	return cost
}

func sourceForModel(model string) string {
	if card, ok := published[model]; ok {
		return card.Source
	}
	return SourceURL
}

func dollars(tokens int, perMillion float64) float64 {
	return float64(tokens) * perMillion / tokensPerMillion
}

func Add(left, right Cost) Cost {
	if !left.Available {
		return right
	}
	if !right.Available {
		return left
	}
	left.UncachedInputUSD += right.UncachedInputUSD
	left.CachedInputUSD += right.CachedInputUSD
	left.CacheWriteUSD += right.CacheWriteUSD
	left.OutputUSD += right.OutputUSD
	left.TotalUSD += right.TotalUSD
	left.LongContext = left.LongContext || right.LongContext
	if left.PricingModel != right.PricingModel {
		left.PricingModel = "mixed"
	}
	return left
}
