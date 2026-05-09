// Package agent implements the core agent loop. Each agent has a persona,
// an LLM provider for decision-making, and a driver for interacting with
// the target application.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/VoterBloc/gollm-qa/internal/driver"
	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// Authenticator is an optional interface that drivers can implement
// to support per-agent authentication.
type Authenticator interface {
	Login(ctx context.Context, identifier, password string) error
}

// Registrar is an optional interface that drivers can implement to support
// creating new user accounts. Used in conjunction with persona.RegisterInput
// — when both are present, the agent calls Register before Login.
type Registrar interface {
	Register(ctx context.Context, input map[string]any) error
}

// Config controls how an agent runs.
type Config struct {
	// MaxSteps is the maximum number of tool-call rounds before stopping.
	MaxSteps int

	// StepDelay is how long to wait between steps (pacing).
	StepDelay time.Duration

	// OnEvent, if non-nil, is called synchronously from inside Run as
	// notable things happen (session start/end, each completed step, UX
	// observations, errors). See events.go for the full taxonomy.
	OnEvent EventCallback
}

// emit calls OnEvent if it's set. Stamping At inside this helper keeps
// the call sites concise.
func (a *Agent) emit(kind EventKind, step int, payload any) {
	if a.config.OnEvent == nil {
		return
	}
	a.config.OnEvent(Event{
		Kind:    kind,
		Step:    step,
		At:      time.Now(),
		Payload: payload,
	})
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxSteps:  50,
		StepDelay: 0,
	}
}

// Agent is a single synthetic user that interacts with a target application.
type Agent struct {
	persona  *Persona
	provider provider.Provider
	driver   driver.Driver
	config   Config
	logger   *slog.Logger
}

// New creates an agent with the given persona, LLM provider, and driver.
func New(persona *Persona, llm provider.Provider, drv driver.Driver, cfg Config, logger *slog.Logger) *Agent {
	if logger == nil {
		logger = slog.Default()
	}
	return &Agent{
		persona:  persona,
		provider: llm,
		driver:   drv,
		config:   cfg,
		logger:   logger.With("agent", persona.Name),
	}
}

// Run executes the agent loop and returns the session report.
func (a *Agent) Run(ctx context.Context) (*Session, error) {
	session := &Session{
		AgentName: a.persona.Name,
		StartedAt: time.Now(),
		Goals:     a.initGoals(),
	}
	a.emit(EventSessionStart, 0, nil)

	authenticated := false

	// Register the user first if the persona declares a register_input and the
	// driver supports it. Some apps (e.g. those whose register mutation returns
	// a token) won't need a follow-up Login — that's controlled by the driver
	// config, not the agent.
	if a.persona.RegisterInput != nil {
		if reg, ok := a.driver.(Registrar); ok {
			a.logger.Info("registering")
			if err := reg.Register(ctx, a.persona.RegisterInput); err != nil {
				agentErr := AgentError{
					Step:      0,
					Timestamp: time.Now(),
					Message:   fmt.Sprintf("registration failed: %v", err),
				}
				session.Errors = append(session.Errors, agentErr)
				session.StopReason = "register_failed"
				session.EndedAt = time.Now()
				a.emit(EventError, 0, agentErr)
				a.emit(EventSessionEnd, 0, session)
				return session, nil
			}
			authenticated = true
		}
	}

	// Authenticate if the driver supports it and the persona has credentials.
	if auth, ok := a.driver.(Authenticator); ok && a.persona.Credentials.Identifier != "" {
		a.logger.Info("authenticating", "identifier", a.persona.Credentials.Identifier)
		if err := auth.Login(ctx, a.persona.Credentials.Identifier, a.persona.Credentials.Password); err != nil {
			agentErr := AgentError{
				Step:      0,
				Timestamp: time.Now(),
				Message:   fmt.Sprintf("authentication failed: %v", err),
			}
			session.Errors = append(session.Errors, agentErr)
			session.StopReason = "auth_failed"
			session.EndedAt = time.Now()
			a.emit(EventError, 0, agentErr)
			a.emit(EventSessionEnd, 0, session)
			return session, nil
		}
		authenticated = true
	}

	tools := a.driver.Tools()
	tools = append(tools, a.builtinTools()...)

	startMessage := "Begin working toward your goals. Use the available tools to interact with the application."
	if authenticated {
		startMessage = "You are now logged in. " + startMessage
	}

	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: a.persona.SystemPrompt()},
		{Role: provider.RoleUser, Content: startMessage},
	}

	a.logger.Info("starting agent run", "goals", len(a.persona.Goals), "max_steps", a.config.MaxSteps)

	for step := 1; step <= a.config.MaxSteps; step++ {
		if ctx.Err() != nil {
			session.StopReason = "cancelled"
			break
		}

		if a.config.StepDelay > 0 && step > 1 {
			select {
			case <-time.After(a.config.StepDelay):
			case <-ctx.Done():
				session.StopReason = "cancelled"
				break
			}
		}

		resp, err := a.provider.Chat(ctx, messages, tools)
		if err != nil {
			agentErr := AgentError{
				Step:      step,
				Timestamp: time.Now(),
				Message:   fmt.Sprintf("provider error: %v", err),
			}
			session.Errors = append(session.Errors, agentErr)
			session.StopReason = "error"
			a.emit(EventError, step, agentErr)
			break
		}

		session.TokensIn += resp.Usage.InputTokens
		session.TokensOut += resp.Usage.OutputTokens
		session.Steps = step

		messages = append(messages, resp.Message)

		// No tool calls — the model is done talking.
		if len(resp.Message.ToolCalls) == 0 {
			a.logger.Info("agent finished (no more tool calls)", "step", step)
			session.StopReason = "goals_complete"
			break
		}

		// Execute each tool call and collect results.
		var toolMessages []provider.Message
		var stepActions []Action
		for _, call := range resp.Message.ToolCalls {
			a.logger.Info("executing tool", "step", step, "tool", call.Name)

			start := time.Now()
			result, execErr := a.executeTool(ctx, call, session, step)
			elapsed := time.Since(start)

			action := Action{
				Step:      step,
				Timestamp: start,
				ToolName:  call.Name,
				Arguments: call.Arguments,
				Result:    result.Content,
				IsError:   result.IsError,
				Duration:  elapsed,
			}
			session.Actions = append(session.Actions, action)
			stepActions = append(stepActions, action)

			if execErr != nil {
				agentErr := AgentError{
					Step:      step,
					Timestamp: time.Now(),
					ToolName:  call.Name,
					Message:   execErr.Error(),
				}
				session.Errors = append(session.Errors, agentErr)
				a.emit(EventError, step, agentErr)
			}

			toolMessages = append(toolMessages, provider.Message{
				Role:    provider.RoleTool,
				ToolID:  call.ID,
				Content: result.Content,
			})
		}
		messages = append(messages, toolMessages...)
		a.emit(EventStep, step, stepActions)

		if resp.StopReason == "length" {
			a.logger.Warn("context limit reached", "step", step)
			session.StopReason = "context_limit"
			break
		}
	}

	if session.StopReason == "" {
		session.StopReason = "step_limit"
	}

	session.EndedAt = time.Now()

	a.logger.Info("agent run complete",
		"steps", session.Steps,
		"actions", len(session.Actions),
		"errors", len(session.Errors),
		"stop_reason", session.StopReason,
		"duration", session.EndedAt.Sub(session.StartedAt),
	)

	a.emit(EventSessionEnd, session.Steps, session)

	return session, nil
}

func (a *Agent) initGoals() []GoalResult {
	goals := make([]GoalResult, len(a.persona.Goals))
	for i, g := range a.persona.Goals {
		goals[i] = GoalResult{Goal: g}
	}
	return goals
}

// executeTool routes a tool call to either a builtin handler or the driver.
func (a *Agent) executeTool(ctx context.Context, call provider.ToolCall, session *Session, step int) (*provider.ToolResult, error) {
	switch call.Name {
	case "report_ux_observation":
		return a.handleUXObservation(call, session, step)
	case "mark_goal_complete":
		return a.handleGoalComplete(call, session)
	default:
		return a.driver.Execute(ctx, call)
	}
}

// builtinTools returns tools that are handled by the agent itself, not the driver.
func (a *Agent) builtinTools() []provider.Tool {
	return []provider.Tool{
		{
			Name:        "report_ux_observation",
			Description: "Report a UX observation — something confusing, broken, missing, or unexpected in the application's interface.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"observation": map[string]any{
						"type":        "string",
						"description": "What you observed",
					},
					"severity": map[string]any{
						"type":        "string",
						"enum":        []string{"info", "warning", "error"},
						"description": "How severe the issue is",
					},
				},
				"required": []string{"observation"},
			},
		},
		{
			Name:        "mark_goal_complete",
			Description: "Mark one of your goals as achieved. Call this when you've successfully completed a goal.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"goal": map[string]any{
						"type":        "string",
						"description": "The goal text, matching one of your listed goals",
					},
					"notes": map[string]any{
						"type":        "string",
						"description": "Optional notes about how you achieved it",
					},
				},
				"required": []string{"goal"},
			},
		},
	}
}

func (a *Agent) handleUXObservation(call provider.ToolCall, session *Session, step int) (*provider.ToolResult, error) {
	var args struct {
		Observation string `json:"observation"`
		Severity    string `json:"severity"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return &provider.ToolResult{
			ToolID:  call.ID,
			Content: fmt.Sprintf("failed to parse arguments: %v", err),
			IsError: true,
		}, nil
	}

	if args.Severity == "" {
		args.Severity = "info"
	}

	note := UXNote{
		Step:        step,
		Timestamp:   time.Now(),
		Observation: args.Observation,
		Severity:    args.Severity,
	}
	session.UXNotes = append(session.UXNotes, note)
	a.emit(EventObservation, step, note)

	a.logger.Info("UX observation recorded", "severity", args.Severity, "observation", args.Observation)

	return &provider.ToolResult{
		ToolID:  call.ID,
		Content: "UX observation recorded.",
	}, nil
}

func (a *Agent) handleGoalComplete(call provider.ToolCall, session *Session) (*provider.ToolResult, error) {
	var args struct {
		Goal  string `json:"goal"`
		Notes string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return &provider.ToolResult{
			ToolID:  call.ID,
			Content: fmt.Sprintf("failed to parse arguments: %v", err),
			IsError: true,
		}, nil
	}

	// Find the matching goal and mark it achieved.
	found := false
	for i := range session.Goals {
		if session.Goals[i].Goal == args.Goal {
			session.Goals[i].Achieved = true
			session.Goals[i].Notes = args.Notes
			found = true
			break
		}
	}

	if !found {
		// Try a substring match — the LLM might not reproduce the goal text exactly.
		for i := range session.Goals {
			if strings.Contains(strings.ToLower(session.Goals[i].Goal), strings.ToLower(args.Goal)) ||
				strings.Contains(strings.ToLower(args.Goal), strings.ToLower(session.Goals[i].Goal)) {
				session.Goals[i].Achieved = true
				session.Goals[i].Notes = args.Notes
				found = true
				break
			}
		}
	}

	if !found {
		return &provider.ToolResult{
			ToolID:  call.ID,
			Content: fmt.Sprintf("No matching goal found for %q. Your goals are: %v", args.Goal, goalNames(session.Goals)),
		}, nil
	}

	a.logger.Info("goal marked complete", "goal", args.Goal)

	return &provider.ToolResult{
		ToolID:  call.ID,
		Content: "Goal marked as complete.",
	}, nil
}

func goalNames(goals []GoalResult) []string {
	names := make([]string, len(goals))
	for i, g := range goals {
		names[i] = g.Goal
	}
	return names
}