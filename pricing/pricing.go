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
}

func rates(model string, input, cached, output float64) Rates {
	return Rates{
		Model: model, InputPerMillion: input, CachedInputPerMillion: cached,
		CacheWritePerMillion: input * 1.25, OutputPerMillion: output,
		LongInputMultiplier: 2, LongOutputMultiplier: 1.5,
		LongContextThreshold: LongContextThreshold,
	}
}

// Lookup returns the official standard rates for a GPT-5.6 model. The
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
	rates, ok := Lookup(model)
	if !ok {
		return Cost{Currency: "USD"}
	}
	usage = usage.Normalized()
	inputMultiplier := 1.0
	outputMultiplier := 1.0
	long := usage.InputTokens > rates.LongContextThreshold
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
