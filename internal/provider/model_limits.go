// model_limits.go — model context window registry and limit lookup.
package provider

import "strings"

// ModelLimits stores the context window and max output tokens for a model (in tokens).
type ModelLimits struct {
	ContextTokens int
	OutputTokens  int
}

// knownModels maps bare model names (no provider prefix) to their limits.
// Sources: OpenAI API docs, Anthropic API docs, model provider pages.
// Last updated: 2026-05.
var knownModels = map[string]ModelLimits{
	// Anthropic Claude 4.x
	"claude-opus-4-7":           {ContextTokens: 200_000, OutputTokens: 32_000},
	"claude-sonnet-4-6":         {ContextTokens: 200_000, OutputTokens: 64_000},
	"claude-haiku-4-5":          {ContextTokens: 200_000, OutputTokens: 8_192},
	"claude-haiku-4-5-20251001": {ContextTokens: 200_000, OutputTokens: 8_192},
	// Anthropic Claude 3.x
	"claude-3-5-sonnet-20241022": {ContextTokens: 200_000, OutputTokens: 8_192},
	"claude-3-5-haiku-20241022":  {ContextTokens: 200_000, OutputTokens: 8_192},
	"claude-3-opus-20240229":     {ContextTokens: 200_000, OutputTokens: 4_096},
	// OpenAI GPT-4.x
	"gpt-4o":       {ContextTokens: 128_000, OutputTokens: 16_384},
	"gpt-4o-mini":  {ContextTokens: 128_000, OutputTokens: 16_384},
	"gpt-4.1":      {ContextTokens: 1_047_576, OutputTokens: 32_768},
	"gpt-4.1-mini": {ContextTokens: 1_047_576, OutputTokens: 32_768},
	"gpt-4.1-nano": {ContextTokens: 1_047_576, OutputTokens: 32_768},
	"gpt-4-turbo":  {ContextTokens: 128_000, OutputTokens: 4_096},
	// OpenAI o-series
	"o3":      {ContextTokens: 200_000, OutputTokens: 100_000},
	"o4-mini": {ContextTokens: 200_000, OutputTokens: 100_000},
	// DeepSeek
	"deepseek-v3": {ContextTokens: 64_000, OutputTokens: 8_000},
	"deepseek-r1": {ContextTokens: 64_000, OutputTokens: 8_000},
	// Qwen
	"qwen2.5-72b-instruct": {ContextTokens: 131_072, OutputTokens: 8_192},
	"qwen3-235b-a22b":      {ContextTokens: 131_072, OutputTokens: 8_192},
	// Gemini
	"gemini-2.0-flash": {ContextTokens: 1_048_576, OutputTokens: 8_192},
	"gemini-2.5-pro":   {ContextTokens: 1_048_576, OutputTokens: 65_536},
}

// defaultLimits is used for any model not found in knownModels.
// 256K is a conservative fallback (HermesAgent pattern).
var defaultLimits = ModelLimits{ContextTokens: 256_000, OutputTokens: 8_192}

// GetModelLimits returns context window limits for a model name.
// It strips provider prefixes (e.g. "openai/gpt-4o" → "gpt-4o") before lookup.
// Unknown models return a 256K conservative fallback.
func GetModelLimits(modelName string) ModelLimits {
	bare := modelName
	if idx := strings.LastIndex(modelName, "/"); idx >= 0 {
		bare = modelName[idx+1:]
	}
	if limits, ok := knownModels[bare]; ok {
		return limits
	}
	return defaultLimits
}
