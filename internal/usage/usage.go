// Package usage extracts token and cost accounting from OpenAI, Anthropic and
// Gemini-compatible responses, including streamed responses.
package usage

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/jonas/opencode-go-router/internal/catalog"
)

// Usage is normalized billing information for one request.
type Usage struct {
	Input         int64
	Output        int64
	Reasoning     int64
	CacheRead     int64
	CacheWrite    int64
	Cost          float64
	Allowance     float64
	ListCost      float64
	BillingSource string

	Estimated bool
	Found     bool

	promptRaw     int64
	completionRaw int64
	ocTokens      bool
	ocInput       int64
	ocOutput      int64
	ocReasoning   int64
	ocCacheRead   int64
	ocCacheWrite  int64
}

func (u Usage) Tokens() int64 {
	return u.Input + u.Output + u.CacheRead + u.CacheWrite
}

func num(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

func intOf(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if f, ok := num(v); ok && f > 0 {
				return int64(math.Round(f))
			}
		}
	}
	return 0
}

func floatOf(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if f, ok := num(v); ok {
				return f
			}
		}
	}
	return 0
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func childMap(m map[string]any, key string) map[string]any {
	v, ok := m[key]
	if !ok {
		return nil
	}
	c, _ := v.(map[string]any)
	return c
}

func parseOpenCode(m map[string]any, u *Usage) {
	u.Allowance = maxf(u.Allowance, floatOf(m, "allowance"))
	u.ListCost = maxf(u.ListCost, floatOf(m, "list_cost"))
	if f := floatOf(m, "cost"); f > 0 {
		u.Cost = maxf(u.Cost, f)
	}
	if s, ok := m["billing_source"].(string); ok && s != "" {
		u.BillingSource = s
	}
	if tokens := childMap(m, "tokens"); tokens != nil {
		u.ocTokens = true
		u.ocInput = max64(u.ocInput, intOf(tokens, "input"))
		u.ocOutput = max64(u.ocOutput, intOf(tokens, "output"))
		u.ocReasoning = max64(u.ocReasoning, intOf(tokens, "reasoning"))
		u.ocCacheRead = max64(u.ocCacheRead, intOf(tokens, "cache_read"))
		u.ocCacheWrite = max64(u.ocCacheWrite, intOf(tokens, "cache_write")+intOf(tokens, "cache_write_5m")+intOf(tokens, "cache_write_1h"))
	}
}

func parseUsageObject(m map[string]any, u *Usage) {
	u.Found = true
	prompt := intOf(m, "prompt_tokens", "input_tokens", "promptTokenCount")
	completion := intOf(m, "completion_tokens", "output_tokens", "candidatesTokenCount")
	u.promptRaw = max64(u.promptRaw, prompt)
	u.completionRaw = max64(u.completionRaw, completion)

	reasoning := intOf(m, "reasoning_tokens", "thoughtsTokenCount")
	if d := childMap(m, "completion_tokens_details"); d != nil {
		reasoning = max64(reasoning, intOf(d, "reasoning_tokens"))
	}
	if d := childMap(m, "output_tokens_details"); d != nil {
		reasoning = max64(reasoning, intOf(d, "reasoning_tokens"))
	}
	u.Reasoning = max64(u.Reasoning, reasoning)

	cacheRead := intOf(m, "cache_read_input_tokens", "cache_read_tokens", "cachedContentTokenCount")
	if d := childMap(m, "prompt_tokens_details"); d != nil {
		cacheRead = max64(cacheRead, intOf(d, "cached_tokens"))
	}
	if d := childMap(m, "input_tokens_details"); d != nil {
		cacheRead = max64(cacheRead, intOf(d, "cached_tokens"))
	}
	u.CacheRead = max64(u.CacheRead, cacheRead)

	cacheWrite := intOf(m, "cache_creation_input_tokens", "cache_write_tokens")
	if d := childMap(m, "cache_creation"); d != nil {
		cacheWrite = max64(cacheWrite, intOf(d, "ephemeral_5m_input_tokens", "ephemeral_1h_input_tokens"))
	}
	u.CacheWrite = max64(u.CacheWrite, cacheWrite)

	u.Cost = maxf(u.Cost, floatOf(m, "cost"))
	u.ListCost = maxf(u.ListCost, floatOf(m, "list_cost"))
	u.Allowance = maxf(u.Allowance, floatOf(m, "allowance"))

	if oc := childMap(m, "opencode"); oc != nil {
		parseOpenCode(oc, u)
	}
}

func walk(v any, u *Usage) {
	switch t := v.(type) {
	case map[string]any:
		if oc := childMap(t, "opencode"); oc != nil {
			parseOpenCode(oc, u)
		}
		if um := childMap(t, "usageMetadata"); um != nil {
			parseUsageObject(um, u)
		}
		if um := childMap(t, "usage"); um != nil {
			parseUsageObject(um, u)
		}
		for k, val := range t {
			switch k {
			case "opencode", "usage", "usageMetadata":
				continue
			}
			walk(val, u)
		}
	case []any:
		for _, item := range t {
			walk(item, u)
		}
	}
}

func decode(raw []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

// Extract parses a complete (non-streamed) JSON response body.
func Extract(raw []byte, family string) Usage {
	var u Usage
	v, ok := decode(raw)
	if !ok {
		return u
	}
	walk(v, &u)
	return u
}

// Merge combines incremental usage updates. Token counts and costs are
// cumulative in every supported stream format, so maxima are correct.
func (u *Usage) Merge(other Usage) {
	u.Input = max64(u.Input, other.Input)
	u.Output = max64(u.Output, other.Output)
	u.Reasoning = max64(u.Reasoning, other.Reasoning)
	u.CacheRead = max64(u.CacheRead, other.CacheRead)
	u.CacheWrite = max64(u.CacheWrite, other.CacheWrite)
	u.Cost = maxf(u.Cost, other.Cost)
	u.Allowance = maxf(u.Allowance, other.Allowance)
	u.ListCost = maxf(u.ListCost, other.ListCost)
	u.promptRaw = max64(u.promptRaw, other.promptRaw)
	u.completionRaw = max64(u.completionRaw, other.completionRaw)
	u.ocTokens = u.ocTokens || other.ocTokens
	u.ocInput = max64(u.ocInput, other.ocInput)
	u.ocOutput = max64(u.ocOutput, other.ocOutput)
	u.ocReasoning = max64(u.ocReasoning, other.ocReasoning)
	u.ocCacheRead = max64(u.ocCacheRead, other.ocCacheRead)
	u.ocCacheWrite = max64(u.ocCacheWrite, other.ocCacheWrite)
	if other.BillingSource != "" {
		u.BillingSource = other.BillingSource
	}
	u.Found = u.Found || other.Found
	u.Estimated = u.Estimated || other.Estimated
}

// Normalize converts provider-specific accounting into consistent fields.
// family is one of chat, responses, messages, google.
func (u *Usage) Normalize(family string) {
	if u.ocTokens {
		u.Input = u.ocInput
		u.Output = u.ocOutput
		u.CacheRead = u.ocCacheRead
		u.CacheWrite = u.ocCacheWrite
		if u.ocReasoning > 0 {
			u.Reasoning = u.ocReasoning
		}
		return
	}
	input := u.promptRaw
	if family != "messages" {
		// OpenAI and Gemini prompt counts include cached tokens.
		input -= u.CacheRead
	}
	if input < 0 {
		input = 0
	}
	u.Input = input
	u.Output = u.completionRaw
}

// Finalize normalizes the usage and resolves the dollar amount that should be
// counted against a sub-key's limits.
func (u *Usage) Finalize(family string, model catalog.Model) {
	u.Normalize(family)
	switch {
	case u.Allowance > 0:
		u.Cost = u.Allowance
	case u.Cost > 0:
		// Actual charge reported by the gateway (balance/BYOK).
	case u.ListCost > 0:
		u.Cost = u.ListCost
		u.Estimated = true
	case u.BillingSource == "free" || u.BillingSource == "go" || u.BillingSource == "go-plus":
		u.Cost = 0
	default:
		u.Cost = Estimate(model, *u)
		u.Estimated = u.Cost > 0
	}
}

// Estimate computes a list-price estimate in USD.
func Estimate(m catalog.Model, u Usage) float64 {
	total := float64(u.Input)*m.Cost.Input +
		float64(u.Output)*m.Cost.Output +
		float64(u.CacheRead)*m.Cost.CacheRead +
		float64(u.CacheWrite)*m.Cost.CacheWrite
	if total <= 0 {
		return 0
	}
	return total / 1_000_000
}

// ErrorMessage extracts a human-readable error from an upstream JSON body.
func ErrorMessage(raw []byte) string {
	v, ok := decode(raw)
	if !ok {
		return ""
	}
	var find func(any, int) string
	find = func(v any, depth int) string {
		if depth > 8 {
			return ""
		}
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		if e := childMap(m, "error"); e != nil {
			if s, ok := e["message"].(string); ok && s != "" {
				return s
			}
			if s, ok := e["type"].(string); ok && s != "" {
				return s
			}
		}
		if s, ok := m["message"].(string); ok && s != "" {
			return s
		}
		for _, val := range m {
			if s := find(val, depth+1); s != "" {
				return s
			}
		}
		return ""
	}
	return truncate(find(v, 0), 500)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
