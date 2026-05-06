package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

	"github.com/VoterBloc/gollm-qa/internal/agent"
	"github.com/VoterBloc/gollm-qa/internal/config"
	apidriver "github.com/VoterBloc/gollm-qa/internal/driver/api"
	"github.com/VoterBloc/gollm-qa/internal/provider/claude"
	"github.com/VoterBloc/gollm-qa/internal/reporter"
)

// gjson path keys inside a purgeTestData report. Centralized so the renderer
// and any future consumer agree on the shape.
const (
	purgeKeyByTable = "byTable"
	purgeKeyTable   = "table"
	purgeKeyDeleted = "deleted"
	purgeKeyTotal   = "total"
)

func main() {
	// Load .env if present (silently ignored — env vars may already be set).
	_ = godotenv.Load()

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "purge":
		err = purgeCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `gollm — AI-driven synthetic user platform

Usage:
  gollm run --config <path> --personas <dir> [flags]
  gollm purge --config <path>

Run "gollm <subcommand> -h" for subcommand-specific flags.
`)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var (
		configPath  string
		personaDir  string
		maxAgents   int
		outputDir   string
		maxSteps    int
		concurrency int
		stepDelay   time.Duration
	)
	fs.StringVar(&configPath, "config", "", "path to app config YAML (required)")
	fs.StringVar(&personaDir, "personas", "", "path to persona directory (required)")
	fs.IntVar(&maxAgents, "agents", 0, "max agents to run (default: all personas)")
	fs.StringVar(&outputDir, "output", "reports", "output directory for reports")
	fs.IntVar(&maxSteps, "max-steps", 50, "max steps per agent")
	fs.IntVar(&concurrency, "concurrency", 3, "max concurrent agents")
	fs.DurationVar(&stepDelay, "step-delay", 0, "delay between agent steps (e.g. 1s)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if configPath == "" || personaDir == "" {
		fs.Usage()
		return fmt.Errorf("--config and --personas are required")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	appCfg, err := config.LoadAppConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	logger.Info("loaded app config", "app", appCfg.Name, "tools", len(appCfg.Tools))

	personas, err := config.LoadPersonas(personaDir)
	if err != nil {
		return fmt.Errorf("loading personas: %w", err)
	}
	if len(personas) == 0 {
		return fmt.Errorf("no personas found in %s", personaDir)
	}

	if maxAgents > 0 && maxAgents < len(personas) {
		personas = personas[:maxAgents]
	}
	logger.Info("loaded personas", "count", len(personas))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	agentCfg := agent.Config{
		MaxSteps:  maxSteps,
		StepDelay: stepDelay,
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

	var mu sync.Mutex
	var sessions []*agent.Session

	fmt.Fprintf(os.Stderr, "\nStarting %d agents against %s (concurrency: %d, max steps: %d)\n\n",
		len(personas), appCfg.Name, concurrency, maxSteps)

	for _, p := range personas {
		g.Go(func() error {
			drv := apidriver.New(appCfg, logger)
			llm := claude.New()
			a := agent.New(p, llm, drv, agentCfg, logger)

			session, err := a.Run(ctx)
			if err != nil {
				logger.Error("agent failed", "agent", p.Name, "error", err)
				return nil
			}

			path, writeErr := reporter.WriteSession(session, outputDir)
			if writeErr != nil {
				logger.Error("failed to write session report", "agent", p.Name, "error", writeErr)
			} else {
				logger.Info("session report written", "agent", p.Name, "path", path)
			}

			mu.Lock()
			sessions = append(sessions, session)
			mu.Unlock()

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("running agents: %w", err)
	}

	if len(sessions) > 0 {
		path, err := reporter.WriteSummary(sessions, outputDir)
		if err != nil {
			logger.Error("failed to write summary", "error", err)
		} else {
			logger.Info("summary written", "path", path)
		}
	}

	totalActions := 0
	totalErrors := 0
	totalUXNotes := 0
	for _, s := range sessions {
		totalActions += len(s.Actions)
		totalErrors += len(s.Errors)
		totalUXNotes += len(s.UXNotes)
	}

	fmt.Fprintf(os.Stderr, "\nDone. %d agents, %d actions, %d errors, %d UX notes. Reports in %s/\n",
		len(sessions), totalActions, totalErrors, totalUXNotes, outputDir)

	return nil
}

func purgeCmd(args []string) error {
	fs := flag.NewFlagSet("purge", flag.ExitOnError)
	var (
		configPath string
		skipPrompt bool
		countdown  time.Duration
	)
	fs.StringVar(&configPath, "config", "", "path to app config YAML (required)")
	fs.BoolVar(&skipPrompt, "yes", false, "skip the abort countdown")
	fs.DurationVar(&countdown, "countdown", 3*time.Second, "abort window before purge runs (ignored with --yes)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if configPath == "" {
		fs.Usage()
		return fmt.Errorf("--config is required")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	appCfg, err := config.LoadAppConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if appCfg.Admin.PurgeQuery == "" {
		return fmt.Errorf("config %s has no admin.purge_query — purge unavailable for this app", configPath)
	}
	if appCfg.Admin.IdentifierEnv == "" || appCfg.Admin.PasswordEnv == "" {
		return fmt.Errorf("config %s is missing admin.identifier_env or admin.password_env", configPath)
	}

	adminID := os.Getenv(appCfg.Admin.IdentifierEnv)
	adminPW := os.Getenv(appCfg.Admin.PasswordEnv)
	if adminID == "" || adminPW == "" {
		return fmt.Errorf("admin credentials not set: export %s and %s", appCfg.Admin.IdentifierEnv, appCfg.Admin.PasswordEnv)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if !skipPrompt && countdown > 0 {
		fmt.Fprintf(os.Stderr, "\n!! purging synthetic data via %s (%s) — Ctrl+C in %s to abort\n\n",
			appCfg.Name, appCfg.BaseURL, countdown)
		select {
		case <-time.After(countdown):
		case <-ctx.Done():
			return fmt.Errorf("aborted")
		}
	}

	drv := apidriver.New(appCfg, logger)
	logger.Info("authenticating as admin", "identifier", adminID)
	if err := drv.Login(ctx, adminID, adminPW); err != nil {
		return fmt.Errorf("admin login: %w", err)
	}

	logger.Info("running purge")
	report, err := drv.Purge(ctx)
	if err != nil {
		return fmt.Errorf("purge: %w", err)
	}

	printPurgeReport(os.Stdout, report)
	return nil
}

// printPurgeReport renders a purge response. If it has the standard
// {byTable: [{table, deleted}], total} shape, render as a table; otherwise
// dump the raw JSON pretty-formatted.
func printPurgeReport(w io.Writer, jsonReport string) {
	byTable := gjson.Get(jsonReport, purgeKeyByTable)
	if !byTable.IsArray() {
		var pretty any
		if err := json.Unmarshal([]byte(jsonReport), &pretty); err == nil {
			out, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Fprintln(w, string(out))
		} else {
			fmt.Fprintln(w, jsonReport)
		}
		return
	}

	const ruler = "----------------------------------------------"
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%-40s %s\n", "TABLE", "DELETED")
	fmt.Fprintln(w, ruler)
	byTable.ForEach(func(_, row gjson.Result) bool {
		fmt.Fprintf(w, "%-40s %d\n", row.Get(purgeKeyTable).String(), row.Get(purgeKeyDeleted).Int())
		return true
	})
	fmt.Fprintln(w, ruler)
	fmt.Fprintf(w, "%-40s %d\n", "TOTAL", gjson.Get(jsonReport, purgeKeyTotal).Int())
	fmt.Fprintln(w)
}
