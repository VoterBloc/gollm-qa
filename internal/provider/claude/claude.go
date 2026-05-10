// Package claude implements the provider.Provider interface using the Anthropic API.
package claude

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// providerPrefix is the registry key this package registers under. Lives
// in one place so the prefix used in Usage.ModelID can't drift from the
// prefix used to look the provider up.
const providerPrefix = "claude"

// DefaultModel is the SDK model used when New is called without going
// through NewFromSpec. Pulled out of the constructor body so the default
// is named and visible to direct callers; the registry path always
// specifies a model.
var DefaultModel = anthropic.ModelClaudeSonnet4_5_20250929

// modelAliases maps short, human-friendly model names to fully-qualified
// SDK model identifiers. Names not in the map fall through to the SDK
// verbatim — anthropic.Model is a typed string, so any well-formed ID
// works; the SDK rejects bogus ones at request time.
var modelAliases = map[string]anthropic.Model{
	"sonnet-4-5": anthropic.ModelClaudeSonnet4_5_20250929,
}

// defaultModelAlias is the spec suffix that resolves to DefaultModel.
// Kept so direct callers of New populate Usage.ModelID with the same
// spec a registry-constructed provider would use.
const defaultModelAlias = "sonnet-4-5"

// init registers the Claude provider so callers of provider.New("claude:...")
// get one. Anywhere using the registry must import this package directly
// or via blank import to fire this init.
func init() {
	provider.Register(providerPrefix, func(model string) (provider.Provider, error) {
		return NewFromSpec(model)
	})
}

// defaultMaxTokens is the per-response output cap. Hardcoded — no caller
// has needed an override; a future tuning knob can re-introduce the option.
const defaultMaxTokens = 4096

// Claude implements provider.Provider using the Anthropic Messages API.
type Claude struct {
	client    *anthropic.Client
	model     anthropic.Model
	modelSpec string // "<prefix>:<alias>" — populates Usage.ModelID
	maxTokens int64
}

// New creates a Claude provider on the default model. Reads ANTHROPIC_API_KEY
// from the environment by default; pass option.WithAPIKey or option.WithBaseURL
// to override. Use NewFromSpec when you need a non-default model — that path
// keeps modelSpec aligned with the actual model so Usage.ModelID stays
// truthful. Selecting a model via direct-mutation here is intentionally not
// supported.
func New(opts ...option.RequestOption) *Claude {
	client := anthropic.NewClient(opts...)
	return &Claude{
		client:    &client,
		model:     DefaultModel,
		modelSpec: providerPrefix + ":" + defaultModelAlias,
		maxTokens: defaultMaxTokens,
	}
}

// NewFromSpec creates a Claude provider for the given model alias (the
// suffix of a "claude:<alias>" spec). Unknown aliases pass through to the
// SDK verbatim — anthropic.Model is a typed string, so any well-formed
// model ID works and the SDK rejects bogus ones on first request.
func NewFromSpec(modelName string, opts ...option.RequestOption) (*Claude, error) {
	if modelName == "" {
		return nil, fmt.Errorf("claude: empty model name")
	}
	c := New(opts...)
	c.model = resolveModel(modelName)
	c.modelSpec = providerPrefix + ":" + modelName
	return c, nil
}

func resolveModel(name string) anthropic.Model {
	if m, ok := modelAliases[name]; ok {
		return m
	}
	return anthropic.Model(name)
}

// Chat sends a conversation with tools to Claude and returns the response.
func (c *Claude) Chat(ctx context.Context, messages []provider.Message, tools []provider.Tool) (*provider.Response, error) {
	sdkMessages, systemPrompt := toSDKMessages(messages)
	sdkTools := toSDKTools(tools)

	params := anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		Messages:  sdkMessages,
		Tools:     sdkTools,
	}
	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	resp, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("claude chat: %w", err)
	}

	out := fromSDKResponse(resp)
	out.Usage.ModelID = c.modelSpec
	return out, nil
}

// toSDKMessages converts our messages to Anthropic SDK format.
// Extracts the system message (Claude expects it separately) and maps
// the rest to SDK MessageParams.
func toSDKMessages(messages []provider.Message) ([]anthropic.MessageParam, string) {
	var sdkMessages []anthropic.MessageParam
	var systemPrompt string

	for _, msg := range messages {
		switch msg.Role {
		case provider.RoleSystem:
			systemPrompt = msg.Content

		case provider.RoleUser:
			sdkMessages = append(sdkMessages, anthropic.NewUserMessage(
				anthropic.NewTextBlock(msg.Content),
			))

		case provider.RoleAssistant:
			var blocks []anthropic.ContentBlockParamUnion
			if msg.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
			}
			for _, tc := range msg.ToolCalls {
				// Parse arguments string back to raw JSON for the SDK.
				var input json.RawMessage
				if tc.Arguments != "" {
					input = json.RawMessage(tc.Arguments)
				} else {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, input, tc.Name))
			}
			sdkMessages = append(sdkMessages, anthropic.NewAssistantMessage(blocks...))

		case provider.RoleTool:
			sdkMessages = append(sdkMessages, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(msg.ToolID, msg.Content, false),
			))
		}
	}

	return sdkMessages, systemPrompt
}

// toSDKTools converts our tool definitions to Anthropic SDK format.
func toSDKTools(tools []provider.Tool) []anthropic.ToolUnionParam {
	sdkTools := make([]anthropic.ToolUnionParam, len(tools))
	for i, t := range tools {
		// Extract properties and required from our JSON Schema map.
		properties, _ := t.Parameters["properties"]
		required, _ := t.Parameters["required"].([]string)

		// If required came back as []any from JSON parsing, convert it.
		if required == nil {
			if raw, ok := t.Parameters["required"].([]any); ok {
				for _, v := range raw {
					if s, ok := v.(string); ok {
						required = append(required, s)
					}
				}
			}
		}

		sdkTools[i] = anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: properties,
					Required:   required,
				},
			},
		}
	}
	return sdkTools
}

// fromSDKResponse converts an Anthropic SDK response to our generic format.
func fromSDKResponse(resp *anthropic.Message) *provider.Response {
	result := &provider.Response{
		StopReason: mapStopReason(resp.StopReason),
		Usage: provider.Usage{
			InputTokens:  int(resp.Usage.InputTokens),
			OutputTokens: int(resp.Usage.OutputTokens),
		},
	}

	result.Message.Role = provider.RoleAssistant

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			tb := block.AsText()
			result.Message.Content += tb.Text

		case "tool_use":
			tb := block.AsToolUse()
			inputJSON, _ := json.Marshal(tb.Input)
			result.Message.ToolCalls = append(result.Message.ToolCalls, provider.ToolCall{
				ID:        tb.ID,
				Name:      tb.Name,
				Arguments: string(inputJSON),
			})
		}
	}

	return result
}

func mapStopReason(reason anthropic.StopReason) string {
	switch reason {
	case anthropic.StopReasonEndTurn:
		return "end"
	case anthropic.StopReasonToolUse:
		return "tool_use"
	case anthropic.StopReasonMaxTokens:
		return "length"
	default:
		return string(reason)
	}
}