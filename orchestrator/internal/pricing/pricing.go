// Package pricing turns response usage into a USD cost.
//
// Rates are Anthropic first-party list prices in USD per million tokens, as
// published for the current model generation (checked 2026-10-17 against
// the claude-api reference: Opus 5.5 $4/$20, Sonnet 5.5 $2/$10, Haiku 5.5
// $0.10/$0.50 up to a 100k-token prompt and $0.50/$2.50 beyond; 5-minute
// cache writes 1.25x input, 1-hour writes 2x; cache reads $0.20 on Opus 5.5
// and Sonnet 5.5, 0.1x input on Haiku 5.5; web search $10 per 1,000).
// Re-check https://platform.claude.com/docs/en/about-claude/pricing when a
// model is added.
package pricing

import (
	"fmt"

	"github.com/LinuxAd/dark-factory/orchestrator/internal/usage"
)

// Cost is an amount in nano-USD. $1 per million tokens is exactly 1,000
// nano-USD per token, so every published rate is an integer here.
type Cost int64

// String formats the cost in USD with six decimals, rounded half up, to
// match agent_calls.cost_usd numeric(12,6).
func (c Cost) String() string {
	micro := (int64(c) + 500) / 1000
	return fmt.Sprintf("%d.%06d", micro/1_000_000, micro%1_000_000)
}

// rates are nano-USD per token (= USD per million tokens x 1000).
type rates struct {
	input, output, cacheWrite5m, cacheWrite1h, cacheRead int64
}

type modelPrice struct {
	base rates
	// longPromptOver, when > 0, switches to long once the prompt
	// (input + cache read + cache write tokens) exceeds it.
	longPromptOver int64
	long           rates
}

const webSearchPerRequest = 10_000_000 // $10 per 1,000 requests

var prices = map[string]modelPrice{
	"claude-opus-5-5":   {base: rates{input: 4000, output: 20000, cacheWrite5m: 5000, cacheWrite1h: 8000, cacheRead: 200}},
	"claude-sonnet-5-5": {base: rates{input: 2000, output: 10000, cacheWrite5m: 2500, cacheWrite1h: 4000, cacheRead: 200}},
	"claude-haiku-5-5": {
		base:           rates{input: 100, output: 500, cacheWrite5m: 125, cacheWrite1h: 200, cacheRead: 10},
		longPromptOver: 100_000,
		long:           rates{input: 500, output: 2500, cacheWrite5m: 625, cacheWrite1h: 1000, cacheRead: 50},
	},
}

// Compute returns the cost of one response. ok is false when the model has
// no price; callers record NULL cost and count it, never fail the call.
func Compute(u usage.Usage) (Cost, bool) {
	p, ok := prices[u.Model]
	if !ok {
		return 0, false
	}
	r := p.base
	if p.longPromptOver > 0 && u.InputTokens+u.CacheReadTokens+u.CacheWriteTokens > p.longPromptOver {
		r = p.long
	}
	// Cache writes without a TTL breakdown, or the part the breakdown does
	// not cover, are priced at the 5-minute rate (the API default).
	write5m := max(u.CacheWriteTokens-u.CacheWrite1hTokens, 0)
	c := u.InputTokens*r.input +
		u.OutputTokens*r.output +
		u.CacheReadTokens*r.cacheRead +
		write5m*r.cacheWrite5m +
		u.CacheWrite1hTokens*r.cacheWrite1h +
		u.WebSearchRequests*webSearchPerRequest
	return Cost(c), true
}
