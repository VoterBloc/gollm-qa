// Package agent implements the core agent loop. Each agent has a persona,
// an LLM provider for decision-making, and a driver for interacting with
// the target application.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/VoterBloc/gollm-qa/internal/driver"
	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// Config controls how an agent runs.
type Config struct {
	// MaxSteps is the maximum number of tool-call rounds before stopping.
	MaxSteps int

	// StepDelay is how long to wait between steps (pacing).
	StepDelay time.Duration
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

	tools := a.driver.Tools()
	tools = append(tools, a.builtinTools()...)

	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: a.persona.SystemPrompt()},
		{Role: provider.RoleUser, Content: "You are now logged in. Begin working toward your goals. Use the available tools to interact with the application."},
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
			session.Errors = append(session.Errors, AgentError{
				Step:      step,
				Timestamp: time.Now(),
				Message:   fmt.Sprintf("provider error: %v", err),
			})
			session.StopReason = "error"
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
		for _, call := range resp.Message.ToolCalls {
			a.logger.Info("executing tool", "step", step, "tool", call.Name)

			start := time.Now()
			result, execErr := a.executeTool(ctx, call)
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

			if execErr != nil {
				session.Errors = append(session.Errors, AgentError{
					Step:      step,
					Timestamp: time.Now(),
					ToolName:  call.Name,
					Message:   execErr.Error(),
				})
			}

			toolMessages = append(toolMessages, provider.Message{
				Role:    provider.RoleTool,
				ToolID:  call.ID,
				Content: result.Content,
			})
		}
		messages = append(messages, toolMessages...)

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
func (a *Agent) executeTool(ctx context.Context, call provider.ToolCall) (*provider.ToolResult, error) {
	switch call.Name {
	case "report_ux_observation":
		return a.handleUXObservation(call)
	case "mark_goal_complete":
		return a.handleGoalComplete(call)
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

func (a *Agent) handleUXObservation(call provider.ToolCall) (*provider.ToolResult, error) {
	// TODO: parse call.Arguments JSON and append to session UX notes.
	// For now, return acknowledgment so the loop works end-to-end.
	return &provider.ToolResult{
		ToolID:  call.ID,
		Content: "UX observation recorded.",
	}, nil
}

func (a *Agent) handleGoalComplete(call provider.ToolCall) (*provider.ToolResult, error) {
	// TODO: parse call.Arguments JSON and mark goal in session.
	// For now, return acknowledgment so the loop works end-to-end.
	return &provider.ToolResult{
		ToolID:  call.ID,
		Content: "Goal marked as complete.",
	}, nil
}