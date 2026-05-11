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
	"path/filepath"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

	"github.com/VoterBloc/gollm-qa/internal/agent"
	"github.com/VoterBloc/gollm-qa/internal/config"
	"github.com/VoterBloc/gollm-qa/internal/cost"
	apidriver "github.com/VoterBloc/gollm-qa/internal/driver/api"
	"github.com/VoterBloc/gollm-qa/internal/introspect"
	"github.com/VoterBloc/gollm-qa/internal/persona"
	"github.com/VoterBloc/gollm-qa/internal/provider"
	_ "github.com/VoterBloc/gollm-qa/internal/provider/claude" // registers "claude" provider
	_ "github.com/VoterBloc/gollm-qa/internal/provider/gemini" // registers "gemini" provider
	_ "github.com/VoterBloc/gollm-qa/internal/provider/local"  // registers "local" provider (Ollama / OpenAI-compatible)
	_ "github.com/VoterBloc/gollm-qa/internal/provider/openai" // registers "openai" provider
	"github.com/VoterBloc/gollm-qa/internal/reporter"
	"github.com/VoterBloc/gollm-qa/internal/server"
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
	case "seed":
		err = seedCmd(os.Args[2:])
	case "serve":
		err = serveCmd(os.Args[2:])
	case "healthcheck":
		err = healthcheckCmd(os.Args[2:])
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
  gollm seed --config <path> --campaign <path> --output <dir> [flags]
  gollm run --config <path> --personas <dir> [flags]
  gollm purge --config <path>
  gollm serve [--addr :8080] [--apps apps] [--campaigns campaigns] [--personas personas] [--clerk-issuer URL]
  gollm healthcheck [--addr :8080] [--url URL] [--timeout 2s]

Run "gollm <subcommand> -h" for subcommand-specific flags.
`)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var (
		configPath      string
		personaDir      string
		maxAgents       int
		outputDir       string
		maxSteps        int
		concurrency     int
		stepDelay       time.Duration
		pricingPath     string
		modelSpec       string
		budgetPerAgent  float64
		maxRunCostUSD   float64
	)
	fs.StringVar(&configPath, "config", "", "path to app config YAML (required)")
	fs.StringVar(&personaDir, "personas", "", "path to persona directory (required)")
	fs.IntVar(&maxAgents, "agents", 0, "max agents to run (default: all personas)")
	fs.StringVar(&outputDir, "output", "reports", "output directory for reports")
	fs.IntVar(&maxSteps, "max-steps", 50, "max steps per agent")
	fs.IntVar(&concurrency, "concurrency", 3, "max concurrent agents")
	fs.DurationVar(&stepDelay, "step-delay", 0, "delay between agent steps (e.g. 1s)")
	fs.StringVar(&pricingPath, "pricing", "", "optional pricing YAML path; merges over the embedded defaults")
	fs.StringVar(&modelSpec, "model", "", "<provider>:<model> spec (e.g. claude:sonnet-4-5, openai:gpt-4o); overrides app config's default_model")
	fs.Float64Var(&budgetPerAgent, "budget-per-agent", 0, "USD soft ceiling per agent; agent gets one wrap-up turn after crossing. 0 = no limit")
	fs.Float64Var(&maxRunCostUSD, "max-run-cost-usd", 5, "USD soft ceiling for the entire run; in-flight agents wrap up once the aggregate is exceeded and no new agents start. 0 = no limit")
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

	if appCfg.ToolsFromSchema {
		logger.Info("introspecting GraphQL schema", "base_url", appCfg.BaseURL)
		schema, err := introspect.Introspect(ctx, appCfg.BaseURL, nil)
		if err != nil {
			return fmt.Errorf("introspecting schema: %w", err)
		}
		var unmatched []string
		appCfg.Tools, unmatched = introspect.GenerateTools(schema, introspect.Options{
			Include: appCfg.ToolsInclude,
			Exclude: appCfg.ToolsExclude,
		})
		if len(unmatched) > 0 {
			// Almost always a snake_case-vs-camelCase mistake. Surface loud.
			logger.Warn("tools_include / tools_exclude entries did not match any schema operation",
				"unmatched", unmatched)
		}
		if len(appCfg.Tools) == 0 {
			return fmt.Errorf("introspection produced zero tools — check tools_include / tools_exclude in %s", configPath)
		}
		logger.Info("generated tools from schema", "count", len(appCfg.Tools))
	}

	pricing, err := cost.Load(pricingPath)
	if err != nil {
		return fmt.Errorf("loading pricing: %w", err)
	}

	resolvedSpec := provider.ResolveSpec(modelSpec, appCfg.DefaultModel)
	llm, err := provider.New(resolvedSpec)
	if err != nil {
		return fmt.Errorf("model %q: %w", resolvedSpec, err)
	}
	logger.Info("using model", "spec", resolvedSpec)

	// Aggregate run-cost tracking. The OnUsage closure feeds every
	// agent's per-turn cost into a shared counter; once the total
	// crosses --max-run-cost-usd, returning true triggers each in-flight
	// agent's wrap-up path. Same flag also gates whether new agents
	// get queued below.
	var (
		runMu        sync.Mutex
		aggregateUSD float64
	)
	overBudget := func() bool {
		runMu.Lock()
		defer runMu.Unlock()
		return maxRunCostUSD > 0 && aggregateUSD > maxRunCostUSD
	}
	onUsage := func(turnUSD float64) bool {
		runMu.Lock()
		defer runMu.Unlock()
		aggregateUSD += turnUSD
		return maxRunCostUSD > 0 && aggregateUSD > maxRunCostUSD
	}

	agentCfg := agent.Config{
		MaxSteps:          maxSteps,
		StepDelay:         stepDelay,
		Cost:              pricing,
		BudgetPerAgentUSD: budgetPerAgent,
		OnUsage:           onUsage,
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

	var mu sync.Mutex
	var sessions []*agent.Session
	var errored int // agents whose Run() returned an error and bailed

	fmt.Fprintf(os.Stderr, "\nStarting %d agents against %s (concurrency: %d, max steps: %d)\n\n",
		len(personas), appCfg.Name, concurrency, maxSteps)

	queued := 0
	for _, p := range personas {
		// Pre-launch check — once the aggregate ceiling is crossed,
		// stop queuing new agents. errgroup.SetLimit blocks Go() while
		// the concurrency window is full, so this check sees the
		// latest aggregate before each new launch.
		if overBudget() {
			logger.Info("run budget ceiling reached, skipping remaining agents",
				"max_run_cost_usd", maxRunCostUSD)
			break
		}
		queued++
		g.Go(func() error {
			drv := apidriver.New(appCfg, logger)
			a := agent.New(p, llm, drv, agentCfg, logger)

			session, err := a.Run(ctx)
			if err != nil {
				logger.Error("agent failed", "agent", p.Name, "error", err)
				mu.Lock()
				errored++
				mu.Unlock()
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
	skipped := len(personas) - queued

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

	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stderr, agent.SummarizeRun(sessions, skipped, errored).Format())
	fmt.Fprintf(os.Stderr, "  Actions: %d, Errors: %d, UX notes: %d\n", totalActions, totalErrors, totalUXNotes)
	fmt.Fprintf(os.Stderr, "  Reports: %s/\n", outputDir)

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
	if appCfg.Admin.TokenEnv == "" {
		return fmt.Errorf("config %s is missing admin.token_env", configPath)
	}

	adminToken := os.Getenv(appCfg.Admin.TokenEnv)
	if adminToken == "" {
		return fmt.Errorf("admin token not set: export %s", appCfg.Admin.TokenEnv)
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
	drv.SetAuthToken(adminToken)

	logger.Info("running purge", "token_env", appCfg.Admin.TokenEnv)
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

func seedCmd(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	var (
		configPath   string
		campaignPath string
		outputDir    string
		modelSpec    string
	)
	fs.StringVar(&configPath, "config", "", "path to app config YAML (required)")
	fs.StringVar(&campaignPath, "campaign", "", "path to campaign YAML describing cohorts to generate (required)")
	fs.StringVar(&outputDir, "output", "", "directory to write generated persona YAMLs into (required)")
	fs.StringVar(&modelSpec, "model", "", "<provider>:<model> spec for persona generation; overrides app config's default_model")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if configPath == "" || campaignPath == "" || outputDir == "" {
		fs.Usage()
		return fmt.Errorf("--config, --campaign, and --output are required")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	appCfg, err := config.LoadAppConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading app config: %w", err)
	}
	if len(appCfg.PersonaRegisterTemplate) == 0 {
		logger.Warn("app config has no persona_register_template; generated personas will lack register_input")
	}

	campaign, err := config.LoadCampaign(campaignPath)
	if err != nil {
		return fmt.Errorf("loading campaign: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	resolvedSpec := provider.ResolveSpec(modelSpec, appCfg.DefaultModel)
	llm, err := provider.New(resolvedSpec)
	if err != nil {
		return fmt.Errorf("model %q: %w", resolvedSpec, err)
	}
	logger.Info("using model", "spec", resolvedSpec)

	gen := persona.NewGenerator(llm)
	seen := persona.NewSeenIdentities()

	fmt.Fprintf(os.Stderr, "\nSeeding %d personas across %d cohorts for %s\n",
		campaign.TotalPersonas(), len(campaign.Cohorts), appCfg.Name)
	fmt.Fprintf(os.Stderr, "Output: %s\n\n", outputDir)

	totalWritten := 0
	for _, cohort := range campaign.Cohorts {
		logger.Info("generating cohort", "cohort", cohort.Name, "count", cohort.Count)

		identities, err := gen.Generate(ctx, cohort.Brief, campaign.BriefGlobal, cohort.Count)
		if err != nil {
			return fmt.Errorf("cohort %s: %w", cohort.Name, err)
		}
		if len(identities) < cohort.Count {
			logger.Warn("model returned fewer personas than requested",
				"cohort", cohort.Name, "requested", cohort.Count, "got", len(identities))
		}

		if renamed := persona.Dedupe(identities, seen); len(renamed) > 0 {
			logger.Warn("renamed personas to dedupe email/username collisions",
				"cohort", cohort.Name, "count", len(renamed), "names", renamed)
		}

		for _, id := range identities {
			path, err := persona.Write(id, persona.WriteOptions{
				OutputDir:        outputDir,
				CohortName:       cohort.Name,
				CampaignName:     filepath.Base(campaignPath),
				RegisterTemplate: appCfg.PersonaRegisterTemplate,
			})
			if err != nil {
				return fmt.Errorf("cohort %s: writing %s: %w", cohort.Name, id.FullName(), err)
			}
			logger.Info("wrote persona", "cohort", cohort.Name, "name", id.FullName(), "path", path)
			totalWritten++
		}
	}

	fmt.Fprintf(os.Stderr, "\nDone. Wrote %d personas to %s/\n", totalWritten, outputDir)
	return nil
}

func serveCmd(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var (
		addr         string
		appsDir      string
		campaignsDir string
		personasDir  string
		clerkIssuer  string
	)
	fs.StringVar(&addr, "addr", ":8080", "HTTP listen address")
	fs.StringVar(&appsDir, "apps", "apps", "directory containing app configs (.yaml)")
	fs.StringVar(&campaignsDir, "campaigns", "campaigns", "directory containing campaign briefs (.yaml)")
	fs.StringVar(&personasDir, "personas", "personas", "directory containing persona files and collections")
	fs.StringVar(&clerkIssuer, "clerk-issuer", os.Getenv("COHORT_CLERK_ISSUER"),
		"Clerk issuer URL (e.g. https://your-app.clerk.accounts.dev). Empty = dev mode, no auth. Also reads from COHORT_CLERK_ISSUER.")
	var pricingPath string
	fs.StringVar(&pricingPath, "pricing", "", "optional pricing YAML path; merges over the embedded defaults")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pricing, err := cost.Load(pricingPath)
	if err != nil {
		return fmt.Errorf("loading pricing: %w", err)
	}

	srv, err := server.New(server.Config{
		Addr:         addr,
		ConfigsDir:   appsDir,
		CampaignsDir: campaignsDir,
		PersonasDir:  personasDir,
		ClerkIssuer:  clerkIssuer,
		Cost:         pricing,
	}, logger)
	if err != nil {
		return fmt.Errorf("init server: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return srv.Run(ctx)
}
