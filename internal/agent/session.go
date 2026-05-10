package agent

import "time"

// Session records everything that happens during an agent's run.
type Session struct {
	AgentName    string       `json:"agent_name"`
	StartedAt    time.Time    `json:"started_at"`
	EndedAt      time.Time    `json:"ended_at,omitempty"`
	Goals        []GoalResult `json:"goals"`
	Actions      []Action     `json:"actions"`
	Errors       []AgentError `json:"errors,omitempty"`
	UXNotes      []UXNote     `json:"ux_notes,omitempty"`
	TokensIn     int          `json:"tokens_in"`
	TokensOut    int          `json:"tokens_out"`
	ModelID      string       `json:"model_id,omitempty"`     // "<provider>:<model>" — set from Usage.ModelID
	EstimatedUSD float64      `json:"estimated_usd,omitempty"` // populated when Config.Cost is set
	Steps        int          `json:"steps"`
	StopReason   string       `json:"stop_reason"` // "goals_complete", "step_limit", "error", "context_limit"
}

// GoalResult tracks whether a goal was achieved.
type GoalResult struct {
	Goal      string `json:"goal"`
	Achieved  bool   `json:"achieved"`
	Notes     string `json:"notes,omitempty"`
}

// Action is a single tool call the agent made during the session.
type Action struct {
	Step      int       `json:"step"`
	Timestamp time.Time `json:"timestamp"`
	ToolName  string    `json:"tool_name"`
	Arguments string    `json:"arguments"` // raw JSON
	Result    string    `json:"result"`
	IsError   bool      `json:"is_error,omitempty"`
	Duration  time.Duration `json:"duration"`
}

// AgentError is an error encountered during the session.
type AgentError struct {
	Step      int       `json:"step"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
	ToolName  string    `json:"tool_name,omitempty"`
}

// UXNote is an observation the agent made about the application's UX.
type UXNote struct {
	Step        int       `json:"step"`
	Timestamp   time.Time `json:"timestamp"`
	Observation string    `json:"observation"`
	Severity    string    `json:"severity,omitempty"` // "info", "warning", "error"
	Screenshot  string    `json:"screenshot,omitempty"` // file path, browser mode only
}