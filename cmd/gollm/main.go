package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/VoterBloc/gollm-qa/internal/agent"
	"github.com/VoterBloc/gollm-qa/internal/config"
	apidriver "github.com/VoterBloc/gollm-qa/internal/driver/api"
	"github.com/VoterBloc/gollm-qa/internal/provider/claude"
	"github.com/VoterBloc/gollm-qa/internal/reporter"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Parse flags.
	var (
		configPath  string
		personaDir  string
		maxAgents   int
		outputDir   string
		maxSteps    int
		concurrency int
		stepDelay   time.Duration
	)

	flag.StringVar(&configPath, "config", "", "path to app config YAML (required)")
	flag.StringVar(&personaDir, "personas", "", "path to persona directory (required)")
	flag.IntVar(&maxAgents, "agents", 0, "max agents to run (default: all personas)")
	flag.StringVar(&outputDir, "output", "reports", "output directory for reports")
	flag.IntVar(&maxSteps, "max-steps", 50, "max steps per agent")
	flag.IntVar(&concurrency, "concurrency", 3, "max concurrent agents")
	flag.DurationVar(&stepDelay, "step-delay", 0, "delay between agent steps (e.g. 1s)")
	flag.Parse()

	if configPath == "" || personaDir == "" {
		flag.Usage()
		return fmt.Errorf("--config and --personas are required")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Load config.
	appCfg, err := config.LoadAppConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	logger.Info("loaded app config", "app", appCfg.Name, "tools", len(appCfg.Tools))

	// Load personas.
	personas, err := config.LoadPersonas(personaDir)
	if err != nil {
		return fmt.Errorf("loading personas: %w", err)
	}
	if len(personas) == 0 {
		return fmt.Errorf("no personas found in %s", personaDir)
	}

	// Limit agent count if requested.
	if maxAgents > 0 && maxAgents < len(personas) {
		personas = personas[:maxAgents]
	}
	logger.Info("loaded personas", "count", len(personas))

	// Set up cancellation.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	agentCfg := agent.Config{
		MaxSteps:  maxSteps,
		StepDelay: stepDelay,
	}

	// Run agents concurrently.
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

	var mu sync.Mutex
	var sessions []*agent.Session

	fmt.Fprintf(os.Stderr, "\nStarting %d agents against %s (concurrency: %d, max steps: %d)\n\n",
		len(personas), appCfg.Name, concurrency, maxSteps)

	for _, p := range personas {
		p := p
		g.Go(func() error {
			drv := apidriver.New(appCfg, logger)
			llm := claude.New()
			a := agent.New(p, llm, drv, agentCfg, logger)

			session, err := a.Run(ctx)
			if err != nil {
				logger.Error("agent failed", "agent", p.Name, "error", err)
				return nil // don't cancel other agents on individual failure
			}

			// Write individual session report.
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

	// Write summary.
	if len(sessions) > 0 {
		path, err := reporter.WriteSummary(sessions, outputDir)
		if err != nil {
			logger.Error("failed to write summary", "error", err)
		} else {
			logger.Info("summary written", "path", path)
		}
	}

	// Print final stats.
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
