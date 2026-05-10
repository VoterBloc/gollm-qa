package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

func TestNewFromSpec_ResolvesAlias(t *testing.T) {
	g, err := NewFromSpec("2.5-flash", WithAPIKey("sk-fake-yowie-key"))
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	if g.model != "gemini-2.5-flash" {
		t.Errorf("expected 2.5-flash alias to resolve to gemini-2.5-flash, got %q", g.model)
	}
	if g.modelSpec != "gemini:2.5-flash" {
		t.Errorf("expected modelSpec %q, got %q", "gemini:2.5-flash", g.modelSpec)
	}
}

func TestNewFromSpec_PassesUnknownThrough(t *testing.T) {
	// Unknown names pass to the API verbatim — new releases work
	// without a code change.
	g, err := NewFromSpec("gemini-experimental-bunyip-detector", WithAPIKey("sk-fake-key"))
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	if g.model != "gemini-experimental-bunyip-detector" {
		t.Errorf("expected unknown name to pass through, got %q", g.model)
	}
}

func TestNewFromSpec_RejectsEmpty(t *testing.T) {
	if _, err := NewFromSpec(""); err == nil {
		t.Fatal("expected error for empty model name")
	}
}

func TestToSDKContents_AllRoles(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a Loch Ness expert who only speaks in haiku."},
		{Role: provider.RoleUser, Content: "Have you seen Nessie lately?"},
		{
			Role:    provider.RoleAssistant,
			Content: "Scanning the loch...",
			ToolCalls: []provider.ToolCall{
				{ID: "call_plesiosaur", Name: "scan_loch", Arguments: `{"depth": "deep"}`},
			},
		},
		{Role: provider.RoleTool, ToolID: "call_plesiosaur", Content: `{"sighting": false}`},
	}

	contents, sys := toSDKContents(messages)

	if sys != "You are a Loch Ness expert who only speaks in haiku." {
		t.Errorf("expected system instruction extracted, got %q", sys)
	}
	// 3 non-system messages → 3 contents (user / model / tool-as-user).
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(contents))
	}

	// User turn.
	if contents[0].Role != "user" || len(contents[0].Parts) != 1 || contents[0].Parts[0].Text != "Have you seen Nessie lately?" {
		t.Errorf("user turn wrong: role=%q parts=%+v", contents[0].Role, contents[0].Parts)
	}

	// Assistant turn — text + function call.
	if contents[1].Role != "model" {
		t.Errorf("assistant turn should map to role=model, got %q", contents[1].Role)
	}
	if len(contents[1].Parts) != 2 {
		t.Fatalf("assistant turn should have 2 parts (text + function call), got %d", len(contents[1].Parts))
	}
	if contents[1].Parts[0].Text != "Scanning the loch..." {
		t.Errorf("expected text part first, got %+v", contents[1].Parts[0])
	}
	fc := contents[1].Parts[1].FunctionCall
	if fc == nil || fc.Name != "scan_loch" {
		t.Fatalf("expected function call 'scan_loch', got %+v", fc)
	}
	if fc.ID != "call_plesiosaur" {
		t.Errorf("expected function call id 'call_plesiosaur', got %q", fc.ID)
	}
	if fc.Args["depth"] != "deep" {
		t.Errorf("expected function call args to carry depth=deep, got %+v", fc.Args)
	}

	// Tool result — comes back as a user-role content with a
	// FunctionResponse part.
	if contents[2].Role != "user" {
		t.Errorf("tool result should map to role=user, got %q", contents[2].Role)
	}
	fr := contents[2].Parts[0].FunctionResponse
	if fr == nil {
		t.Fatal("expected FunctionResponse part on tool turn")
	}
	if fr.ID != "call_plesiosaur" {
		t.Errorf("expected FunctionResponse id 'call_plesiosaur', got %q", fr.ID)
	}
	if fr.Response["output"] != `{"sighting": false}` {
		t.Errorf("expected response output to be wrapped, got %+v", fr.Response)
	}
}

func TestToSDKContents_AssistantEmptyArgsBecomesEmptyMap(t *testing.T) {
	// Empty Arguments string → empty map, not nil. Keeps the wire
	// shape consistent regardless of whether the upstream provider
	// stamped args at all.
	messages := []provider.Message{
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{
				{ID: "call_yeti", Name: "look_around", Arguments: ""},
			},
		},
	}
	contents, _ := toSDKContents(messages)
	if len(contents) != 1 || len(contents[0].Parts) != 1 {
		t.Fatalf("unexpected shape: %+v", contents)
	}
	fc := contents[0].Parts[0].FunctionCall
	if fc == nil {
		t.Fatal("expected FunctionCall part")
	}
	if fc.Args == nil {
		t.Error("expected empty (non-nil) map for empty Arguments, got nil")
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
		t.Fatalf("expected 1 wrapping Tool, got %d", len(sdkTools))
	}
	if len(sdkTools[0].FunctionDeclarations) != 1 {
		t.Fatalf("expected 1 function declaration, got %d", len(sdkTools[0].FunctionDeclarations))
	}
	decl := sdkTools[0].FunctionDeclarations[0]
	if decl.Name != "summon_cryptid" {
		t.Errorf("expected function name 'summon_cryptid', got %q", decl.Name)
	}
	if decl.Description != "Summons a cryptid for questioning." {
		t.Errorf("expected description preserved, got %q", decl.Description)
	}
	if decl.ParametersJsonSchema == nil {
		t.Error("expected ParametersJsonSchema set on declaration")
	}
}

func TestMapFinishReason(t *testing.T) {
	cases := []struct {
		in   genai.FinishReason
		want string
	}{
		{genai.FinishReasonStop, "end"},
		{"", "end"}, // empty also maps to "end"
		{genai.FinishReasonMaxTokens, "length"},
		{"SAFETY", "SAFETY"}, // unfamiliar reasons pass through verbatim
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			if got := mapFinishReason(tc.in); got != tc.want {
				t.Errorf("mapFinishReason(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestChat_EndToEnd(t *testing.T) {
	// Mock Gemini's REST API with a canned tool-call response.
	// The Gemini path is something like
	// /v1beta/models/<model>:generateContent — we accept anything
	// under /v1beta/models/ and return the canned body.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":generateContent") {
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"role": "model",
						"parts": []map[string]any{
							{"text": "I'll join the Bigfoot Appreciation Society."},
							{
								"functionCall": map[string]any{
									"id":   "call_sasquatch_42",
									"name": "join_bloc",
									"args": map[string]any{"bloc_id": "bigfoot-appreciation-99"},
								},
							},
						},
					},
					"finishReason": "STOP",
					"index":        0,
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":     42,
				"candidatesTokenCount": 67,
				"totalTokenCount":      109,
			},
			"modelVersion": "gemini-2.5-pro-001",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	g, err := NewFromSpec("2.5-pro",
		WithBaseURL(server.URL),
		WithAPIKey("sk-fake-yowie-key"),
	)
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}

	resp, err := g.Chat(context.Background(),
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
	// STOP + tool call → tool_use, not end.
	if resp.StopReason != "tool_use" {
		t.Errorf("expected stop reason 'tool_use' (STOP + tool call), got %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 42 {
		t.Errorf("expected 42 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 67 {
		t.Errorf("expected 67 output tokens, got %d", resp.Usage.OutputTokens)
	}
	if resp.Usage.ModelID != "gemini:2.5-pro" {
		t.Errorf("expected ModelID 'gemini:2.5-pro', got %q", resp.Usage.ModelID)
	}
}

func TestChat_TextOnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"role": "model",
						"parts": []map[string]any{
							{"text": "I've completed all my goals. Time for a nap."},
						},
					},
					"finishReason": "STOP",
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":     100,
				"candidatesTokenCount": 15,
				"totalTokenCount":      115,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	g, err := NewFromSpec("2.5-pro",
		WithBaseURL(server.URL),
		WithAPIKey("sk-fake-yowie-key"),
	)
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}

	resp, err := g.Chat(context.Background(), []provider.Message{
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
