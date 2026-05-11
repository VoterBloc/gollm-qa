//go:build ignore

// scripts/backfill-persona-tags.go — one-shot tag backfill for
// existing persona YAMLs. See internal/persona.BackfillTags for the
// matching logic and skip rules.
//
// Run via:
//
//	go run scripts/backfill-persona-tags.go [--dry-run] [--personas personas] [--campaigns campaigns]
//
// Default --dry-run prints what would change without touching files.
// Pass --dry-run=false to actually write.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/VoterBloc/gollm-qa/internal/persona"
)

func main() {
	var (
		personasDir  string
		campaignsDir string
		dryRun       bool
	)
	flag.StringVar(&personasDir, "personas", "personas", "persona directory to walk")
	flag.StringVar(&campaignsDir, "campaigns", "campaigns", "campaign directory to look up cohorts from")
	flag.BoolVar(&dryRun, "dry-run", true, "report what would change without writing (default true — pass --dry-run=false to apply)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	report, err := persona.BackfillTags(personasDir, campaignsDir, dryRun, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backfill failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Backfill summary")
	fmt.Fprintf(os.Stderr, "  Examined: %d\n", report.Examined)
	if dryRun {
		fmt.Fprintf(os.Stderr, "  Would tag: %d (dry-run; pass --dry-run=false to apply)\n", len(report.Tagged))
	} else {
		fmt.Fprintf(os.Stderr, "  Tagged: %d\n", len(report.Tagged))
	}
	fmt.Fprintf(os.Stderr, "  Already tagged (skipped): %d\n", len(report.AlreadyTagged))
	fmt.Fprintf(os.Stderr, "  No cohort prefix (skipped): %d\n", len(report.NoCohortPrefix))
	fmt.Fprintf(os.Stderr, "  Unmatched cohort (skipped): %d\n", len(report.UnmatchedCohort))
	fmt.Fprintf(os.Stderr, "  Ambiguous cohort (skipped): %d\n", len(report.AmbiguousCohort))
}
