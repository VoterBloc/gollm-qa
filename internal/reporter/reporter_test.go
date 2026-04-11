package reporter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VoterBloc/gollm-qa/internal/agent"
)

func testSession(name string, steps int, goalAchieved bool) *agent.Session {
	now := time.Date(2026, 4, 10, 14, 30, 0, 0, time.UTC)
	return &agent.Session{
		AgentName:  name,
		StartedAt:  now,
		EndedAt:    now.Add(2 * time.Minute),
		Steps:      steps,
		TokensIn:   500,
		TokensOut:  200,
		StopReason: "goals_complete",
		Goals: []agent.GoalResult{
			{Goal: "Find the Loch Ness Monster", Achieved: goalAchieved},
			{Goal: "Take a selfie with Bigfoot", Achieved: false},
		},
		Actions: []agent.Action{
			{Step: 1, ToolName: "search_lake", Arguments: `{"depth": "very"}`, Result: "found a log"},
			{Step: 2, ToolName: "take_photo", Arguments: `{"subject": "log"}`, Result: "blurry photo"},
		},
		Errors: []agent.AgentError{
			{Step: 2, Message: "camera was upside down"},
		},
		UXNotes: []agent.UXNote{
			{Step: 1, Observation: "the search button was hidden behind a kelp graphic"},
		},
	}
}

func TestWriteSession(t *testing.T) {
	dir := t.TempDir()
	session := testSession("Nessie Hunter 3000", 5, true)

	path, err := WriteSession(session, dir)
	if err != nil {
		t.Fatalf("WriteSession() error: %v", err)
	}

	// Verify filename format.
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "nessie_hunter_3000_") {
		t.Errorf("unexpected filename: %s", base)
	}
	if !strings.HasSuffix(base, ".json") {
		t.Errorf("expected .json extension: %s", base)
	}

	// Verify content is valid JSON and contains expected data.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	var parsed agent.Session
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parsing JSON: %v", err)
	}
	if parsed.AgentName != "Nessie Hunter 3000" {
		t.Errorf("unexpected agent name: %q", parsed.AgentName)
	}
	if len(parsed.Actions) != 2 {
		t.Errorf("expected 2 actions, got %d", len(parsed.Actions))
	}
}

func TestWriteSession_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "reports")
	session := testSession("Yeti Finder", 1, false)

	path, err := WriteSession(session, dir)
	if err != nil {
		t.Fatalf("WriteSession() error: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected file to exist")
	}
}

func TestWriteSummary(t *testing.T) {
	dir := t.TempDir()
	sessions := []*agent.Session{
		testSession("Mothman Mike", 10, true),
		testSession("Chupacabra Charlie", 5, false),
	}

	path, err := WriteSummary(sessions, dir)
	if err != nil {
		t.Fatalf("WriteSummary() error: %v", err)
	}

	base := filepath.Base(path)
	if !strings.HasPrefix(base, "summary_") || !strings.HasSuffix(base, ".json") {
		t.Errorf("unexpected summary filename: %s", base)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	var summary Summary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("parsing JSON: %v", err)
	}

	if summary.AgentCount != 2 {
		t.Errorf("expected 2 agents, got %d", summary.AgentCount)
	}
	if summary.TotalSteps != 15 {
		t.Errorf("expected 15 total steps, got %d", summary.TotalSteps)
	}
	if summary.TotalActions != 4 {
		t.Errorf("expected 4 total actions, got %d", summary.TotalActions)
	}
	if summary.TotalErrors != 2 {
		t.Errorf("expected 2 total errors, got %d", summary.TotalErrors)
	}
	// 1 of 4 goals achieved = 0.25
	if summary.GoalCompletion != 0.25 {
		t.Errorf("expected 0.25 goal completion, got %f", summary.GoalCompletion)
	}
	if summary.StopReasons["goals_complete"] != 2 {
		t.Errorf("expected 2 goals_complete stops, got %v", summary.StopReasons)
	}
	if len(summary.AgentSummaries) != 2 {
		t.Errorf("expected 2 agent summaries, got %d", len(summary.AgentSummaries))
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Cornelius McMuffin", "cornelius_mcmuffin"},
		{"Dr. Bigfoot III", "dr_bigfoot_iii"},
		{"agent-007", "agent-007"},
		{"YELLING PERSON", "yelling_person"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
