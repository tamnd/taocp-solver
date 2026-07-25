package route

import (
	"fmt"
	"strings"
)

// Suggest builds a refreshed registry from what the routes actually advertise.
//
// It keeps the rank of every route that still exists, because those ranks
// encode measured evidence and regenerating them from a catalogue would throw
// that away. A route whose model has disappeared is disabled with the reason
// rather than deleted, and a model the catalogue offers that no route covers
// is appended disabled, so a person decides whether an unknown model gets to
// touch a published proof.
func Suggest(registry Registry, catalogues map[string][]string) Registry {
	out := Registry{Routes: make([]Route, 0, len(registry.Routes))}
	covered := map[string]bool{}
	for _, value := range registry.Routes {
		covered[value.Model] = true
		catalogue, probed := catalogues[value.Name]
		if probed && len(catalogue) > 0 && !containsString(catalogue, value.Model) {
			value.Disabled = true
			value.Note = driftMessage(value, catalogue)
		}
		out.Routes = append(out.Routes, value)
	}

	for _, value := range registry.Routes {
		catalogue := catalogues[value.Name]
		if len(catalogue) == 0 {
			continue
		}
		for _, model := range catalogue {
			if covered[model] || !strings.HasSuffix(model, "-free") {
				continue
			}
			covered[model] = true
			out.Routes = append(out.Routes, Route{
				Name:      suggestedName(model, out),
				Wire:      value.Wire,
				BaseURL:   value.BaseURL,
				Model:     model,
				APIKeyEnv: value.APIKeyEnv,
				Rank:      nextFreeRank(out),
				Pricing:   model,
				Disabled:  true,
				Note:      "new in the catalogue, never probed; enable it once a solve has proven it",
			})
		}
	}
	out.sort()
	return out
}

// freeBandStart is where free routes are ranked. New ones land at the end of
// the band so they never displace a route with evidence behind it.
const freeBandStart = 30

func nextFreeRank(registry Registry) int {
	rank := freeBandStart
	for _, value := range registry.Routes {
		if value.Rank >= rank && value.Rank < freeBandStart+50 {
			rank = value.Rank + 1
		}
	}
	return rank
}

// suggestedName derives a route name from the model, keeping it unique.
func suggestedName(model string, registry Registry) string {
	base := "zen-free-" + strings.TrimSuffix(model, "-free")
	name := base
	for suffix := 2; ; suffix++ {
		if _, taken := registry.Find(name); !taken {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, suffix)
	}
}

// Drift lists the routes whose configured model is missing from the catalogue
// the route itself advertises.
func Drift(results []Health) []string {
	var out []string
	for _, health := range results {
		if health.Drift != "" {
			out = append(out, fmt.Sprintf("drift  %s  %s", health.Route, health.Drift))
		}
	}
	return out
}
