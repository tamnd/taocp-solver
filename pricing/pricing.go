// Package pricing calculates standard API list cost from provider-reported
// token usage. It does not include Batch, Priority, tool-call, or negotiated
// pricing.
package pricing

import (
	"strings"

	"github.com/tamnd/taocp-solver/api"
)

const (
	SourceURL            = "https://developers.openai.com/api/docs/models/compare"
	ZenSourceURL         = "https://opencode.ai/docs/zen"
	Hy3SourceURL         = "https://cloud.tencent.com/document/product/1759/127342"
	LongContextThreshold = 272000
	tokensPerMillion     = 1000000.0
)

type Rates struct {
	Model                 string  `json:"model"`
	InputPerMillion       float64 `json:"input_per_million_usd"`
	CachedInputPerMillion float64 `json:"cached_input_per_million_usd"`
	CacheWritePerMillion  float64 `json:"cache_write_per_million_usd"`
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
	PromotionEnds             string  `json:"promotion_ends,omitempty"`
	PostPromotionInput        float64 `json:"post_promotion_input_per_million,omitempty"`
	PostPromotionCachedInput  float64 `json:"post_promotion_cached_input_per_million,omitempty"`
	PostPromotionOutput       float64 `json:"post_promotion_output_per_million,omitempty"`
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
	"gpt-5.6-sol":   rates("gpt-5.6-sol", 5, 0.5, 30),
	"gpt-5.6-terra": rates("gpt-5.6-terra", 2.5, 0.25, 15),
	"gpt-5.6-luna":  rates("gpt-5.6-luna", 1, 0.1, 6),
	"gpt-5.5":       rates("gpt-5.5", 5, 0.5, 30),
	"gpt-5.4":       rates("gpt-5.4", 2.5, 0.25, 15),
	"gpt-5.4-mini":  standardRates("gpt-5.4-mini", 0.75, 0.075, 4.5),
}

var published = map[string]ListPrice{
	"deepseek-v4-flash-free": zenFree("deepseek-v4-flash-free"),
	"mimo-v2.5-free":         zenFree("mimo-v2.5-free"),
	"nemotron-3-ultra-free":  zenFree("nemotron-3-ultra-free"),
	"north-mini-code-free":   zenFree("north-mini-code-free"),
	"hy3-free": {
		Available: true, Provider: "Tencent Cloud", Model: "Hy3", Currency: "CNY",
		CachedInputPriceAvailable: true, Source: Hy3SourceURL, PromotionEnds: "2026-07-22",
		PostPromotionInput: 1, PostPromotionCachedInput: 0.25, PostPromotionOutput: 4,
		Note: "Tencent Cloud upstream price. OpenCode Zen does not publish hy3-free in its price table.",
	},
}

func zenFree(model string) ListPrice {
	return ListPrice{
		Available: true, Provider: "OpenCode Zen", Model: model, Currency: "USD",
		CachedInputPriceAvailable: true, Source: ZenSourceURL,
		Note: "Limited-time free route. Zen does not publish a cache-write price.",
	}
}

// PublishedListPrice returns the current provider-published rate card. OpenAI
// entries use standard API prices. Free Zen entries remain available zero-rate
// cards, which is distinct from an unavailable price.
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
			CacheWritePerMillion: value.CacheWritePerMillion, CacheWritePriceAvailable: true,
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
		CacheWritePerMillion: input * 1.25, OutputPerMillion: output,
		LongInputMultiplier: 2, LongOutputMultiplier: 1.5,
		LongContextThreshold: LongContextThreshold,
	}
}

// Lookup returns official standard rates for supported GPT models. The
// unsuffixed gpt-5.6 alias is priced as GPT-5.6 Sol.
func Lookup(model string) (Rates, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "gpt-5.6" {
		model = "gpt-5.6-sol"
	}
	for name, value := range official {
		if model == name || strings.HasPrefix(model, name+"-") {
			return value, true
		}
	}
	return Rates{}, false
}

func Calculate(model string, usage api.Usage) Cost {
	if card, ok := published[strings.ToLower(strings.TrimSpace(model))]; ok && card.Currency == "USD" {
		return Cost{Available: true, Currency: "USD", PricingModel: card.Model, Source: card.Source}
	}
	rates, ok := Lookup(model)
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
		Source: SourceURL, LongContext: long,
		UncachedInputUSD: dollars(usage.UncachedInputTokens(), rates.InputPerMillion*inputMultiplier),
		CachedInputUSD:   dollars(usage.CachedInputTokens, rates.CachedInputPerMillion*inputMultiplier),
		CacheWriteUSD:    dollars(usage.CacheWriteTokens, rates.CacheWritePerMillion*inputMultiplier),
		OutputUSD:        dollars(usage.OutputTokens, rates.OutputPerMillion*outputMultiplier),
	}
	cost.TotalUSD = cost.UncachedInputUSD + cost.CachedInputUSD + cost.CacheWriteUSD + cost.OutputUSD
	return cost
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
