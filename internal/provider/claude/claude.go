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

// Claude implements provider.Provider using the Anthropic Messages API.
type Claude struct {
	client    *anthropic.Client
	model     anthropic.Model
	maxTokens int64
}

// Option configures a Claude provider.
type Option func(*Claude)

// WithModel sets the model to use. Defaults to Claude Sonnet.
func WithModel(model anthropic.Model) Option {
	return func(c *Claude) {
		c.model = model
	}
}

// WithMaxTokens sets the maximum tokens per response. Defaults to 4096.
func WithMaxTokens(n int64) Option {
	return func(c *Claude) {
		c.maxTokens = n
	}
}

// New creates a Claude provider. By default it reads ANTHROPIC_API_KEY from
// the environment. Pass option.WithAPIKey to override.
func New(opts ...any) *Claude {
	var reqOpts []option.RequestOption
	var clOpts []Option

	for _, o := range opts {
		switch v := o.(type) {
		case option.RequestOption:
			reqOpts = append(reqOpts, v)
		case Option:
			clOpts = append(clOpts, v)
		}
	}

	client := anthropic.NewClient(reqOpts...)
	c := &Claude{
		client:    &client,
		model:     anthropic.ModelClaudeSonnet4_5_20250929,
		maxTokens: 4096,
	}
	for _, o := range clOpts {
		o(c)
	}
	return c
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

	return fromSDKResponse(resp), nil
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