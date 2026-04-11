// Package reporter handles session reporting — actions taken, goals achieved,
// errors encountered, UX observations, and screenshots.
package reporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VoterBloc/gollm-qa/internal/agent"
)

// WriteSession writes a session report as a JSON file and returns the file path.
func WriteSession(session *agent.Session, outputDir string) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("creating output dir: %w", err)
	}

	name := sanitizeFilename(session.AgentName)
	ts := session.StartedAt.Format("20060102-150405")
	filename := fmt.Sprintf("%s_%s.json", name, ts)
	path := filepath.Join(outputDir, filename)

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling session: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("writing session: %w", err)
	}

	return path, nil
}

// Summary is an aggregate view of multiple agent sessions.
type Summary struct {
	Timestamp       time.Time       `json:"timestamp"`
	AgentCount      int             `json:"agent_count"`
	TotalSteps      int             `json:"total_steps"`
	TotalActions    int             `json:"total_actions"`
	TotalErrors     int             `json:"total_errors"`
	TotalUXNotes    int             `json:"total_ux_notes"`
	TotalTokensIn   int             `json:"total_tokens_in"`
	TotalTokensOut  int             `json:"total_tokens_out"`
	GoalCompletion  float64         `json:"goal_completion_rate"`
	StopReasons     map[string]int  `json:"stop_reasons"`
	AgentSummaries  []AgentSummary  `json:"agents"`
}

// AgentSummary is a per-agent view within the summary.
type AgentSummary struct {
	Name         string  `json:"name"`
	Steps        int     `json:"steps"`
	Actions      int     `json:"actions"`
	Errors       int     `json:"errors"`
	UXNotes      int     `json:"ux_notes"`
	GoalsTotal   int     `json:"goals_total"`
	GoalsAchieved int    `json:"goals_achieved"`
	StopReason   string  `json:"stop_reason"`
	Duration     string  `json:"duration"`
}

// WriteSummary writes an aggregate summary of all sessions and returns the file path.
func WriteSummary(sessions []*agent.Session, outputDir string) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("creating output dir: %w", err)
	}

	summary := buildSummary(sessions)

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling summary: %w", err)
	}

	path := filepath.Join(outputDir, "summary.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("writing summary: %w", err)
	}

	return path, nil
}

func buildSummary(sessions []*agent.Session) *Summary {
	s := &Summary{
		Timestamp:   time.Now(),
		AgentCount:  len(sessions),
		StopReasons: make(map[string]int),
	}

	totalGoals := 0
	achievedGoals := 0

	for _, sess := range sessions {
		s.TotalSteps += sess.Steps
		s.TotalActions += len(sess.Actions)
		s.TotalErrors += len(sess.Errors)
		s.TotalUXNotes += len(sess.UXNotes)
		s.TotalTokensIn += sess.TokensIn
		s.TotalTokensOut += sess.TokensOut
		s.StopReasons[sess.StopReason]++

		agentGoals := 0
		for _, g := range sess.Goals {
			totalGoals++
			if g.Achieved {
				achievedGoals++
				agentGoals++
			}
		}

		s.AgentSummaries = append(s.AgentSummaries, AgentSummary{
			Name:          sess.AgentName,
			Steps:         sess.Steps,
			Actions:       len(sess.Actions),
			Errors:        len(sess.Errors),
			UXNotes:       len(sess.UXNotes),
			GoalsTotal:    len(sess.Goals),
			GoalsAchieved: agentGoals,
			StopReason:    sess.StopReason,
			Duration:      sess.EndedAt.Sub(sess.StartedAt).Round(time.Millisecond).String(),
		})
	}

	if totalGoals > 0 {
		s.GoalCompletion = float64(achievedGoals) / float64(totalGoals)
	}

	return s
}

func sanitizeFilename(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "_")
	// Remove anything that's not alphanumeric, underscore, or hyphen.
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
