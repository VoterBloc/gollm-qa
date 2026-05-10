// Package openai implements the provider.Provider interface using
// OpenAI's chat.completions API.
package openai

import (
	"context"
	"fmt"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
	"github.com/openai/openai-go/shared/constant"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// providerPrefix is the registry key this package registers under.
// One source of truth for both the lookup key and the spec stamped
// on Usage.ModelID.
const providerPrefix = "openai"

// DefaultModel is the SDK model used when New is called without going
// through NewFromSpec — same shape as the claude package, so direct
// callers without a spec still produce a working provider.
var DefaultModel shared.ChatModel = shared.ChatModelGPT4o

// modelAliases maps friendly suffix names to SDK model identifiers.
// The SDK type is a string newtype, so unknown names fall through to
// the API verbatim and the server rejects bogus ids on first request.
var modelAliases = map[string]shared.ChatModel{
	"gpt-4o":      shared.ChatModelGPT4o,
	"gpt-4o-mini": shared.ChatModelGPT4oMini,
}

// defaultModelAlias is the spec suffix that resolves to DefaultModel.
const defaultModelAlias = "gpt-4o"

// defaultMaxTokens caps per-response output. Hardcoded — no caller has
// needed an override; revisit when tuning becomes a real ask.
const defaultMaxTokens int64 = 4096

// init self-registers under "openai:" so callers of provider.New
// reach this package via a blank import.
func init() {
	provider.Register(providerPrefix, func(model string) (provider.Provider, error) {
		return NewFromSpec(model)
	})
}

// OpenAI implements provider.Provider against the OpenAI chat.completions API.
type OpenAI struct {
	client    *oai.Client
	model     shared.ChatModel
	modelSpec string // "openai:<alias>" — populates Usage.ModelID
	maxTokens int64
}

// New creates an OpenAI provider on the default model. Reads OPENAI_API_KEY
// from the environment by default; pass option.WithAPIKey or option.WithBaseURL
// to override. Use NewFromSpec when you need a non-default model — that path
// keeps modelSpec aligned with the actual model so Usage.ModelID stays truthful.
func New(opts ...option.RequestOption) *OpenAI {
	client := oai.NewClient(opts...)
	return &OpenAI{
		client:    &client,
		model:     DefaultModel,
		modelSpec: providerPrefix + ":" + defaultModelAlias,
		maxTokens: defaultMaxTokens,
	}
}

// NewFromSpec creates an OpenAI provider for the given model alias (the
// suffix of an "openai:<alias>" spec). Unknown aliases pass through to
// the SDK verbatim — shared.ChatModel is a typed string, so any
// well-formed model id works and the API rejects bogus ones on first
// request.
func NewFromSpec(modelName string, opts ...option.RequestOption) (*OpenAI, error) {
	if modelName == "" {
		return nil, fmt.Errorf("openai: empty model name")
	}
	c := New(opts...)
	c.model = resolveModel(modelName)
	c.modelSpec = providerPrefix + ":" + modelName
	return c, nil
}

func resolveModel(name string) shared.ChatModel {
	if m, ok := modelAliases[name]; ok {
		return m
	}
	return shared.ChatModel(name)
}

// Chat sends a conversation with tools to OpenAI and returns the response.
func (o *OpenAI) Chat(ctx context.Context, messages []provider.Message, tools []provider.Tool) (*provider.Response, error) {
	sdkMessages := toSDKMessages(messages)
	sdkTools := toSDKTools(tools)

	params := oai.ChatCompletionNewParams{
		Model:               o.model,
		Messages:            sdkMessages,
		MaxCompletionTokens: param.NewOpt(o.maxTokens),
	}
	if len(sdkTools) > 0 {
		params.Tools = sdkTools
	}

	resp, err := o.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai chat: %w", err)
	}

	out := fromSDKResponse(resp)
	out.Usage.ModelID = o.modelSpec
	return out, nil
}

// toSDKMessages converts our messages to OpenAI SDK format. Unlike the
// Anthropic API, the system prompt is just another message in the list,
// so there's no "extract system separately" step.
func toSDKMessages(messages []provider.Message) []oai.ChatCompletionMessageParamUnion {
	out := make([]oai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case provider.RoleSystem:
			out = append(out, oai.SystemMessage(msg.Content))

		case provider.RoleUser:
			out = append(out, oai.UserMessage(msg.Content))

		case provider.RoleAssistant:
			// AssistantMessage helper covers content-only; for tool
			// calls we have to construct the param directly so both
			// Content and ToolCalls land on the same message — that's
			// the shape the API requires when an assistant turn both
			// speaks and asks for tools.
			asst := oai.ChatCompletionAssistantMessageParam{}
			if msg.Content != "" {
				asst.Content = oai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: param.NewOpt(msg.Content),
				}
			}
			for _, tc := range msg.ToolCalls {
				args := tc.Arguments
				if args == "" {
					args = "{}"
				}
				asst.ToolCalls = append(asst.ToolCalls, oai.ChatCompletionMessageToolCallParam{
					ID:   tc.ID,
					Type: constant.Function(""), // marshals as "function"
					Function: oai.ChatCompletionMessageToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: args,
					},
				})
			}
			out = append(out, oai.ChatCompletionMessageParamUnion{OfAssistant: &asst})

		case provider.RoleTool:
			out = append(out, oai.ToolMessage(msg.Content, msg.ToolID))
		}
	}
	return out
}

// toSDKTools converts our tool definitions to OpenAI's function-calling format.
// OpenAI wraps the JSON Schema directly under `function.parameters`; the only
// shape gymnastics is matching paramObj's type-system expectations.
func toSDKTools(tools []provider.Tool) []oai.ChatCompletionToolParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]oai.ChatCompletionToolParam, len(tools))
	for i, t := range tools {
		fn := shared.FunctionDefinitionParam{
			Name:       t.Name,
			Parameters: shared.FunctionParameters(t.Parameters),
		}
		if t.Description != "" {
			fn.Description = param.NewOpt(t.Description)
		}
		out[i] = oai.ChatCompletionToolParam{
			Type:     constant.Function(""), // marshals as "function"
			Function: fn,
		}
	}
	return out
}

// fromSDKResponse converts an OpenAI ChatCompletion to our generic format.
// Single-choice runs only — N>1 isn't a useful shape for an agent loop.
func fromSDKResponse(resp *oai.ChatCompletion) *provider.Response {
	result := &provider.Response{
		Usage: provider.Usage{
			InputTokens:  int(resp.Usage.PromptTokens),
			OutputTokens: int(resp.Usage.CompletionTokens),
		},
	}
	result.Message.Role = provider.RoleAssistant

	if len(resp.Choices) == 0 {
		// Should not happen in practice; the API guarantees at least
		// one choice. Returning an empty assistant turn keeps the
		// agent loop's "no tool calls = done" branch the right read.
		result.StopReason = "end"
		return result
	}

	choice := resp.Choices[0]
	result.StopReason = mapFinishReason(choice.FinishReason)
	result.Message.Content = choice.Message.Content

	for _, tc := range choice.Message.ToolCalls {
		result.Message.ToolCalls = append(result.Message.ToolCalls, provider.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	return result
}

// mapFinishReason normalizes OpenAI's finish_reason vocabulary to ours.
// "stop"/"end_turn" both mean the model's done; "tool_calls" means it
// asked for tools; "length" means hit the token cap. Anything else
// falls through verbatim — better to surface an unfamiliar reason than
// to silently coerce it.
func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "length"
	default:
		return reason
	}
}
