package pricing

import (
	"testing"

	"github.com/LinuxAd/dark-factory/orchestrator/internal/usage"
)

// thousandEach puts 1,000 tokens in every metered field so each test sums
// the five per-MTok rates of one model.
func thousandEach(model string) usage.Usage {
	return usage.Usage{
		Model:              model,
		InputTokens:        1000,
		OutputTokens:       1000,
		CacheReadTokens:    1000,
		CacheWriteTokens:   2000,
		CacheWrite5mTokens: 1000,
		CacheWrite1hTokens: 1000,
		Found:              true,
	}
}

func TestCostPerModel(t *testing.T) {
	cases := []struct {
		name string
		u    usage.Usage
		want string
	}{
		// $4 in + $20 out + $0.20 read + $5 5m write + $8 1h write, per MTok, x 1k tokens.
		{"opus", thousandEach("claude-opus-5-5"), "0.037200"},
		// $2 + $10 + $0.20 + $2.50 + $4.
		{"sonnet", thousandEach("claude-sonnet-5-5"), "0.018700"},
		// Prompt <= 100k tokens: $0.10 + $0.50 + $0.01 + $0.125 + $0.20.
		{"haiku short prompt", thousandEach("claude-haiku-5-5"), "0.000935"},
		// Prompt > 100k tokens: $0.50 in, $2.50 out per MTok.
		{"haiku long prompt", usage.Usage{Model: "claude-haiku-5-5", InputTokens: 200_000, OutputTokens: 1000, Found: true}, "0.102500"},
		// Cache writes with no TTL breakdown are priced at the 5-minute rate.
		{"no cache breakdown", usage.Usage{Model: "claude-sonnet-5-5", CacheWriteTokens: 1000, Found: true}, "0.002500"},
		// Web search is $10 per 1,000 requests; web fetch has no per-request fee.
		{"web search", usage.Usage{Model: "claude-sonnet-5-5", WebSearchRequests: 2, WebFetchRequests: 3, ServerToolCalls: 5, Found: true}, "0.020000"},
		// Sub-micro-dollar remainders round half up: 15 Haiku input tokens = 1.5 micro-dollars.
		{"rounding", usage.Usage{Model: "claude-haiku-5-5", InputTokens: 15, Found: true}, "0.000002"},
		{"zero", usage.Usage{Model: "claude-opus-5-5"}, "0.000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := Compute(tc.u)
			if !ok {
				t.Fatalf("model %q reported as unpriced", tc.u.Model)
			}
			if got := c.String(); got != tc.want {
				t.Errorf("cost = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestUnknownModelIsUnpriced(t *testing.T) {
	for _, m := range []string{"", "claude-unknown-9", "claude-opus-5-5-20990101", "gpt-x"} {
		if _, ok := Compute(usage.Usage{Model: m, InputTokens: 10, Found: true}); ok {
			t.Errorf("model %q: want unpriced", m)
		}
	}
}

func TestEveryFactoryModelIsPriced(t *testing.T) {
	for _, m := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"} {
		if _, ok := Compute(usage.Usage{Model: m}); !ok {
			t.Errorf("model %q has no price", m)
		}
	}
}

func TestCostStringLarge(t *testing.T) {
	if got := Cost(123_456_789_000_499).String(); got != "123456.789000" {
		t.Errorf("got %s", got)
	}
}
