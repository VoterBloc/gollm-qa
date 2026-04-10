package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

func TestToSDKMessages_ExtractsSystemPrompt(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are Bigfoot, a synthetic user who believes in conspiracy theories."},
		{Role: provider.RoleUser, Content: "Go find the aliens."},
	}

	sdkMsgs, systemPrompt := toSDKMessages(messages)

	if systemPrompt != "You are Bigfoot, a synthetic user who believes in conspiracy theories." {
		t.Errorf("expected system prompt to be extracted, got %q", systemPrompt)
	}
	if len(sdkMsgs) != 1 {
		t.Fatalf("expected 1 SDK message (system extracted), got %d", len(sdkMsgs))
	}
}

func TestToSDKMessages_AssistantWithToolCalls(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "Do the thing."},
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{
				{
					ID:        "toolu_pancakes",
					Name:      "join_bloc",
					Arguments: `{"bloc_id": "flat-earth-society-42"}`,
				},
			},
		},
	}

	sdkMsgs, _ := toSDKMessages(messages)

	if len(sdkMsgs) != 2 {
		t.Fatalf("expected 2 SDK messages, got %d", len(sdkMsgs))
	}
}

func TestToSDKMessages_ToolResultBecomesUserMessage(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleTool, ToolID: "toolu_pancakes", Content: `{"success": true}`},
	}

	sdkMsgs, _ := toSDKMessages(messages)

	if len(sdkMsgs) != 1 {
		t.Fatalf("expected 1 SDK message, got %d", len(sdkMsgs))
	}
	// Tool results are sent as user messages in the Anthropic API.
	if sdkMsgs[0].Role != "user" {
		t.Errorf("expected tool result to be a user message, got %q", sdkMsgs[0].Role)
	}
}

func TestToSDKTools_ConvertsCorrectly(t *testing.T) {
	tools := []provider.Tool{
		{
			Name:        "launch_taco_cannon",
			Description: "Fires tacos at unsuspecting legislators.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{
						"type":        "string",
						"description": "Who gets the taco",
					},
				},
				"required": []string{"target"},
			},
		},
	}

	sdkTools := toSDKTools(tools)

	if len(sdkTools) != 1 {
		t.Fatalf("expected 1 SDK tool, got %d", len(sdkTools))
	}
	if sdkTools[0].OfTool == nil {
		t.Fatal("expected OfTool to be set")
	}
	if sdkTools[0].OfTool.Name != "launch_taco_cannon" {
		t.Errorf("expected tool name 'launch_taco_cannon', got %q", sdkTools[0].OfTool.Name)
	}
}

func TestToSDKTools_HandlesRequiredAsAnySlice(t *testing.T) {
	// When required comes from JSON unmarshaling, it arrives as []any, not []string.
	tools := []provider.Tool{
		{
			Name:        "pet_the_golem",
			Description: "Gently pat the clay creature.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   []any{"affection_level"},
			},
		},
	}

	sdkTools := toSDKTools(tools)

	if len(sdkTools[0].OfTool.InputSchema.Required) != 1 {
		t.Fatalf("expected 1 required field, got %d", len(sdkTools[0].OfTool.InputSchema.Required))
	}
	if sdkTools[0].OfTool.InputSchema.Required[0] != "affection_level" {
		t.Errorf("expected required field 'affection_level', got %q", sdkTools[0].OfTool.InputSchema.Required[0])
	}
}

func TestMapStopReason(t *testing.T) {
	tests := []struct {
		input    anthropic.StopReason
		expected string
	}{
		{anthropic.StopReasonEndTurn, "end"},
		{anthropic.StopReasonToolUse, "tool_use"},
		{anthropic.StopReasonMaxTokens, "length"},
		{anthropic.StopReasonStopSequence, "stop_sequence"},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			got := mapStopReason(tt.input)
			if got != tt.expected {
				t.Errorf("mapStopReason(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestChat_EndToEnd(t *testing.T) {
	// Spin up a mock server that returns a canned Claude response with a tool call.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":    "msg_fishsticks",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-sonnet-4-5-20250929",
			"content": []map[string]any{
				{
					"type": "text",
					"text": "I'll join the Bigfoot Appreciation Society right away!",
				},
				{
					"type":  "tool_use",
					"id":    "toolu_sasquatch",
					"name":  "join_bloc",
					"input": map[string]any{"bloc_id": "bigfoot-appreciation-99"},
				},
			},
			"stop_reason": "tool_use",
			"usage": map[string]any{
				"input_tokens":  42,
				"output_tokens": 67,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := New(
		option.WithBaseURL(server.URL),
		option.WithAPIKey("sk-fake-key-for-testing"),
	)

	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a friendly golem."},
		{Role: provider.RoleUser, Content: "Join a bloc."},
	}
	tools := []provider.Tool{
		{
			Name:        "join_bloc",
			Description: "Join a voter bloc.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bloc_id": map[string]any{"type": "string"},
				},
				"required": []string{"bloc_id"},
			},
		},
	}

	resp, err := c.Chat(context.Background(), messages, tools)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	if resp.Message.Role != provider.RoleAssistant {
		t.Errorf("expected assistant role, got %q", resp.Message.Role)
	}
	if resp.Message.Content != "I'll join the Bigfoot Appreciation Society right away!" {
		t.Errorf("unexpected content: %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}
	if resp.Message.ToolCalls[0].Name != "join_bloc" {
		t.Errorf("expected tool call 'join_bloc', got %q", resp.Message.ToolCalls[0].Name)
	}
	if resp.Message.ToolCalls[0].ID != "toolu_sasquatch" {
		t.Errorf("expected tool call ID 'toolu_sasquatch', got %q", resp.Message.ToolCalls[0].ID)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("expected stop reason 'tool_use', got %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 42 {
		t.Errorf("expected 42 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 67 {
		t.Errorf("expected 67 output tokens, got %d", resp.Usage.OutputTokens)
	}
}

func TestChat_TextOnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":    "msg_waffles",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-sonnet-4-5-20250929",
			"content": []map[string]any{
				{
					"type": "text",
					"text": "I've completed all my goals. Time for a nap.",
				},
			},
			"stop_reason": "end_turn",
			"usage": map[string]any{
				"input_tokens":  100,
				"output_tokens": 15,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := New(
		option.WithBaseURL(server.URL),
		option.WithAPIKey("sk-fake-key-for-testing"),
	)

	resp, err := c.Chat(context.Background(), []provider.Message{
		{Role: provider.RoleUser, Content: "Are you done?"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	if resp.StopReason != "end" {
		t.Errorf("expected stop reason 'end', got %q", resp.StopReason)
	}
	if len(resp.Message.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.Message.ToolCalls))
	}
}