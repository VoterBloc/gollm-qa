package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

func TestResolveBaseURL(t *testing.T) {
	cases := []struct {
		name     string
		explicit string
		envHost  string
		want     string
	}{
		{"default everything", "", "", "http://localhost:11434/v1"},
		{"explicit URL wins", "http://elsewhere:8000", "http://env:9000", "http://elsewhere:8000/v1"},
		{"OLLAMA_HOST when no explicit", "", "http://ollama-host:7000", "http://ollama-host:7000/v1"},
		{"trailing slash stripped before /v1", "http://host:11434/", "", "http://host:11434/v1"},
		{"already has /v1 — idempotent", "http://host:11434/v1", "", "http://host:11434/v1"},
		{"already has /v1 with trailing slash", "http://host:11434/v1/", "", "http://host:11434/v1"},
		{"non-v1 versioned path left alone", "http://host:11434/v1beta", "", "http://host:11434/v1beta"},
		{"versioned path with digit suffix", "http://host:11434/v2alpha", "", "http://host:11434/v2alpha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envHost == "" {
				t.Setenv("OLLAMA_HOST", "")
			} else {
				t.Setenv("OLLAMA_HOST", tc.envHost)
			}
			if got := resolveBaseURL(tc.explicit); got != tc.want {
				t.Errorf("resolveBaseURL(%q) with OLLAMA_HOST=%q = %q, want %q",
					tc.explicit, tc.envHost, got, tc.want)
			}
		})
	}
}

func TestNewFromSpec_RejectsEmpty(t *testing.T) {
	if _, err := NewFromSpec(""); err == nil {
		t.Fatal("expected error for empty model name")
	}
}

func TestResolveAPIKey(t *testing.T) {
	cases := []struct {
		name     string
		explicit string
		envKey   string
		want     string
	}{
		{"defaults to placeholder", "", "", placeholderAPIKey},
		{"WithAPIKey wins outright", "sk-yowie-key", "sk-env-key", "sk-yowie-key"},
		{"OLLAMA_API_KEY when no explicit", "", "sk-env-key", "sk-env-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OLLAMA_API_KEY", tc.envKey)
			if got := resolveAPIKey(tc.explicit); got != tc.want {
				t.Errorf("resolveAPIKey(%q) with OLLAMA_API_KEY=%q = %q, want %q",
					tc.explicit, tc.envKey, got, tc.want)
			}
		})
	}
}

func TestNewFromSpec_StampsLocalPrefixOnSpec(t *testing.T) {
	// Confirms the wrapper's reason for existing: the inner openai
	// provider stamps "openai:<model>", but we want "local:<model>"
	// so cost accounting bills against the (zero-priced) local entry.
	l, err := NewFromSpec("llama3.1")
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	if l.modelSpec != "local:llama3.1" {
		t.Errorf("modelSpec = %q, want %q", l.modelSpec, "local:llama3.1")
	}
}

func TestChat_OverridesModelIDInUsage(t *testing.T) {
	// Mock an Ollama-flavored chat.completions response. The inner
	// openai provider would stamp Usage.ModelID = "openai:llama3.1";
	// Local.Chat overrides it to "local:llama3.1" so cost-table
	// lookup hits our zero-priced entry.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The OpenAI SDK posts to /v1/chat/completions; the local
		// wrapper appended the /v1 path itself, so the SDK's path
		// joins to /v1/chat/completions against our test base URL.
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{
			"id":      "chatcmpl-drop-bear",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "llama3.1",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "G'day mate, no Ollama charges here.",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     20,
				"completion_tokens": 8,
				"total_tokens":      28,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	l, err := NewFromSpec("llama3.1", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}

	out, err := l.Chat(context.Background(), []provider.Message{
		{Role: provider.RoleUser, Content: "Howdy."},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if out.Usage.ModelID != "local:llama3.1" {
		t.Errorf("Usage.ModelID = %q, want %q (override should beat inner openai stamping)",
			out.Usage.ModelID, "local:llama3.1")
	}
	if out.Message.Content != "G'day mate, no Ollama charges here." {
		t.Errorf("content not forwarded through: %q", out.Message.Content)
	}
}

func TestChat_ToolCallRoundTrip(t *testing.T) {
	// Tool-capable model. The OpenAI SDK's wire shape is what Ollama
	// emits, so this exercises the full pipeline.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{
			"id":      "chatcmpl-yowietool",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "llama3.1",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{
							{
								"id":   "call_yowie_tracker",
								"type": "function",
								"function": map[string]any{
									"name":      "scan_outback",
									"arguments": `{"region":"Daintree"}`,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 25,
				"total_tokens":      125,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	l, err := NewFromSpec("llama3.1", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}

	out, err := l.Chat(context.Background(),
		[]provider.Message{{Role: provider.RoleUser, Content: "Find a yowie."}},
		[]provider.Tool{
			{
				Name:        "scan_outback",
				Description: "Scans an Australian region for cryptid sightings.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"region": map[string]any{"type": "string"}},
					"required":   []string{"region"},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if len(out.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(out.Message.ToolCalls))
	}
	if out.Message.ToolCalls[0].Name != "scan_outback" {
		t.Errorf("tool name = %q, want scan_outback", out.Message.ToolCalls[0].Name)
	}
	if out.Message.ToolCalls[0].Arguments != `{"region":"Daintree"}` {
		t.Errorf("arguments not preserved: %q", out.Message.ToolCalls[0].Arguments)
	}
	if out.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use (openai converter maps tool_calls)", out.StopReason)
	}
	if out.Usage.ModelID != "local:llama3.1" {
		t.Errorf("Usage.ModelID = %q, want local:llama3.1", out.Usage.ModelID)
	}
}

func TestChat_TextOnlyFromNonToolModel(t *testing.T) {
	// Non-tool-capable local models return plain text — the agent
	// loop reads that as "no more tool calls → goals_complete." We
	// don't need special handling in this layer, but pin the
	// behavior so a future refactor doesn't accidentally start
	// inventing tool calls.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"id":      "chatcmpl-bigfootchat",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "phi3",
			"choices": []map[string]any{
				{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "I cannot use tools, but here's my answer."},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     50,
				"completion_tokens": 12,
				"total_tokens":      62,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	l, err := NewFromSpec("phi3", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}

	out, err := l.Chat(context.Background(), []provider.Message{
		{Role: provider.RoleUser, Content: "Ahoy."},
	}, []provider.Tool{
		// Tools offered but model returns text — degrades gracefully.
		{Name: "irrelevant", Description: "", Parameters: map[string]any{"type": "object"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if len(out.Message.ToolCalls) != 0 {
		t.Errorf("expected no tool calls from non-tool model, got %d", len(out.Message.ToolCalls))
	}
	if out.StopReason != "end" {
		t.Errorf("StopReason = %q, want end", out.StopReason)
	}
}
