package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/VoterBloc/gollm-qa/internal/driver"
	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// mockProvider returns canned responses in sequence.
type mockProvider struct {
	responses []*provider.Response
	callIndex int
}

func (m *mockProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool) (*provider.Response, error) {
	if m.callIndex >= len(m.responses) {
		return &provider.Response{
			Message:    provider.Message{Role: provider.RoleAssistant, Content: "I'm done, nothing left to do."},
			StopReason: "end",
			Usage:      provider.Usage{InputTokens: 10, OutputTokens: 5},
		}, nil
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

// errorProvider always returns an error.
type errorProvider struct{}

func (e *errorProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool) (*provider.Response, error) {
	return nil, fmt.Errorf("the golem's brain exploded")
}

// mockDriver records tool executions and returns canned results.
type mockDriver struct {
	tools    []provider.Tool
	calls    []provider.ToolCall
	results  map[string]*provider.ToolResult
}

func newMockDriver() *mockDriver {
	return &mockDriver{
		tools: []provider.Tool{
			{
				Name:        "browse_blocs",
				Description: "Browse available blocs.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
			{
				Name:        "join_bloc",
				Description: "Join a bloc.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"bloc_id": map[string]any{"type": "string"},
					},
				},
			},
		},
		results: map[string]*provider.ToolResult{
			"browse_blocs": {Content: `[{"id": "ufo-watchers-69", "name": "UFO Watchers of Nevada"}]`},
			"join_bloc":    {Content: `{"joined": true}`},
		},
	}
}

func (m *mockDriver) Tools() []provider.Tool { return m.tools }

func (m *mockDriver) Execute(_ context.Context, call provider.ToolCall) (*provider.ToolResult, error) {
	m.calls = append(m.calls, call)
	if result, ok := m.results[call.Name]; ok {
		return &provider.ToolResult{ToolID: call.ID, Content: result.Content, IsError: result.IsError}, nil
	}
	return &provider.ToolResult{ToolID: call.ID, Content: "unknown tool", IsError: true}, nil
}

func (m *mockDriver) Close() error { return nil }

// Verify mockDriver implements the interface.
var _ driver.Driver = (*mockDriver)(nil)

func testPersona() *Persona {
	return &Persona{
		Name:        "Cornelius McMuffin",
		Description: "A retired conspiracy theorist from Roswell, NM who now channels his energy into local politics.",
		Goals:       []string{"Find a bloc to join", "Post something controversial"},
		Behavior:    BehaviorEngaged,
		Tags:        map[string]string{"state": "NM", "tinfoil_hat": "yes"},
	}
}

func TestAgent_StopsWhenNoToolCalls(t *testing.T) {
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "I looked around and there's nothing to do here."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 50, OutputTokens: 20},
			},
		},
	}

	a := New(testPersona(), prov, newMockDriver(), DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if session.StopReason != "goals_complete" {
		t.Errorf("expected stop reason 'goals_complete', got %q", session.StopReason)
	}
	if session.Steps != 1 {
		t.Errorf("expected 1 step, got %d", session.Steps)
	}
	if session.TokensIn != 50 {
		t.Errorf("expected 50 input tokens, got %d", session.TokensIn)
	}
}

func TestAgent_ExecutesToolCalls(t *testing.T) {
	drv := newMockDriver()
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message: provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{ID: "toolu_abcdef", Name: "browse_blocs", Arguments: "{}"},
					},
				},
				StopReason: "tool_use",
				Usage:      provider.Usage{InputTokens: 100, OutputTokens: 30},
			},
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Found the UFO Watchers! Looks perfect."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 150, OutputTokens: 15},
			},
		},
	}

	a := New(testPersona(), prov, drv, DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(drv.calls) != 1 {
		t.Fatalf("expected 1 driver call, got %d", len(drv.calls))
	}
	if drv.calls[0].Name != "browse_blocs" {
		t.Errorf("expected tool call 'browse_blocs', got %q", drv.calls[0].Name)
	}
	if len(session.Actions) != 1 {
		t.Fatalf("expected 1 action recorded, got %d", len(session.Actions))
	}
	if session.Actions[0].ToolName != "browse_blocs" {
		t.Errorf("expected action 'browse_blocs', got %q", session.Actions[0].ToolName)
	}
	if session.TokensIn != 250 {
		t.Errorf("expected 250 total input tokens, got %d", session.TokensIn)
	}
	if session.Steps != 2 {
		t.Errorf("expected 2 steps, got %d", session.Steps)
	}
}

func TestAgent_StopsAtStepLimit(t *testing.T) {
	// Provider always returns a tool call — agent should stop at MaxSteps.
	prov := &mockProvider{
		responses: make([]*provider.Response, 100), // plenty of responses
	}
	for i := range prov.responses {
		prov.responses[i] = &provider.Response{
			Message: provider.Message{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{
					{ID: fmt.Sprintf("toolu_%d", i), Name: "browse_blocs", Arguments: "{}"},
				},
			},
			StopReason: "tool_use",
			Usage:      provider.Usage{InputTokens: 10, OutputTokens: 5},
		}
	}

	cfg := Config{MaxSteps: 3}
	a := New(testPersona(), prov, newMockDriver(), cfg, nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if session.StopReason != "step_limit" {
		t.Errorf("expected stop reason 'step_limit', got %q", session.StopReason)
	}
	if session.Steps != 3 {
		t.Errorf("expected 3 steps, got %d", session.Steps)
	}
}

func TestAgent_StopsOnProviderError(t *testing.T) {
	a := New(testPersona(), &errorProvider{}, newMockDriver(), DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if session.StopReason != "error" {
		t.Errorf("expected stop reason 'error', got %q", session.StopReason)
	}
	if len(session.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(session.Errors))
	}
	if session.Errors[0].Message != "provider error: the golem's brain exploded" {
		t.Errorf("unexpected error message: %q", session.Errors[0].Message)
	}
}

func TestAgent_StopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	a := New(testPersona(), &mockProvider{}, newMockDriver(), DefaultConfig(), nil)
	session, err := a.Run(ctx)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if session.StopReason != "cancelled" {
		t.Errorf("expected stop reason 'cancelled', got %q", session.StopReason)
	}
}

func TestAgent_BuiltinToolsIncluded(t *testing.T) {
	// Verify that the agent adds builtin tools to the driver's tools.
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Done."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 10, OutputTokens: 5},
			},
		},
	}

	a := New(testPersona(), prov, newMockDriver(), DefaultConfig(), nil)
	builtins := a.builtinTools()

	names := make(map[string]bool)
	for _, t := range builtins {
		names[t.Name] = true
	}

	if !names["report_ux_observation"] {
		t.Error("expected builtin tool 'report_ux_observation'")
	}
	if !names["mark_goal_complete"] {
		t.Error("expected builtin tool 'mark_goal_complete'")
	}

	// Run to make sure builtins don't break anything.
	_, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

func TestAgent_HandlesMultipleToolCallsInOneStep(t *testing.T) {
	drv := newMockDriver()
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message: provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{ID: "toolu_1", Name: "browse_blocs", Arguments: "{}"},
						{ID: "toolu_2", Name: "join_bloc", Arguments: `{"bloc_id": "lizard-people-anonymous-7"}`},
					},
				},
				StopReason: "tool_use",
				Usage:      provider.Usage{InputTokens: 100, OutputTokens: 40},
			},
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "All done!"},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 200, OutputTokens: 10},
			},
		},
	}

	a := New(testPersona(), prov, drv, DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(drv.calls) != 2 {
		t.Fatalf("expected 2 driver calls, got %d", len(drv.calls))
	}
	if len(session.Actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(session.Actions))
	}
	if session.Actions[0].ToolName != "browse_blocs" {
		t.Errorf("expected first action 'browse_blocs', got %q", session.Actions[0].ToolName)
	}
	if session.Actions[1].ToolName != "join_bloc" {
		t.Errorf("expected second action 'join_bloc', got %q", session.Actions[1].ToolName)
	}
}

func TestAgent_InitGoals(t *testing.T) {
	persona := testPersona()
	a := New(persona, &mockProvider{}, newMockDriver(), DefaultConfig(), nil)
	goals := a.initGoals()

	if len(goals) != 2 {
		t.Fatalf("expected 2 goals, got %d", len(goals))
	}
	if goals[0].Goal != "Find a bloc to join" {
		t.Errorf("unexpected goal: %q", goals[0].Goal)
	}
	if goals[0].Achieved {
		t.Error("goal should not be achieved initially")
	}
}

func TestPersona_SystemPrompt(t *testing.T) {
	p := testPersona()
	prompt := p.SystemPrompt()

	// Should contain the persona name.
	if !contains(prompt, "Cornelius McMuffin") {
		t.Error("system prompt should contain persona name")
	}
	// Should contain goals.
	if !contains(prompt, "Find a bloc to join") {
		t.Error("system prompt should contain goals")
	}
	// Should contain behavior description for engaged.
	if !contains(prompt, "active, engaged user") {
		t.Error("system prompt should describe engaged behavior")
	}
	// Should contain the freeform description.
	if !contains(prompt, "retired conspiracy theorist") {
		t.Error("system prompt should contain persona description")
	}
}

func TestPersona_SystemPromptBehaviors(t *testing.T) {
	tests := []struct {
		behavior Behavior
		contains string
	}{
		{BehaviorEngaged, "active, engaged user"},
		{BehaviorLurker, "mostly observe"},
		{BehaviorModerate, "regular but not obsessive"},
		{"", "interact naturally"},
	}

	for _, tt := range tests {
		t.Run(string(tt.behavior), func(t *testing.T) {
			p := &Persona{Name: "Test", Behavior: tt.behavior}
			prompt := p.SystemPrompt()
			if !contains(prompt, tt.contains) {
				t.Errorf("expected prompt to contain %q for behavior %q", tt.contains, tt.behavior)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}