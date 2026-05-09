package server

import (
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
	"github.com/VoterBloc/gollm-qa/internal/provider/claude"
)

// RunRequest is the JSON body shape for POST /v1/runs.
type RunRequest struct {
	ConfigName string `json:"config_name"`
	PersonaSet string `json:"persona_set"`

	// MaxSteps caps how many tool-call rounds each agent takes. Optional;
	// nil/0 falls back to defaultMaxSteps. Capped at maxAllowedSteps as a
	// runaway-cost guard.
	MaxSteps int `json:"max_steps,omitempty"`
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
	if req.ConfigName == "" || req.PersonaSet == "" {
		writeError(w, http.StatusBadRequest, errors.New("config_name and persona_set are required"))
		return
	}

	configPath, err := resolveYAMLByName(s.cfg.ConfigsDir, req.ConfigName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	personaDir, err := resolvePersonaCollection(s.cfg.PersonasDir, req.PersonaSet)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	appCfg, err := config.LoadAppConfig(configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("loading config: %w", err))
		return
	}
	personas, err := config.LoadPersonas(personaDir)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("loading personas: %w", err))
		return
	}
	if len(personas) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("persona collection %q has no personas", req.PersonaSet))
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
		Kind:    runEventStart,
		At:      time.Now(),
		Payload: map[string]any{"config": req.ConfigName, "persona_set": req.PersonaSet, "agents": len(personas)},
	}})

	if appCfg.ToolsFromSchema {
		if err := s.introspectIntoConfig(r.Context(), appCfg); err != nil {
			sse.writeRunError(fmt.Errorf("introspecting schema: %w", err))
			sse.write(RunEvent{Event: agent.Event{Kind: runEventEnd, At: time.Now()}})
			return
		}
	}

	s.runAgents(r.Context(), appCfg, personas, req.maxSteps(), sse)
	sse.write(RunEvent{Event: agent.Event{Kind: runEventEnd, At: time.Now()}})
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

// runAgents fans out personas across goroutines (bounded concurrency),
// each writing events to the shared SSE stream via the persona-tagged
// callback. Returns when every agent has finished.
func (s *Server) runAgents(ctx context.Context, appCfg *config.AppConfig, personas []*agent.Persona, maxSteps int, sse *sseWriter) {
	provFn := s.cfg.ProviderFactory
	if provFn == nil {
		provFn = func() provider.Provider { return claude.New() }
	}
	drvFn := s.cfg.DriverFactory
	if drvFn == nil {
		drvFn = func(appCfg *config.AppConfig, logger *slog.Logger) driver.Driver {
			return apidriver.New(appCfg, logger)
		}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, runConcurrency)

	for _, p := range personas {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(p *agent.Persona) {
			defer wg.Done()
			defer func() { <-sem }()

			drv := drvFn(appCfg, s.logger)
			llm := provFn()
			cfg := agent.Config{
				MaxSteps: maxSteps,
				OnEvent: func(ev agent.Event) {
					sse.write(RunEvent{Persona: p.Name, Event: ev})
				},
			}
			a := agent.New(p, llm, drv, cfg, s.logger)
			if _, err := a.Run(ctx); err != nil {
				s.logger.Error("agent failed", "persona", p.Name, "error", err)
				sse.write(RunEvent{Persona: p.Name, Event: agent.Event{
					Kind:    runEventError,
					At:      time.Now(),
					Payload: map[string]string{"error": err.Error()},
				}})
			}
		}(p)
	}

	wg.Wait()
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

// resolvePersonaCollection checks that <dir>/<name>/ exists, is a
// directory, and contains at least one .yaml file. Loose top-level
// .yaml personas aren't accepted as a "persona set" — those are
// individual personas; runs need a collection.
func resolvePersonaCollection(dir, name string) (string, error) {
	if dir == "" {
		return "", errors.New("server has no personas directory configured")
	}
	p := filepath.Join(dir, name)
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("persona collection %q not found", name)
	}
	count, err := countYAMLFiles(p)
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", fmt.Errorf("persona collection %q has no .yaml files", name)
	}
	return p, nil
}
