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

// authMockDriver is a mockDriver that also implements Authenticator and
// Registrar. Tests can leave the register fields zero-valued to exercise
// login-only paths.
type authMockDriver struct {
	mockDriver
	loginCalled    bool
	loginErr       error
	registerCalled bool
	registerInput  map[string]any
	registerErr    error
}

func (m *authMockDriver) Login(_ context.Context, identifier, password string) error {
	m.loginCalled = true
	return m.loginErr
}

func (m *authMockDriver) Register(_ context.Context, input map[string]any) error {
	m.registerCalled = true
	m.registerInput = input
	return m.registerErr
}

func testPersona() *Persona {
	return &Persona{
		Name:        "Cornelius McMuffin",
		Description: "A retired conspiracy theorist from Roswell, NM who now channels his energy into local politics.",
		Goals:       []string{"Find a bloc to join", "Post something controversial"},
		Behavior:    BehaviorEngaged,
		Tags:        map[string]string{"state": "NM", "tinfoil_hat": "yes"},
	}
}

func testPersonaWithCreds() *Persona {
	p := testPersona()
	p.Credentials = Credentials{
		Identifier: "cornelius@lizardtruth.net",
		Password:   "Tr00thS33k3r!",
	}
	return p
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

func TestAgent_AuthenticatesBeforeLoop(t *testing.T) {
	drv := &authMockDriver{mockDriver: *newMockDriver()}
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Logged in and ready."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 10, OutputTokens: 5},
			},
		},
	}

	a := New(testPersonaWithCreds(), prov, drv, DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if !drv.loginCalled {
		t.Error("expected Login() to be called")
	}
	if session.StopReason != "goals_complete" {
		t.Errorf("expected stop reason 'goals_complete', got %q", session.StopReason)
	}
}

func TestAgent_AuthFailureStopsRun(t *testing.T) {
	drv := &authMockDriver{
		mockDriver: *newMockDriver(),
		loginErr:   fmt.Errorf("your credentials are as fake as the moon landing"),
	}

	a := New(testPersonaWithCreds(), &mockProvider{}, drv, DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if session.StopReason != "auth_failed" {
		t.Errorf("expected stop reason 'auth_failed', got %q", session.StopReason)
	}
	if len(session.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(session.Errors))
	}
	if !contains(session.Errors[0].Message, "moon landing") {
		t.Errorf("expected error message about moon landing, got: %q", session.Errors[0].Message)
	}
}

func TestAgent_SkipsAuthWithoutCredentials(t *testing.T) {
	drv := &authMockDriver{mockDriver: *newMockDriver()}
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "No creds, no problem."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 10, OutputTokens: 5},
			},
		},
	}

	// testPersona() has no credentials set.
	a := New(testPersona(), prov, drv, DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if drv.loginCalled {
		t.Error("Login() should not be called when persona has no credentials")
	}
	if session.StopReason != "goals_complete" {
		t.Errorf("expected stop reason 'goals_complete', got %q", session.StopReason)
	}
}

func TestAgent_RegistersBeforeLogin(t *testing.T) {
	drv := &authMockDriver{mockDriver: *newMockDriver()}
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Registered, logged in, ready to find Bigfoot."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 10, OutputTokens: 5},
			},
		},
	}

	persona := testPersonaWithCreds()
	persona.RegisterInput = map[string]any{
		"email":     "cornelius@lizardtruth.net",
		"username":  "cornelius_mcmuffin",
		"password":  "Tr00thS33k3r!",
		"firstName": "Cornelius",
		"lastName":  "McMuffin",
	}

	a := New(persona, prov, drv, DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if !drv.registerCalled {
		t.Error("expected Register() to be called when persona has register_input")
	}
	if !drv.loginCalled {
		t.Error("expected Login() to be called after Register when credentials are also set")
	}
	if drv.registerInput["email"] != "cornelius@lizardtruth.net" {
		t.Errorf("expected register input email to be passed through, got %v", drv.registerInput["email"])
	}
	if session.StopReason != "goals_complete" {
		t.Errorf("expected stop reason 'goals_complete', got %q", session.StopReason)
	}
}

func TestAgent_RegisterOnlyNoCreds(t *testing.T) {
	drv := &authMockDriver{mockDriver: *newMockDriver()}
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Just registered, the register endpoint gave me a token."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 10, OutputTokens: 5},
			},
		},
	}

	persona := testPersona() // no credentials
	persona.RegisterInput = map[string]any{
		"email":    "yeti@himalaya.example",
		"username": "yeti_mountaineer",
		"password": "AbominableSnow1!",
	}

	a := New(persona, prov, drv, DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if !drv.registerCalled {
		t.Error("expected Register() to be called")
	}
	if drv.loginCalled {
		t.Error("Login() should not be called when persona has no credentials")
	}
	if session.StopReason != "goals_complete" {
		t.Errorf("expected stop reason 'goals_complete', got %q", session.StopReason)
	}
}

func TestAgent_RegisterFailureStopsRun(t *testing.T) {
	drv := &authMockDriver{
		mockDriver:  *newMockDriver(),
		registerErr: fmt.Errorf("email already in use by a confirmed cryptid"),
	}

	persona := testPersonaWithCreds()
	persona.RegisterInput = map[string]any{
		"email":    "duplicate@cryptid.example",
		"username": "duplicate_dan",
	}

	a := New(persona, &mockProvider{}, drv, DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if !drv.registerCalled {
		t.Error("expected Register() to be called")
	}
	if drv.loginCalled {
		t.Error("Login() should not be called after Register fails")
	}
	if session.StopReason != "register_failed" {
		t.Errorf("expected stop reason 'register_failed', got %q", session.StopReason)
	}
	if len(session.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(session.Errors))
	}
	if !contains(session.Errors[0].Message, "confirmed cryptid") {
		t.Errorf("expected error message about confirmed cryptid, got: %q", session.Errors[0].Message)
	}
}

func TestAgent_NoRegisterInputSkipsRegister(t *testing.T) {
	drv := &authMockDriver{mockDriver: *newMockDriver()}
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Logged in only."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 10, OutputTokens: 5},
			},
		},
	}

	// Persona has credentials but no RegisterInput — login-only path.
	a := New(testPersonaWithCreds(), prov, drv, DefaultConfig(), nil)
	if _, err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if drv.registerCalled {
		t.Error("Register() should not be called when persona has no register_input")
	}
	if !drv.loginCalled {
		t.Error("expected Login() to be called")
	}
}

func TestAgent_UXObservationRecorded(t *testing.T) {
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message: provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{
							ID:        "toolu_ux",
							Name:      "report_ux_observation",
							Arguments: `{"observation": "The submit button was camouflaged as a potato", "severity": "warning"}`,
						},
					},
				},
				StopReason: "tool_use",
				Usage:      provider.Usage{InputTokens: 50, OutputTokens: 20},
			},
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Noted the issue."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 60, OutputTokens: 10},
			},
		},
	}

	a := New(testPersona(), prov, newMockDriver(), DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(session.UXNotes) != 1 {
		t.Fatalf("expected 1 UX note, got %d", len(session.UXNotes))
	}
	if session.UXNotes[0].Observation != "The submit button was camouflaged as a potato" {
		t.Errorf("unexpected observation: %q", session.UXNotes[0].Observation)
	}
	if session.UXNotes[0].Severity != "warning" {
		t.Errorf("expected severity 'warning', got %q", session.UXNotes[0].Severity)
	}
}

func TestAgent_GoalMarkedComplete(t *testing.T) {
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message: provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{
							ID:        "toolu_goal",
							Name:      "mark_goal_complete",
							Arguments: `{"goal": "Find a bloc to join", "notes": "Joined the Lizard People Anonymous bloc"}`,
						},
					},
				},
				StopReason: "tool_use",
				Usage:      provider.Usage{InputTokens: 50, OutputTokens: 20},
			},
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Done!"},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 60, OutputTokens: 5},
			},
		},
	}

	a := New(testPersona(), prov, newMockDriver(), DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	achieved := 0
	for _, g := range session.Goals {
		if g.Achieved {
			achieved++
			if g.Goal != "Find a bloc to join" {
				t.Errorf("wrong goal marked achieved: %q", g.Goal)
			}
			if g.Notes != "Joined the Lizard People Anonymous bloc" {
				t.Errorf("unexpected notes: %q", g.Notes)
			}
		}
	}
	if achieved != 1 {
		t.Errorf("expected 1 achieved goal, got %d", achieved)
	}
}

func TestAgent_GoalFuzzyMatch(t *testing.T) {
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message: provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{
							ID:        "toolu_goal",
							Name:      "mark_goal_complete",
							Arguments: `{"goal": "find a bloc"}`,
						},
					},
				},
				StopReason: "tool_use",
				Usage:      provider.Usage{InputTokens: 50, OutputTokens: 20},
			},
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Done!"},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 60, OutputTokens: 5},
			},
		},
	}

	a := New(testPersona(), prov, newMockDriver(), DefaultConfig(), nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// "find a bloc" should fuzzy-match "Find a bloc to join"
	achieved := false
	for _, g := range session.Goals {
		if g.Goal == "Find a bloc to join" && g.Achieved {
			achieved = true
		}
	}
	if !achieved {
		t.Error("expected fuzzy match to mark 'Find a bloc to join' as achieved")
	}
}

func TestAgent_OnEventFiresThroughLifecycle(t *testing.T) {
	drv := newMockDriver()
	prov := &mockProvider{
		responses: []*provider.Response{
			{
				Message: provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{ID: "toolu_swamp", Name: "browse_blocs", Arguments: "{}"},
					},
				},
				StopReason: "tool_use",
				Usage:      provider.Usage{InputTokens: 80, OutputTokens: 12},
			},
			{
				Message: provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{
							ID:        "toolu_obs",
							Name:      "report_ux_observation",
							Arguments: `{"observation":"the swamp creature filter has no zoom","severity":"warning"}`,
						},
					},
				},
				StopReason: "tool_use",
				Usage:      provider.Usage{InputTokens: 95, OutputTokens: 18},
			},
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "All set."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 110, OutputTokens: 10},
			},
		},
	}

	var events []Event
	cfg := DefaultConfig()
	cfg.OnEvent = func(ev Event) { events = append(events, ev) }

	a := New(testPersona(), prov, drv, cfg, nil)
	session, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if session.Steps == 0 {
		t.Fatalf("expected the agent to take at least one step")
	}

	// First and last events bracket the run.
	if len(events) < 2 {
		t.Fatalf("expected at least session_start and session_end events, got %d", len(events))
	}
	if events[0].Kind != EventSessionStart {
		t.Errorf("first event: want %q, got %q", EventSessionStart, events[0].Kind)
	}
	if events[len(events)-1].Kind != EventSessionEnd {
		t.Errorf("last event: want %q, got %q", EventSessionEnd, events[len(events)-1].Kind)
	}

	// At least one step event with non-empty actions.
	var sawStep bool
	for _, ev := range events {
		if ev.Kind != EventStep {
			continue
		}
		actions, ok := ev.Payload.([]Action)
		if !ok {
			t.Errorf("step event payload: want []Action, got %T", ev.Payload)
			continue
		}
		if len(actions) > 0 {
			sawStep = true
		}
	}
	if !sawStep {
		t.Errorf("expected at least one step event with actions, got events: %v", kinds(events))
	}

	// The UX observation tool call should have produced an observation event
	// with the recorded note.
	var sawObservation bool
	for _, ev := range events {
		if ev.Kind != EventObservation {
			continue
		}
		note, ok := ev.Payload.(UXNote)
		if !ok {
			t.Errorf("observation event payload: want UXNote, got %T", ev.Payload)
			continue
		}
		if note.Severity != "warning" {
			t.Errorf("observation severity: want warning, got %q", note.Severity)
		}
		if !contains(note.Observation, "swamp creature") {
			t.Errorf("observation text: want swamp-creature note, got %q", note.Observation)
		}
		sawObservation = true
	}
	if !sawObservation {
		t.Errorf("expected an observation event from report_ux_observation, got events: %v", kinds(events))
	}

	// The session_end payload is the full Session pointer.
	endEvent := events[len(events)-1]
	if endSession, ok := endEvent.Payload.(*Session); !ok {
		t.Errorf("session_end payload: want *Session, got %T", endEvent.Payload)
	} else if endSession != session {
		t.Errorf("session_end payload should match returned session")
	}
}

func TestAgent_OnEventNilIsAllowed(t *testing.T) {
	// Default config has OnEvent=nil; the agent must not blow up.
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
	if _, err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run() with nil OnEvent: %v", err)
	}
}

func kinds(events []Event) []EventKind {
	out := make([]EventKind, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
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