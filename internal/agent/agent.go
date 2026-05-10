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

	"github.com/VoterBloc/gollm-qa/internal/cost"
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

	// Cost, if non-nil, is used to populate Session.EstimatedUSD at end
	// of run. Nil leaves the field zero — useful in tests, or when cost
	// accounting is irrelevant (local-model runs, fixtures).
	Cost *cost.Table

	// BudgetPerAgentUSD is the soft USD ceiling for this agent's run.
	// When the running estimate (computed via Cost) crosses the budget,
	// the agent gets one more turn to wrap up and exits with
	// StopReason="budget_exhausted". Zero (the default) disables
	// enforcement; nil Cost also disables enforcement regardless of
	// budget value, since we can't estimate cost without a price table.
	BudgetPerAgentUSD float64
}

// budgetExhaustedNudge is the wrap-up message appended when an agent's
// running cost crosses the per-agent budget. RoleUser (not RoleSystem)
// because the Claude provider treats system messages as a single
// up-front prompt — a second system message would clobber the persona
// prompt, losing context for the wrap-up turn.
//
// The [ENGINE NOTICE] prefix is a provenance marker: in a session
// transcript a future operator (or report tool) scanning user-role
// turns can classify this as "the engine spoke, not the persona"
// without parsing the wording. The bracket form is intentionally
// distinct from anything the model would produce on its own.
const budgetExhaustedNudge = "[ENGINE NOTICE] Budget exhausted. Stop, summarize what you did, and report any UX observations before exiting. Do not call any more tools."

// StopReason* are the values session.StopReason takes when an agent
// run ends. Exported so reports / dashboards can group by reason
// without string-matching, and so callers can tell "ran out of budget"
// from "completed naturally" or "hit step limit" cleanly. Kept as
// untyped string constants (rather than a typed enum) because the
// session JSON ships these verbatim and consumers in other languages
// pattern-match on the string form.
const (
	StopReasonGoalsComplete   = "goals_complete"
	StopReasonStepLimit       = "step_limit"
	StopReasonContextLimit    = "context_limit"
	StopReasonError           = "error"
	StopReasonCancelled       = "cancelled"
	StopReasonAuthFailed      = "auth_failed"
	StopReasonRegisterFailed  = "register_failed"
	StopReasonBudgetExhausted = "budget_exhausted"
)

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
				session.StopReason = StopReasonRegisterFailed
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
			session.StopReason = StopReasonAuthFailed
			session.EndedAt = time.Now()
			a.emit(EventError, 0, agentErr)
			a.emit(EventSessionEnd, 0, session)
			return session, nil
		}
		authenticated = true
	}

	tools := a.driver.Tools()
	tools = append(tools, a.builtinTools()...)

	// On the wrap-up turn, we strip driver tools and offer only the
	// agent's built-ins (report_ux_observation, mark_goal_complete).
	// A model that defies the nudge can still report what it did, but
	// can't fire a destructive driver call (delete_account, post, …)
	// just because we asked it to summarize.
	wrapUpTools := a.builtinTools()

	startMessage := "Begin working toward your goals. Use the available tools to interact with the application."
	if authenticated {
		startMessage = "You are now logged in. " + startMessage
	}

	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: a.persona.SystemPrompt()},
		{Role: provider.RoleUser, Content: startMessage},
	}

	a.logger.Info("starting agent run", "goals", len(a.persona.Goals), "max_steps", a.config.MaxSteps)

	// budgetExhausted flips to true once the running cost has crossed
	// BudgetPerAgentUSD and we've appended the wrap-up nudge. The next
	// loop iteration is the agent's one chance to summarize cleanly;
	// after that turn we exit with StopReasonBudgetExhausted regardless
	// of whether the model still wanted to call tools.
	budgetExhausted := false

	for step := 1; step <= a.config.MaxSteps; step++ {
		if ctx.Err() != nil {
			session.StopReason = StopReasonCancelled
			break
		}

		if a.config.StepDelay > 0 && step > 1 {
			select {
			case <-time.After(a.config.StepDelay):
			case <-ctx.Done():
				session.StopReason = StopReasonCancelled
				break
			}
		}

		callTools := tools
		if budgetExhausted {
			callTools = wrapUpTools
		}
		resp, err := a.provider.Chat(ctx, messages, callTools)
		if err != nil {
			agentErr := AgentError{
				Step:      step,
				Timestamp: time.Now(),
				Message:   fmt.Sprintf("provider error: %v", err),
			}
			session.Errors = append(session.Errors, agentErr)
			session.StopReason = StopReasonError
			a.emit(EventError, step, agentErr)
			break
		}

		session.TokensIn += resp.Usage.InputTokens
		session.TokensOut += resp.Usage.OutputTokens
		if resp.Usage.ModelID != "" {
			// Provider stamps the same id on every response in a session;
			// last-write-wins is fine and handles the empty-first-response
			// edge case (e.g. provider error before metadata is populated).
			session.ModelID = resp.Usage.ModelID
		}
		session.Steps = step

		messages = append(messages, resp.Message)

		// No tool calls — the model is done talking. If we'd already
		// issued the budget nudge, this is the wrap-up turn finishing
		// cleanly. If the budget was crossed on this turn (single
		// over-budget text response, no prior nudge), still tag as
		// budget_exhausted — cost-truthful labeling beats letting an
		// expensive single-shot completion read as "goals_complete."
		if len(resp.Message.ToolCalls) == 0 {
			a.logger.Info("agent finished (no more tool calls)", "step", step)
			if budgetExhausted || a.budgetCrossed(session) {
				session.StopReason = StopReasonBudgetExhausted
			} else {
				session.StopReason = StopReasonGoalsComplete
			}
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
			session.StopReason = StopReasonContextLimit
			break
		}

		// If the prior iteration appended the budget nudge, that just-
		// completed turn was the wrap-up. The model may have ignored
		// the nudge and called tools anyway; either way, we stop here
		// rather than letting cost continue to accumulate.
		if budgetExhausted {
			a.logger.Info("budget exhausted, ending wrap-up turn", "step", step)
			session.StopReason = StopReasonBudgetExhausted
			break
		}

		// If the running cost just crossed the budget, queue the
		// wrap-up nudge for the next iteration. Skipped when Cost is
		// nil (no estimate possible) or BudgetPerAgentUSD is zero
		// (no-limit default).
		if a.budgetCrossed(session) {
			a.logger.Info("budget crossed, requesting wrap-up",
				"step", step,
				"budget_usd", a.config.BudgetPerAgentUSD,
				"tokens_in", session.TokensIn,
				"tokens_out", session.TokensOut,
			)
			budgetExhausted = true
			messages = append(messages, provider.Message{
				Role:    provider.RoleUser,
				Content: budgetExhaustedNudge,
			})
		}
	}

	if session.StopReason == "" {
		session.StopReason = StopReasonStepLimit
	}

	if a.config.Cost != nil {
		session.EstimatedUSD = a.config.Cost.Estimate(provider.Usage{
			InputTokens:  session.TokensIn,
			OutputTokens: session.TokensOut,
			ModelID:      session.ModelID,
		})
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

// budgetCrossed reports whether the running cost has exceeded the
// configured per-agent budget. Returns false if Cost is nil or
// BudgetPerAgentUSD is zero (the default = "no limit").
func (a *Agent) budgetCrossed(session *Session) bool {
	if a.config.Cost == nil || a.config.BudgetPerAgentUSD <= 0 {
		return false
	}
	running := a.config.Cost.Estimate(provider.Usage{
		InputTokens:  session.TokensIn,
		OutputTokens: session.TokensOut,
		ModelID:      session.ModelID,
	})
	return running > a.config.BudgetPerAgentUSD
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