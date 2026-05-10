package agent

import (
	"fmt"
	"sort"
	"strings"
)

// RunSummary aggregates state across a multi-agent run for both human
// (CLI summary line) and machine (HTTP run_end payload) consumers.
//
// Built from a slice of completed *Session — same shape on both paths
// so the two surfaces report identical numbers without re-deriving.
type RunSummary struct {
	Agents       int            `json:"agents"`
	Completed    int            `json:"completed"`
	StoppedOnBudget int         `json:"stopped_on_budget"`
	TokensIn     int            `json:"tokens_in"`
	TokensOut    int            `json:"tokens_out"`
	EstimatedUSD float64        `json:"estimated_usd"`
	StopReasons  map[string]int `json:"stop_reasons"`
}

// SummarizeRun rolls a slice of completed sessions into a single summary.
// "Completed" counts agents whose stop reason is goals_complete (the
// happy path); "stopped on budget" counts both per-agent and run-level
// budget exits since they share StopReasonBudgetExhausted.
func SummarizeRun(sessions []*Session) RunSummary {
	out := RunSummary{
		Agents:      len(sessions),
		StopReasons: make(map[string]int, len(sessions)),
	}
	for _, s := range sessions {
		out.TokensIn += s.TokensIn
		out.TokensOut += s.TokensOut
		out.EstimatedUSD += s.EstimatedUSD
		out.StopReasons[s.StopReason]++
		switch s.StopReason {
		case StopReasonGoalsComplete:
			out.Completed++
		case StopReasonBudgetExhausted:
			out.StoppedOnBudget++
		}
	}
	return out
}

// Format renders the summary as the CLI-friendly multi-line block.
// Same shape as the example in issue #37 so output matches the spec
// users read before running.
func (s RunSummary) Format() string {
	var b strings.Builder
	fmt.Fprintln(&b, "Run summary")
	fmt.Fprintf(&b, "  Agents: %d (%d completed, %d stopped on budget)\n",
		s.Agents, s.Completed, s.StoppedOnBudget)
	fmt.Fprintf(&b, "  Tokens: %s input, %s output\n",
		formatTokens(s.TokensIn), formatTokens(s.TokensOut))
	fmt.Fprintf(&b, "  Estimated cost: $%.2f\n", s.EstimatedUSD)
	fmt.Fprintf(&b, "  Stop reasons: %s\n", formatStopReasons(s.StopReasons))
	return b.String()
}

// formatTokens shortens large counts the way operators read them in
// dashboards: 4_200_000 -> "4.2M", 180_000 -> "180K", 42 -> "42".
func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.0fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatStopReasons renders the stop-reason map as a stable
// "N reason" comma-separated string, sorted by count desc then by name.
// Sorting matters so the line is reproducible across runs and tests.
func formatStopReasons(reasons map[string]int) string {
	if len(reasons) == 0 {
		return "(none)"
	}
	type kv struct {
		Reason string
		Count  int
	}
	pairs := make([]kv, 0, len(reasons))
	for r, c := range reasons {
		pairs = append(pairs, kv{r, c})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Count != pairs[j].Count {
			return pairs[i].Count > pairs[j].Count
		}
		return pairs[i].Reason < pairs[j].Reason
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = fmt.Sprintf("%d %s", p.Count, p.Reason)
	}
	return strings.Join(parts, ", ")
}
