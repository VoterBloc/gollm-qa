package agent

import (
	"strings"
	"testing"
)

func TestSummarizeRun_AggregatesCorrectly(t *testing.T) {
	sessions := []*Session{
		{TokensIn: 1_000_000, TokensOut: 200_000, EstimatedUSD: 6.00, StopReason: StopReasonGoalsComplete},
		{TokensIn: 500_000, TokensOut: 100_000, EstimatedUSD: 3.00, StopReason: StopReasonGoalsComplete},
		{TokensIn: 100_000, TokensOut: 50_000, EstimatedUSD: 1.05, StopReason: StopReasonBudgetExhausted},
		{TokensIn: 250_000, TokensOut: 75_000, EstimatedUSD: 1.875, StopReason: StopReasonStepLimit},
	}

	got := SummarizeRun(sessions)

	if got.Agents != 4 {
		t.Errorf("Agents = %d, want 4", got.Agents)
	}
	if got.Completed != 2 {
		t.Errorf("Completed = %d, want 2", got.Completed)
	}
	if got.StoppedOnBudget != 1 {
		t.Errorf("StoppedOnBudget = %d, want 1", got.StoppedOnBudget)
	}
	if got.TokensIn != 1_850_000 {
		t.Errorf("TokensIn = %d, want 1_850_000", got.TokensIn)
	}
	if got.TokensOut != 425_000 {
		t.Errorf("TokensOut = %d, want 425_000", got.TokensOut)
	}
	if got.EstimatedUSD < 11.92 || got.EstimatedUSD > 11.93 {
		t.Errorf("EstimatedUSD = %v, want ~11.925", got.EstimatedUSD)
	}
	if got.StopReasons[StopReasonGoalsComplete] != 2 {
		t.Errorf("StopReasons[goals_complete] = %d, want 2", got.StopReasons[StopReasonGoalsComplete])
	}
	if got.StopReasons[StopReasonBudgetExhausted] != 1 {
		t.Errorf("StopReasons[budget_exhausted] = %d, want 1", got.StopReasons[StopReasonBudgetExhausted])
	}
	if got.StopReasons[StopReasonStepLimit] != 1 {
		t.Errorf("StopReasons[step_limit] = %d, want 1", got.StopReasons[StopReasonStepLimit])
	}
}

func TestSummarizeRun_EmptyInput(t *testing.T) {
	got := SummarizeRun(nil)
	if got.Agents != 0 || got.EstimatedUSD != 0 || len(got.StopReasons) != 0 {
		t.Errorf("empty input should yield zero summary, got %+v", got)
	}
}

func TestRunSummary_FormatMatchesIssueShape(t *testing.T) {
	// Mirrors the example block in issue #37 to keep the line shape
	// stable for users (and dashboards reading the CLI output).
	sessions := make([]*Session, 100)
	for i := 0; i < 89; i++ {
		sessions[i] = &Session{
			TokensIn: 42_000, TokensOut: 1_800,
			EstimatedUSD: 0.155,
			StopReason:   StopReasonGoalsComplete,
		}
	}
	for i := 89; i < 97; i++ {
		sessions[i] = &Session{
			TokensIn: 50_000, TokensOut: 2_200,
			EstimatedUSD: 0.183,
			StopReason:   StopReasonStepLimit,
		}
	}
	for i := 97; i < 100; i++ {
		sessions[i] = &Session{
			TokensIn: 60_000, TokensOut: 2_500,
			EstimatedUSD: 0.218,
			StopReason:   StopReasonBudgetExhausted,
		}
	}
	sessions[88] = &Session{
		TokensIn: 1_500, TokensOut: 100, EstimatedUSD: 0.006,
		StopReason: StopReasonGoalsComplete,
	}

	out := SummarizeRun(sessions).Format()

	for _, want := range []string{
		"Run summary",
		"Agents: 100 (89 completed, 3 stopped on budget)",
		"Tokens:",
		"input,",
		"output",
		"Estimated cost: $",
		"Stop reasons:",
		"89 goals_complete",
		"8 step_limit",
		"3 budget_exhausted",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Format output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestFormatTokens(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1_000, "1K"},
		{180_000, "180K"},
		{999_999, "1000K"}, // rounds via %.0fK before crossing the M boundary
		{1_000_000, "1.0M"},
		{4_200_000, "4.2M"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := formatTokens(tc.in); got != tc.want {
				t.Errorf("formatTokens(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatStopReasons_StableOrdering(t *testing.T) {
	// Highest count first; ties broken alphabetically. This stability
	// matters for tests asserting on the line and for users diffing
	// output across runs.
	in := map[string]int{
		"goals_complete":   89,
		"step_limit":       8,
		"budget_exhausted": 3,
	}
	got := formatStopReasons(in)
	want := "89 goals_complete, 8 step_limit, 3 budget_exhausted"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatStopReasons_AlphabeticalTieBreak(t *testing.T) {
	in := map[string]int{
		"taco_cannon_jam": 5,
		"bigfoot_sighted": 5,
		"yeti_unreachable": 5,
	}
	got := formatStopReasons(in)
	want := "5 bigfoot_sighted, 5 taco_cannon_jam, 5 yeti_unreachable"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatStopReasons_EmptyMap(t *testing.T) {
	if got := formatStopReasons(map[string]int{}); got != "(none)" {
		t.Errorf("empty map should render as (none), got %q", got)
	}
}
