// Package local implements the provider.Provider interface against
// any OpenAI-compatible HTTP endpoint — primarily Ollama's
// `localhost:11434/v1` but also vLLM, llama.cpp's OpenAI-compat
// mode, LM Studio, etc.
//
// The shape is intentionally a thin wrapper over the openai package:
// Ollama's chat.completions endpoint is wire-compatible with
// OpenAI's, so message conversion, tool-call shape, and response
// parsing are identical. We compose openai.OpenAI rather than
// duplicate the conversion code, then override Usage.ModelID
// stamping so cost accounting bills against `local:<model>` (priced
// at zero in models.yaml) instead of `openai:<model>`.
package local

import (
	"context"
	"fmt"
	"os"
	"strings"

	oaiopt "github.com/openai/openai-go/option"

	"github.com/VoterBloc/gollm-qa/internal/provider"
	"github.com/VoterBloc/gollm-qa/internal/provider/openai"
)

// providerPrefix is the registry key this package registers under.
// One source of truth for both the lookup key and the spec stamped
// on Usage.ModelID.
const providerPrefix = "local"

// defaultOllamaHost is where Ollama listens by default. The /v1
// suffix is appended in resolveBaseURL — Ollama exposes the
// OpenAI-compatible API there.
const defaultOllamaHost = "http://localhost:11434"

// DefaultModel is the spec suffix used when New is called without a
// model name. Llama 3.1 is a common, tool-capable local choice.
// Users pass whatever they have pulled locally via `local:<model>`.
const DefaultModel = "llama3.1"

// placeholderAPIKey is what we feed the OpenAI SDK when no key is
// supplied — Ollama doesn't validate keys, but the SDK rejects an
// empty value. "ollama" is the placeholder Ollama's own docs use,
// so a misrouted request lands somewhere recognizable.
const placeholderAPIKey = "ollama"

// init self-registers under "local:" so callers of provider.New
// reach this package via a blank import.
func init() {
	provider.Register(providerPrefix, func(model string) (provider.Provider, error) {
		return NewFromSpec(model)
	})
}

// Option configures a Local provider. Mirrors the option-functions
// pattern in the other provider packages so callers don't have to
// import the OpenAI SDK's option type just to set a base URL.
type Option func(*localConfig)

type localConfig struct {
	baseURL string
	apiKey  string
}

// WithBaseURL points the provider at a non-default OpenAI-compatible
// endpoint. Wins over OLLAMA_HOST. The /v1 path suffix is appended
// automatically if missing — pass either form.
func WithBaseURL(url string) Option {
	return func(c *localConfig) { c.baseURL = url }
}

// WithAPIKey overrides the placeholder key. Ollama doesn't check
// authentication, but some compatible servers (vLLM, llama.cpp's
// OpenAI-compat mode) do. OLLAMA_API_KEY env var works too.
func WithAPIKey(key string) Option {
	return func(c *localConfig) { c.apiKey = key }
}

// Intentionally no WithModel-style option. The model name only
// enters via NewFromSpec's argument so it can't drift away from
// modelSpec (the bug the original Claude package's WithModel had,
// flagged on PR #39). If you need a different model, build a new
// provider via NewFromSpec rather than mutating an existing one.

// Local implements provider.Provider via an inner openai.OpenAI
// configured with a local base URL + placeholder API key. Chat()
// delegates and post-processes the Usage.ModelID stamp.
type Local struct {
	inner     *openai.OpenAI
	modelSpec string // "local:<name>" — overrides what the inner provider stamps
}

// New creates a Local provider on the default model. Resolution
// order for the endpoint: WithBaseURL > OLLAMA_HOST env > default
// (http://localhost:11434). Use NewFromSpec for a non-default model.
func New(opts ...Option) (*Local, error) {
	return NewFromSpec(DefaultModel, opts...)
}

// NewFromSpec creates a Local provider for the given model name
// (the suffix of a `local:<model>` spec). The OpenAI SDK passes
// model names to the API verbatim, so any name Ollama has pulled
// works (llama3.1, qwen2.5, mistral, deepseek-r1, ...). Pricing
// for unknown local models falls through to zero with a one-time
// "unknown model" warning — see internal/cost.
func NewFromSpec(modelName string, opts ...Option) (*Local, error) {
	if modelName == "" {
		return nil, fmt.Errorf("local: empty model name")
	}

	cfg := &localConfig{}
	for _, o := range opts {
		o(cfg)
	}

	baseURL := resolveBaseURL(cfg.baseURL)
	apiKey := resolveAPIKey(cfg.apiKey)

	inner, err := openai.NewFromSpec(modelName,
		oaiopt.WithBaseURL(baseURL),
		oaiopt.WithAPIKey(apiKey),
	)
	if err != nil {
		return nil, fmt.Errorf("local: %w", err)
	}
	return &Local{
		inner:     inner,
		modelSpec: providerPrefix + ":" + modelName,
	}, nil
}

// resolveBaseURL applies the WithBaseURL > OLLAMA_HOST > default
// cascade and ensures the URL ends with a `/v<N>` versioned path
// (Ollama's OpenAI-compat endpoint lives at `/v1`). Idempotent —
// callers can pass `http://host:port`, `http://host:port/v1`, or
// even `http://host:port/v1beta` for compat servers that publish a
// non-`v1` path, and the resolver leaves the existing version
// segment alone instead of double-appending.
func resolveBaseURL(explicit string) string {
	base := explicit
	if base == "" {
		base = os.Getenv("OLLAMA_HOST")
	}
	if base == "" {
		base = defaultOllamaHost
	}
	base = strings.TrimRight(base, "/")
	if !hasVersionPath(base) {
		base += "/v1"
	}
	return base
}

// hasVersionPath reports whether base already ends in a versioned
// path segment like `/v1`, `/v1beta`, `/v2`, `/v3alpha`. Used to
// keep resolveBaseURL idempotent against non-default compat servers.
func hasVersionPath(base string) bool {
	i := strings.LastIndex(base, "/v")
	if i < 0 {
		return false
	}
	rest := base[i+2:]
	return len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9'
}

// resolveAPIKey applies the WithAPIKey > OLLAMA_API_KEY > placeholder
// cascade. The placeholder ("ollama") matches Ollama's docs; operators
// running a key-checking compat server (vLLM, llama.cpp) set the env
// var or pass WithAPIKey.
func resolveAPIKey(explicit string) string {
	key := explicit
	if key == "" {
		key = os.Getenv("OLLAMA_API_KEY")
	}
	if key == "" {
		key = placeholderAPIKey
	}
	return key
}

// Chat delegates to the OpenAI-compatible inner provider and
// rewrites Usage.ModelID so cost accounting bills against our
// local:<model> key (priced at zero in models.yaml) rather than
// the openai:<model> the inner provider would stamp.
//
// Models without tool-call support degrade naturally: the model
// returns plain text instead of tool_calls, and the agent loop
// reads that as "no tool calls → goals_complete." No special
// handling needed in this layer.
func (l *Local) Chat(ctx context.Context, messages []provider.Message, tools []provider.Tool) (*provider.Response, error) {
	resp, err := l.inner.Chat(ctx, messages, tools)
	if err != nil {
		return nil, err
	}
	resp.Usage.ModelID = l.modelSpec
	return resp, nil
}
