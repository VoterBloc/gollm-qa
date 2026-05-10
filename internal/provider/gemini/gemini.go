// Package gemini implements the provider.Provider interface using
// Google's GenAI SDK against the Gemini API backend.
package gemini

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/genai"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// providerPrefix is the registry key this package registers under.
// One source of truth for both the lookup key and the spec stamped
// on Usage.ModelID.
const providerPrefix = "gemini"

// DefaultModel is the SDK model id used when New is called without
// going through NewFromSpec. The SDK takes the model as a plain
// string; constants aren't published.
const DefaultModel = "gemini-2.5-pro"

// modelAliases maps the user-visible spec suffix (e.g. "2.5-pro")
// to the SDK's model id (e.g. "gemini-2.5-pro"). The user-visible
// form drops the redundant "gemini-" prefix since "<provider>:<model>"
// already carries the provider name. Unknown aliases fall through
// to the SDK verbatim — the model id is a string, so new releases
// work without a code change.
var modelAliases = map[string]string{
	"2.5-pro":   "gemini-2.5-pro",
	"2.5-flash": "gemini-2.5-flash",
}

// defaultModelAlias is the spec suffix that resolves to DefaultModel.
const defaultModelAlias = "2.5-pro"

// defaultMaxOutputTokens caps per-response output. Hardcoded — no
// caller has needed an override; revisit when tuning becomes a real ask.
const defaultMaxOutputTokens int32 = 4096

// init self-registers under "gemini:" so callers of provider.New
// reach this package via a blank import.
func init() {
	provider.Register(providerPrefix, func(model string) (provider.Provider, error) {
		return NewFromSpec(model)
	})
}

// Option configures a Gemini provider. Mirrors the option-functions
// pattern in the claude / openai packages so tests can inject base
// URL / API key without coupling to the SDK's config shape.
type Option func(*genai.ClientConfig)

// WithAPIKey sets the API key explicitly. Defaults to the GEMINI_API_KEY
// / GOOGLE_API_KEY env var the SDK reads automatically.
func WithAPIKey(key string) Option {
	return func(c *genai.ClientConfig) {
		c.APIKey = key
	}
}

// WithBaseURL points the SDK at a non-default endpoint. Used by tests
// to swap in an httptest server; not exercised in production.
func WithBaseURL(url string) Option {
	return func(c *genai.ClientConfig) {
		c.HTTPOptions.BaseURL = url
	}
}

// Gemini implements provider.Provider against the Gemini API.
type Gemini struct {
	client    *genai.Client
	initErr   error // captured at New() time; surfaced from Chat
	model     string
	modelSpec string // "gemini:<alias>" — populates Usage.ModelID
	maxTokens int32
}

// New creates a Gemini provider on the default model. Reads
// GEMINI_API_KEY or GOOGLE_API_KEY from the environment by default;
// pass WithAPIKey or WithBaseURL to override. Use NewFromSpec when
// you need a non-default model — that path keeps modelSpec aligned
// with the actual model so Usage.ModelID stays truthful.
//
// genai.NewClient takes a context, but for the Gemini API backend
// (the default) it doesn't actually do any RPCs — it just validates
// config. We pass context.Background() and stash any init error to
// surface from the first Chat call rather than threading errors
// through New (which would break parity with the claude / openai
// providers' constructor shape).
func New(opts ...Option) *Gemini {
	cfg := &genai.ClientConfig{Backend: genai.BackendGeminiAPI}
	for _, o := range opts {
		o(cfg)
	}
	client, err := genai.NewClient(context.Background(), cfg)
	return &Gemini{
		client:    client,
		initErr:   err,
		model:     DefaultModel,
		modelSpec: providerPrefix + ":" + defaultModelAlias,
		maxTokens: defaultMaxOutputTokens,
	}
}

// NewFromSpec creates a Gemini provider for the given model alias
// (the suffix of a "gemini:<alias>" spec). Unknown aliases pass
// through to the SDK verbatim — model ids are strings, so any
// well-formed id works and the API rejects bogus ones on first
// request.
func NewFromSpec(modelName string, opts ...Option) (*Gemini, error) {
	if modelName == "" {
		return nil, fmt.Errorf("gemini: empty model name")
	}
	g := New(opts...)
	g.model = resolveModel(modelName)
	g.modelSpec = providerPrefix + ":" + modelName
	return g, nil
}

func resolveModel(name string) string {
	if m, ok := modelAliases[name]; ok {
		return m
	}
	return name
}

// Chat sends a conversation with tools to Gemini and returns the response.
func (g *Gemini) Chat(ctx context.Context, messages []provider.Message, tools []provider.Tool) (*provider.Response, error) {
	if g.initErr != nil {
		return nil, fmt.Errorf("gemini client init: %w", g.initErr)
	}

	contents, systemInstruction := toSDKContents(messages)
	cfg := &genai.GenerateContentConfig{
		MaxOutputTokens: g.maxTokens,
	}
	if systemInstruction != "" {
		cfg.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: systemInstruction}},
		}
	}
	if sdkTools := toSDKTools(tools); len(sdkTools) > 0 {
		cfg.Tools = sdkTools
	}

	resp, err := g.client.Models.GenerateContent(ctx, g.model, contents, cfg)
	if err != nil {
		return nil, fmt.Errorf("gemini chat: %w", err)
	}

	out := fromSDKResponse(resp)
	out.Usage.ModelID = g.modelSpec
	return out, nil
}

// toSDKContents converts our messages to Gemini's []*Content format
// and extracts the system instruction. Gemini, like Anthropic but
// unlike OpenAI, expects the system message in a distinct field
// rather than interleaved.
//
// Role mapping:
//   - RoleSystem    → SystemInstruction (extracted, returned separately)
//   - RoleUser      → Content{Role: "user", Parts: [{Text}]}
//   - RoleAssistant → Content{Role: "model", Parts: [{Text} and/or {FunctionCall}...]}
//   - RoleTool      → Content{Role: "user", Parts: [{FunctionResponse}]}
//
// Note Gemini calls the assistant role "model" in its wire shape.
func toSDKContents(messages []provider.Message) ([]*genai.Content, string) {
	var (
		contents          []*genai.Content
		systemInstruction string
	)
	for _, msg := range messages {
		switch msg.Role {
		case provider.RoleSystem:
			// Last-wins, matching the claude package's behavior. Multi
			// system-message scenarios are rare and ambiguous anyway.
			systemInstruction = msg.Content

		case provider.RoleUser:
			contents = append(contents, &genai.Content{
				Role:  genai.RoleUser,
				Parts: []*genai.Part{{Text: msg.Content}},
			})

		case provider.RoleAssistant:
			parts := make([]*genai.Part, 0, 1+len(msg.ToolCalls))
			if msg.Content != "" {
				parts = append(parts, &genai.Part{Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				args := map[string]any{}
				if tc.Arguments != "" {
					// Best-effort decode; if Arguments isn't JSON we
					// pass an empty map rather than failing the whole
					// conversation. The model never produces invalid
					// JSON args in practice, but conversation replay
					// from a forwarding layer could.
					_ = json.Unmarshal([]byte(tc.Arguments), &args)
				}
				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   tc.ID,
						Name: tc.Name,
						Args: args,
					},
				})
			}
			contents = append(contents, &genai.Content{
				Role:  genai.RoleModel,
				Parts: parts,
			})

		case provider.RoleTool:
			// Tool results come back as a "user" content with a
			// FunctionResponse part. Gemini wants the response as a
			// structured map; we wrap the string content under
			// {"output": ...} which is the SDK-documented convention.
			contents = append(contents, &genai.Content{
				Role: genai.RoleUser,
				Parts: []*genai.Part{
					{
						FunctionResponse: &genai.FunctionResponse{
							ID:       msg.ToolID,
							Response: map[string]any{"output": msg.Content},
						},
					},
				},
			})
		}
	}
	return contents, systemInstruction
}

// toSDKTools converts our tool definitions to Gemini's function-
// declarations format. ParametersJsonSchema accepts a JSON Schema map
// directly, which matches the shape our provider.Tool.Parameters
// already uses — no traversal needed.
func toSDKTools(tools []provider.Tool) []*genai.Tool {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]*genai.FunctionDeclaration, len(tools))
	for i, t := range tools {
		decls[i] = &genai.FunctionDeclaration{
			Name:                 t.Name,
			Description:          t.Description,
			ParametersJsonSchema: t.Parameters,
		}
	}
	// All function declarations live under a single Tool — Gemini
	// scopes "tools" to broader categories (function-calling vs.
	// retrieval vs. code-execution); function declarations are one
	// category's contents.
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

// fromSDKResponse converts a Gemini GenerateContentResponse to our
// generic format. Single-candidate runs only — N>1 isn't useful for
// an agent loop and we don't request it.
func fromSDKResponse(resp *genai.GenerateContentResponse) *provider.Response {
	result := &provider.Response{}
	result.Message.Role = provider.RoleAssistant

	if resp.UsageMetadata != nil {
		result.Usage.InputTokens = int(resp.UsageMetadata.PromptTokenCount)
		result.Usage.OutputTokens = int(resp.UsageMetadata.CandidatesTokenCount)
	}

	if len(resp.Candidates) == 0 {
		// Should not happen in practice; the API guarantees at least
		// one candidate. Returning an empty assistant turn keeps the
		// agent loop's "no tool calls = done" branch the right read.
		result.StopReason = "end"
		return result
	}

	choice := resp.Candidates[0]
	result.StopReason = mapFinishReason(choice.FinishReason)

	if choice.Content == nil {
		return result
	}

	for _, part := range choice.Content.Parts {
		if part.Text != "" {
			result.Message.Content += part.Text
		}
		if part.FunctionCall != nil {
			argsJSON, _ := json.Marshal(part.FunctionCall.Args)
			result.Message.ToolCalls = append(result.Message.ToolCalls, provider.ToolCall{
				ID:        part.FunctionCall.ID,
				Name:      part.FunctionCall.Name,
				Arguments: string(argsJSON),
			})
		}
	}

	// Gemini's STOP finish_reason fires even on tool-call turns —
	// the convention is "the model finished generating its turn,
	// which may contain a function_call." Distinguish tool-use here
	// so the agent loop reads the StopReason like it does for the
	// other providers.
	if result.StopReason == "end" && len(result.Message.ToolCalls) > 0 {
		result.StopReason = "tool_use"
	}

	return result
}

// mapFinishReason normalizes Gemini's FinishReason vocabulary to ours.
// STOP is the natural-end case (may also accompany a tool call —
// caller distinguishes by inspecting ToolCalls). MAX_TOKENS is the
// length cap. Anything else passes through verbatim so unfamiliar
// reasons surface in reports rather than being silently coerced.
func mapFinishReason(reason genai.FinishReason) string {
	switch reason {
	case genai.FinishReasonStop, "":
		return "end"
	case genai.FinishReasonMaxTokens:
		return "length"
	default:
		return string(reason)
	}
}
