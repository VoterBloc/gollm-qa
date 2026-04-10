// Package provider defines the LLM provider interface and message types.
// Supports multiple backends (Claude, GPT, etc.) through a common interface.
package provider

import "context"

// Role identifies the sender of a message in a conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a single message in the conversation between the agent and the LLM.
type Message struct {
	Role      Role       `json:"role"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolID    string     `json:"tool_id,omitempty"` // set when Role == RoleTool
}

// Tool describes a function the LLM can call.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters is the JSON Schema for the tool's input.
	Parameters map[string]any `json:"parameters"`
}

// ToolCall is the LLM's request to invoke a tool.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON string
}

// ToolResult is the outcome of executing a tool call.
type ToolResult struct {
	ToolID  string `json:"tool_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// Response is what the provider returns from a Chat call.
type Response struct {
	Message    Message `json:"message"`
	StopReason string  `json:"stop_reason"` // "end", "tool_use", "length"
	Usage      Usage   `json:"usage"`
}

// Usage tracks token consumption for a single request.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Provider is the interface that LLM backends implement.
type Provider interface {
	// Chat sends a conversation with available tools and returns the model's response.
	Chat(ctx context.Context, messages []Message, tools []Tool) (*Response, error)
}