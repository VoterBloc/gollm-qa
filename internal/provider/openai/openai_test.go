package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

func TestNewFromSpec_ResolvesAlias(t *testing.T) {
	c, err := NewFromSpec("gpt-4o-mini", option.WithAPIKey("sk-fake-fishstick-key"))
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	if c.model != shared.ChatModelGPT4oMini {
		t.Errorf("expected gpt-4o-mini alias to resolve to ChatModelGPT4oMini, got %q", c.model)
	}
	if c.modelSpec != "openai:gpt-4o-mini" {
		t.Errorf("expected modelSpec %q, got %q", "openai:gpt-4o-mini", c.modelSpec)
	}
}

func TestNewFromSpec_PassesUnknownThrough(t *testing.T) {
	// Unknown names are not an error — they pass to the API verbatim,
	// which lets new models work without a code change.
	c, err := NewFromSpec("gpt-experimental-bigfoot-detector", option.WithAPIKey("sk-fake-key"))
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	if string(c.model) != "gpt-experimental-bigfoot-detector" {
		t.Errorf("expected unknown name to pass through, got %q", c.model)
	}
}

func TestNewFromSpec_RejectsEmpty(t *testing.T) {
	if _, err := NewFromSpec(""); err == nil {
		t.Fatal("expected error for empty model name")
	}
}

func TestToSDKMessages_AllRoles(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a Loch Ness expert who only speaks in haiku."},
		{Role: provider.RoleUser, Content: "Have you seen Nessie lately?"},
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{
				{ID: "call_plesiosaur", Name: "scan_loch", Arguments: `{"depth": "deep"}`},
			},
		},
		{Role: provider.RoleTool, ToolID: "call_plesiosaur", Content: `{"sighting": false}`},
	}

	sdkMsgs := toSDKMessages(messages)

	if len(sdkMsgs) != 4 {
		t.Fatalf("expected 4 SDK messages, got %d", len(sdkMsgs))
	}

	// Round-trip through JSON to verify each role lands in the right param shape.
	type wireShape struct {
		Role       string `json:"role"`
		Content    any    `json:"content"`
		ToolCallID string `json:"tool_call_id,omitempty"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls,omitempty"`
	}

	roles := []string{"system", "user", "assistant", "tool"}
	for i, want := range roles {
		raw, err := json.Marshal(sdkMsgs[i])
		if err != nil {
			t.Fatalf("marshaling message %d: %v", i, err)
		}
		var got wireShape
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal message %d: %v", i, err)
		}
		if got.Role != want {
			t.Errorf("message %d: role = %q, want %q (raw=%s)", i, got.Role, want, raw)
		}
	}

	// The assistant turn should carry the tool call name + args verbatim.
	rawAsst, _ := json.Marshal(sdkMsgs[2])
	var asst wireShape
	_ = json.Unmarshal(rawAsst, &asst)
	if len(asst.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call on assistant message, got %d (raw=%s)", len(asst.ToolCalls), rawAsst)
	}
	if asst.ToolCalls[0].Function.Name != "scan_loch" {
		t.Errorf("expected function name 'scan_loch', got %q", asst.ToolCalls[0].Function.Name)
	}
	if asst.ToolCalls[0].Function.Arguments != `{"depth": "deep"}` {
		t.Errorf("expected arguments preserved, got %q", asst.ToolCalls[0].Function.Arguments)
	}

	// Tool result should reference the call id.
	rawTool, _ := json.Marshal(sdkMsgs[3])
	var tool wireShape
	_ = json.Unmarshal(rawTool, &tool)
	if tool.ToolCallID != "call_plesiosaur" {
		t.Errorf("expected tool_call_id linkage, got %q (raw=%s)", tool.ToolCallID, rawTool)
	}
}

func TestToSDKMessages_AssistantWithEmptyArgsBecomesEmptyObject(t *testing.T) {
	// Some upstream providers stamp empty Arguments on tool calls; OpenAI
	// requires the field to be a parseable JSON string. We coerce to "{}"
	// so a forwarded conversation doesn't fail validation.
	messages := []provider.Message{
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{
				{ID: "call_yeti", Name: "look_around", Arguments: ""},
			},
		},
	}
	sdkMsgs := toSDKMessages(messages)

	raw, err := json.Marshal(sdkMsgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"arguments":"{}"`) {
		t.Errorf("expected empty arguments coerced to '{}', raw=%s", raw)
	}
}

func TestToSDKTools_ConvertsCorrectly(t *testing.T) {
	tools := []provider.Tool{
		{
			Name:        "summon_cryptid",
			Description: "Summons a cryptid for questioning.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"species": map[string]any{
						"type":        "string",
						"description": "Which cryptid to summon.",
					},
				},
				"required": []string{"species"},
			},
		},
	}

	sdkTools := toSDKTools(tools)

	if len(sdkTools) != 1 {
		t.Fatalf("expected 1 SDK tool, got %d", len(sdkTools))
	}
	if sdkTools[0].Function.Name != "summon_cryptid" {
		t.Errorf("expected tool name 'summon_cryptid', got %q", sdkTools[0].Function.Name)
	}
}

func TestMapFinishReason(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"stop", "end"},
		{"tool_calls", "tool_use"},
		{"length", "length"},
		{"content_filter", "content_filter"}, // unfamiliar reasons pass through verbatim
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := mapFinishReason(tc.in); got != tc.want {
				t.Errorf("mapFinishReason(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestChat_EndToEnd(t *testing.T) {
	// Mock the OpenAI API with a canned tool-call response.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":      "chatcmpl-fishsticks",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "gpt-4o-2024-11-20",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "I'll join the Bigfoot Appreciation Society.",
						"tool_calls": []map[string]any{
							{
								"id":   "call_sasquatch_42",
								"type": "function",
								"function": map[string]any{
									"name":      "join_bloc",
									"arguments": `{"bloc_id":"bigfoot-appreciation-99"}`,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     42,
				"completion_tokens": 67,
				"total_tokens":      109,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c, err := NewFromSpec("gpt-4o",
		option.WithBaseURL(server.URL),
		option.WithAPIKey("sk-fake-fishstick-key"),
	)
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}

	resp, err := c.Chat(context.Background(),
		[]provider.Message{
			{Role: provider.RoleSystem, Content: "You are a friendly golem."},
			{Role: provider.RoleUser, Content: "Join a bloc."},
		},
		[]provider.Tool{
			{
				Name:        "join_bloc",
				Description: "Join a voter bloc.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"bloc_id": map[string]any{"type": "string"}},
					"required":   []string{"bloc_id"},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if resp.Message.Role != provider.RoleAssistant {
		t.Errorf("expected assistant role, got %q", resp.Message.Role)
	}
	if resp.Message.Content != "I'll join the Bigfoot Appreciation Society." {
		t.Errorf("unexpected content: %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}
	if resp.Message.ToolCalls[0].Name != "join_bloc" {
		t.Errorf("expected tool 'join_bloc', got %q", resp.Message.ToolCalls[0].Name)
	}
	if resp.Message.ToolCalls[0].ID != "call_sasquatch_42" {
		t.Errorf("expected id 'call_sasquatch_42', got %q", resp.Message.ToolCalls[0].ID)
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
	if resp.Usage.ModelID != "openai:gpt-4o" {
		t.Errorf("expected ModelID 'openai:gpt-4o', got %q", resp.Usage.ModelID)
	}
}

func TestChat_TextOnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":      "chatcmpl-waffles",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "gpt-4o-2024-11-20",
			"choices": []map[string]any{
				{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "I've completed all my goals. Time for a nap."},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 15,
				"total_tokens":      115,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c, err := NewFromSpec("gpt-4o",
		option.WithBaseURL(server.URL),
		option.WithAPIKey("sk-fake-fishstick-key"),
	)
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}

	resp, err := c.Chat(context.Background(), []provider.Message{
		{Role: provider.RoleUser, Content: "Are you done?"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if resp.StopReason != "end" {
		t.Errorf("expected stop reason 'end', got %q", resp.StopReason)
	}
	if len(resp.Message.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.Message.ToolCalls))
	}
}

