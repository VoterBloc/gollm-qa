package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/VoterBloc/gollm-qa/internal/agent"
	"github.com/VoterBloc/gollm-qa/internal/config"
	"github.com/VoterBloc/gollm-qa/internal/driver"
	apidriver "github.com/VoterBloc/gollm-qa/internal/driver/api"
	"github.com/VoterBloc/gollm-qa/internal/introspect"
	"github.com/VoterBloc/gollm-qa/internal/provider"
	_ "github.com/VoterBloc/gollm-qa/internal/provider/claude" // registers "claude" provider
	_ "github.com/VoterBloc/gollm-qa/internal/provider/openai" // registers "openai" provider
)

// jsonNull matches JSON's null literal — `json.RawMessage` retains the
// 4-byte literal `null` for explicit nulls, which we want to treat the
// same as field-absent.
var jsonNull = []byte("null")

// hasInline reports whether a json.RawMessage represents real content
// (not absent, not the JSON null literal). Clients that pre-fill
// request shapes with `null` should be indistinguishable from clients
// that omit the field entirely.
func hasInline(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), jsonNull)
}

// RunRequest is the JSON body shape for POST /v1/runs.
//
// Each input has two ways to arrive at the engine:
//   - by name (CLI / panel-mirroring use case): ConfigName / PersonaSet
//     lookups against the engine's configured directories.
//   - inline (panel-with-its-own-DB use case): Config / Personas carry the
//     parsed YAML content as JSON.
//
// Validation requires exactly one of each pair. Mixing is allowed (e.g.
// inline config + named persona set) and useful in practice.
type RunRequest struct {
	ConfigName string          `json:"config_name,omitempty"`
	Config     json.RawMessage `json:"config,omitempty"`
	PersonaSet string          `json:"persona_set,omitempty"`
	Personas   json.RawMessage `json:"personas,omitempty"`

	// MaxSteps caps how many tool-call rounds each agent takes. Optional;
	// nil/0 falls back to defaultMaxSteps. Capped at maxAllowedSteps as a
	// runaway-cost guard.
	MaxSteps int `json:"max_steps,omitempty"`

	// Model is the "<provider>:<model>" spec for this run. Optional;
	// when omitted, falls back to the resolved app-config default,
	// then to provider.DefaultModelSpec. Unknown specs return 400
	// before any agent starts.
	Model string `json:"model,omitempty"`

	// BudgetPerAgentUSD caps each agent's spend at the given USD
	// estimate. When the running cost crosses the ceiling mid-loop,
	// the agent gets one wrap-up turn before exiting with
	// stop_reason="budget_exhausted". Zero (default) = no limit.
	BudgetPerAgentUSD float64 `json:"budget_per_agent_usd,omitempty"`

	// MaxRunCostUSD caps the aggregate spend across all agents in
	// this run. When the total crosses the ceiling, in-flight agents
	// receive the wrap-up nudge and no new agents start. Zero
	// (default) = no limit; the server has no opinion about the right
	// ceiling for arbitrary requests, so the CLI's $5 default doesn't
	// carry over here.
	MaxRunCostUSD float64 `json:"max_run_cost_usd,omitempty"`
}

const (
	defaultMaxSteps  = 50
	maxAllowedSteps  = 200
)

// maxSteps returns the effective per-agent step cap, applying defaults
// and the ceiling.
func (r RunRequest) maxSteps() int {
	if r.MaxSteps <= 0 {
		return defaultMaxSteps
	}
	if r.MaxSteps > maxAllowedSteps {
		return maxAllowedSteps
	}
	return r.MaxSteps
}

// RunEvent is what the engine streams over SSE — one envelope per agent
// event, plus run-level events emitted by the orchestrator itself.
//
// Persona is empty for run-level events ("run_start", "run_end") and
// populated for per-agent events (everything emitted by Agent.OnEvent).
type RunEvent struct {
	Persona string      `json:"persona,omitempty"`
	Event   agent.Event `json:"event"`
}

// Run-level event kinds emitted by the orchestrator (not the agent).
const (
	runEventStart = agent.EventKind("run_start")
	runEventEnd   = agent.EventKind("run_end")
	runEventError = agent.EventKind("run_error")
)

// runConcurrency caps how many agents run simultaneously per request.
// Matches the CLI default. Could be made configurable per-run later.
const runConcurrency = 3

func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	req.ConfigName = strings.TrimSpace(req.ConfigName)
	req.PersonaSet = strings.TrimSpace(req.PersonaSet)

	appCfg, err := s.resolveConfig(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	personas, err := s.resolvePersonas(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(personas) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("no personas to run"))
		return
	}

	// Budget enforcement requires a pricing table — without one we
	// can't estimate cost, so the budget would silently no-op. Surface
	// that mismatch as a 400 here rather than at the boundary where
	// the user discovers their cap didn't fire.
	if (req.BudgetPerAgentUSD > 0 || req.MaxRunCostUSD > 0) && s.cfg.Cost == nil {
		writeError(w, http.StatusBadRequest, errors.New(
			"budget enforcement (budget_per_agent_usd / max_run_cost_usd) requires a pricing table; restart the server with --pricing"))
		return
	}

	// Resolve and validate the model spec before opening the SSE stream.
	// An unknown prefix here should land as a clean 400, not as a mid-stream
	// run_error after the client has already committed to a long-poll.
	modelSpec := provider.ResolveSpec(req.Model, appCfg.DefaultModel)
	provFn := s.cfg.ProviderFactory
	if provFn == nil {
		provFn = provider.New
	}
	llm, err := provFn(modelSpec)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("model %q: %w", modelSpec, err))
		return
	}

	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // hint to nginx/etc. not to buffer
	w.WriteHeader(http.StatusOK)

	sse := &sseWriter{w: w, rc: rc, logger: s.logger}

	// Probe flushing capability up front. If the underlying writer truly
	// doesn't support Flush (rare in production, but possible behind some
	// reverse-proxies), we'd rather fail with a visible run_error than
	// silently buffer events for the whole run.
	if err := rc.Flush(); err != nil {
		sse.writeRunError(fmt.Errorf("response writer doesn't support flushing: %w", err))
		return
	}

	stopKeepalive := sse.startKeepalive(r.Context())
	defer stopKeepalive()

	sse.write(RunEvent{Event: agent.Event{
		Kind: runEventStart,
		At:   time.Now(),
		Payload: map[string]any{
			"config":  appCfg.Name,
			"agents":  len(personas),
			"sources": runSources(req),
		},
	}})

	if appCfg.ToolsFromSchema {
		if err := s.introspectIntoConfig(r.Context(), appCfg); err != nil {
			sse.writeRunError(fmt.Errorf("introspecting schema: %w", err))
			sse.write(RunEvent{Event: agent.Event{Kind: runEventEnd, At: time.Now()}})
			return
		}
	}

	res := s.runAgents(r.Context(), appCfg, personas, req.maxSteps(), req.BudgetPerAgentUSD, req.MaxRunCostUSD, llm, sse)
	sse.write(RunEvent{Event: agent.Event{
		Kind:    runEventEnd,
		At:      time.Now(),
		Payload: agent.SummarizeRun(res.Sessions, res.Skipped, res.Errored),
	}})
}

// introspectIntoConfig populates appCfg.Tools from the live GraphQL
// schema, mirroring runCmd's behavior.
func (s *Server) introspectIntoConfig(ctx context.Context, appCfg *config.AppConfig) error {
	schema, err := introspect.Introspect(ctx, appCfg.BaseURL, nil)
	if err != nil {
		return err
	}
	tools, unmatched := introspect.GenerateTools(schema, introspect.Options{
		Include: appCfg.ToolsInclude,
		Exclude: appCfg.ToolsExclude,
	})
	if len(unmatched) > 0 {
		s.logger.Warn("tools_include / tools_exclude entries did not match any schema operation", "unmatched", unmatched)
	}
	if len(tools) == 0 {
		return errors.New("introspection produced zero tools")
	}
	appCfg.Tools = tools
	return nil
}

// runResult bundles everything the caller needs to roll a run_end
// summary: completed sessions plus the orchestrator-level counts of
// agents that never produced one (skipped by the run-level cap; or
// returned an error from Run()).
type runResult struct {
	Sessions []*agent.Session
	Skipped  int
	Errored  int
}

// runAgents fans out personas across goroutines (bounded concurrency),
// each writing events to the shared SSE stream via the persona-tagged
// callback. Returns the runResult so the caller can roll a complete
// run summary into run_end (skipped + errored agents have to be
// counted at this boundary — they never produce a Session).
//
// llm is shared across every agent in the run — Anthropic / OpenAI SDK
// clients are concurrency-safe, and the stubProvider used in tests
// serializes Chat calls under a mutex.
//
// maxRunCostUSD is the aggregate ceiling. Non-zero enables it: in-flight
// agents get the wrap-up nudge via OnUsage's stop-return, and the
// fan-out loop stops queuing new agents once the aggregate is crossed.
// Zero leaves enforcement off — same shape as BudgetPerAgentUSD.
func (s *Server) runAgents(ctx context.Context, appCfg *config.AppConfig, personas []*agent.Persona, maxSteps int, budgetPerAgent, maxRunCostUSD float64, llm provider.Provider, sse *sseWriter) runResult {
	drvFn := s.cfg.DriverFactory
	if drvFn == nil {
		drvFn = func(appCfg *config.AppConfig, logger *slog.Logger) driver.Driver {
			return apidriver.New(appCfg, logger)
		}
	}

	// Aggregate-cost tracking shared across all agents in this run.
	// onUsage signals stop=true once the running total crosses the cap,
	// triggering the wrap-up path inside Agent.Run (same machinery
	// per-agent budget uses, just a different trigger).
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

	// Sessions and the errored counter share a mutex — both written
	// from goroutines, both read once after wg.Wait() returns.
	var (
		sessionMu sync.Mutex
		sessions  []*agent.Session
		errored   int
	)

	var wg sync.WaitGroup
	sem := make(chan struct{}, runConcurrency)

	queued := 0
	for _, p := range personas {
		// Pre-launch budget check — skip remaining personas once the
		// aggregate ceiling has been crossed by already-running agents.
		// Active goroutines see the same ceiling via their next OnUsage
		// and enter wrap-up.
		if overBudget() {
			s.logger.Info("run budget ceiling reached, skipping remaining agents",
				"max_run_cost_usd", maxRunCostUSD)
			break
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return runResult{
				Sessions: sessions,
				Skipped:  len(personas) - queued,
				Errored:  errored,
			}
		case sem <- struct{}{}:
		}

		queued++
		wg.Add(1)
		go func(p *agent.Persona) {
			defer wg.Done()
			defer func() { <-sem }()

			drv := drvFn(appCfg, s.logger)
			cfg := agent.Config{
				MaxSteps:          maxSteps,
				Cost:              s.cfg.Cost,
				BudgetPerAgentUSD: budgetPerAgent,
				OnUsage:           onUsage,
				OnEvent: func(ev agent.Event) {
					sse.write(RunEvent{Persona: p.Name, Event: ev})
				},
			}
			a := agent.New(p, llm, drv, cfg, s.logger)
			session, err := a.Run(ctx)
			if err != nil {
				s.logger.Error("agent failed", "persona", p.Name, "error", err)
				sse.write(RunEvent{Persona: p.Name, Event: agent.Event{
					Kind:    runEventError,
					At:      time.Now(),
					Payload: map[string]string{"error": err.Error()},
				}})
				sessionMu.Lock()
				errored++
				sessionMu.Unlock()
				return
			}
			sessionMu.Lock()
			sessions = append(sessions, session)
			sessionMu.Unlock()
		}(p)
	}

	wg.Wait()
	return runResult{
		Sessions: sessions,
		Skipped:  len(personas) - queued,
		Errored:  errored,
	}
}

// sseWriter serializes writes to the response — multiple agents may
// emit events concurrently, but http.ResponseWriter is not safe for
// concurrent use. Flushing goes through http.ResponseController so it
// walks the wrapper chain (statusRecorder → real writer) properly.
type sseWriter struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	rc     *http.ResponseController
	logger *slog.Logger
}

func (s *sseWriter) write(ev RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, err := json.Marshal(ev)
	if err != nil {
		s.logger.Error("marshal SSE event", "err", err)
		return
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", body); err != nil {
		// Client disconnected — nothing to do, the request context will
		// cancel on its own and agents will see ctx.Err() shortly.
		return
	}
	if err := s.rc.Flush(); err != nil {
		s.logger.Error("flush SSE event", "err", err)
	}
}

func (s *sseWriter) writeRunError(err error) {
	s.write(RunEvent{Event: agent.Event{
		Kind:    runEventError,
		At:      time.Now(),
		Payload: map[string]string{"error": err.Error()},
	}})
}

// keepaliveInterval is how often the SSE handler sends a comment line
// to keep the connection alive through reverse-proxies and load
// balancers. Cloud Run idles connections after ~30s of silence; nginx
// and corporate proxies tend to be around 60s. 15s gives a comfortable
// margin without being chatty.
const keepaliveInterval = 15 * time.Second

// startKeepalive launches a goroutine that writes an SSE comment line
// (`: keepalive\n\n`, ignored by EventSource clients) on a fixed
// interval. Stops when ctx is done or when the returned function is
// called. Callers should defer the returned stop function to ensure
// the goroutine exits even when the handler returns early via an
// error path.
func (s *sseWriter) startKeepalive(ctx context.Context) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(keepaliveInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-t.C:
				s.writeRaw(": keepalive\n\n")
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// writeRaw emits a literal byte sequence to the stream — used for the
// keepalive comment line and any other non-JSON SSE framing. Goes
// through the same mutex as write() so concurrent emission is safe.
func (s *sseWriter) writeRaw(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprint(s.w, text); err != nil {
		return
	}
	if err := s.rc.Flush(); err != nil {
		s.logger.Error("flush SSE keepalive", "err", err)
	}
}

// resolveConfig returns the *config.AppConfig for the run, sourced
// either from inline JSON content or from a name lookup against the
// configured ConfigsDir. Validation: exactly one of ConfigName / Config
// must be set.
func (s *Server) resolveConfig(req RunRequest) (*config.AppConfig, error) {
	haveName := req.ConfigName != ""
	haveInline := hasInline(req.Config)
	if haveName && haveInline {
		return nil, errors.New("config_name and config are mutually exclusive — set exactly one")
	}
	if !haveName && !haveInline {
		return nil, errors.New("config_name or config is required")
	}
	if haveInline {
		appCfg, err := config.ParseAppConfig(req.Config)
		if err != nil {
			return nil, fmt.Errorf("inline config invalid: %w", err)
		}
		return appCfg, nil
	}
	path, err := resolveYAMLByName(s.cfg.ConfigsDir, req.ConfigName)
	if err != nil {
		return nil, err
	}
	appCfg, err := config.LoadAppConfig(path)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	return appCfg, nil
}

// resolvePersonas returns the personas for the run, sourced either from
// inline JSON (an array of persona objects) or from a named collection
// on disk. Validation: exactly one of PersonaSet / Personas must be set,
// and the inline array (if used) must be non-empty.
func (s *Server) resolvePersonas(req RunRequest) ([]*agent.Persona, error) {
	haveName := req.PersonaSet != ""
	haveInline := hasInline(req.Personas)
	if haveName && haveInline {
		return nil, errors.New("persona_set and personas are mutually exclusive — set exactly one")
	}
	if !haveName && !haveInline {
		return nil, errors.New("persona_set or personas is required")
	}
	if haveInline {
		var raws []json.RawMessage
		if err := json.Unmarshal(req.Personas, &raws); err != nil {
			return nil, fmt.Errorf("personas must be a JSON array: %w", err)
		}
		if len(raws) == 0 {
			return nil, errors.New("personas array is empty")
		}
		out := make([]*agent.Persona, 0, len(raws))
		for i, raw := range raws {
			p, err := config.ParsePersona(raw)
			if err != nil {
				return nil, fmt.Errorf("personas[%d]: %w", i, err)
			}
			out = append(out, p)
		}
		return out, nil
	}
	return s.resolveNamedPersonaSet(req.PersonaSet)
}

// runSources reports where each input came from for the run_start
// event payload. Lets the panel show "this run was submitted with
// inline config + named persona set" without needing to keep its own
// derivation.
func runSources(req RunRequest) map[string]string {
	source := func(name, inline bool) string {
		switch {
		case inline:
			return "inline"
		case name:
			return "name"
		default:
			return ""
		}
	}
	return map[string]string{
		"config":   source(req.ConfigName != "", hasInline(req.Config)),
		"personas": source(req.PersonaSet != "", hasInline(req.Personas)),
	}
}

// resolveYAMLByName checks that <dir>/<name>.yaml or .yml exists. Used
// at submit time so we 4xx fast on typos instead of failing later in
// the run.
func resolveYAMLByName(dir, name string) (string, error) {
	if dir == "" {
		return "", errors.New("server has no configs directory configured")
	}
	for _, ext := range []string{".yaml", ".yml"} {
		p := filepath.Join(dir, name+ext)
		info, err := os.Stat(p)
		if err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("config %q not found", name)
}

// resolveNamedPersonaSet maps a persona_set name to one or more
// loaded *agent.Persona, accepting either:
//
//  1. A directory at <dir>/<name>/ containing one or more .yaml files
//     — the original "collection" shape. Wins on ambiguity if both
//     forms exist.
//  2. A single file at <dir>/<name>.yaml (or .yml) — a singleton
//     "set of one." Lets a run pick one persona by name without first
//     wrapping it in a directory, matching how /v1/personas already
//     lists both shapes side by side (#49).
//
// Anything else returns the same `persona collection %q not found`
// error the original collection-only resolver did, so existing 400
// responses don't change shape for the bad-input case.
func (s *Server) resolveNamedPersonaSet(name string) ([]*agent.Persona, error) {
	if s.cfg.PersonasDir == "" {
		return nil, errors.New("server has no personas directory configured")
	}

	// Collection wins on ambiguity: if `<dir>/<name>/` is a directory,
	// use it even if `<dir>/<name>.yaml` also exists. Locking this
	// order in matches the issue's acceptance criterion and keeps
	// existing collections that happened to share a name with a stray
	// file on disk continuing to work.
	collectionDir := filepath.Join(s.cfg.PersonasDir, name)
	if info, err := os.Stat(collectionDir); err == nil && info.IsDir() {
		personas, err := config.LoadPersonas(collectionDir)
		if err != nil {
			return nil, fmt.Errorf("loading personas: %w", err)
		}
		if len(personas) == 0 {
			return nil, fmt.Errorf("persona collection %q has no .yaml files", name)
		}
		return personas, nil
	}

	// Singleton-file fallback. Try .yaml first then .yml; both are
	// accepted everywhere else the loader runs.
	for _, ext := range []string{".yaml", ".yml"} {
		filePath := filepath.Join(s.cfg.PersonasDir, name+ext)
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		p, err := config.ParsePersona(data)
		if err != nil {
			return nil, fmt.Errorf("parsing persona %s: %w", name+ext, err)
		}
		return []*agent.Persona{p}, nil
	}

	return nil, fmt.Errorf("persona collection %q not found", name)
}
