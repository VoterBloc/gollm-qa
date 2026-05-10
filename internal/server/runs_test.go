package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/VoterBloc/gollm-qa/internal/config"
	"github.com/VoterBloc/gollm-qa/internal/cost"
	"github.com/VoterBloc/gollm-qa/internal/driver"
	"github.com/VoterBloc/gollm-qa/internal/provider"
)

func TestCreateRun_RejectsInvalidJSON(t *testing.T) {
	srv := mustNewServer(t, Config{})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader("not json {"))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid JSON") {
		t.Errorf("body should mention invalid JSON, got %s", w.Body.String())
	}
}

func TestCreateRun_RejectsMissingFields(t *testing.T) {
	srv := mustNewServer(t, Config{})
	cases := []struct {
		name      string
		body      string
		bodyMatch string // a fragment that should appear in the error response
	}{
		{"empty body", `{}`, "config_name or config is required"},
		{"missing config", `{"persona_set":"hauntings"}`, "config_name or config is required"},
		{"empty config_name + empty persona_set", `{"config_name":"  ","persona_set":""}`, "config_name or config is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.bodyMatch) {
				t.Errorf("body: want substring %q, got %s", tc.bodyMatch, w.Body.String())
			}
		})
	}
}

func TestCreateRun_RejectsUnknownConfig(t *testing.T) {
	configsDir := t.TempDir()
	personasDir := makePersonaCollection(t, "okay-collection", []string{"phantom.yaml"})
	srv := mustNewServer(t, Config{ConfigsDir: configsDir, PersonasDir: personasDir})

	body := `{"config_name":"definitely-not-a-real-config","persona_set":"okay-collection"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `definitely-not-a-real-config`) || !strings.Contains(w.Body.String(), "not found") {
		t.Errorf("body should name the missing config, got %s", w.Body.String())
	}
}

func TestCreateRun_RejectsUnknownPersonaSet(t *testing.T) {
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "ghost-watch.yaml"), validAppConfigYAML())
	personasDir := t.TempDir()
	srv := mustNewServer(t, Config{ConfigsDir: configsDir, PersonasDir: personasDir})

	body := `{"config_name":"ghost-watch","persona_set":"missing-coven"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `missing-coven`) || !strings.Contains(w.Body.String(), "not found") {
		t.Errorf("body should name the missing collection, got %s", w.Body.String())
	}
}

func TestCreateRun_RejectsEmptyPersonaCollection(t *testing.T) {
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "ghost-watch.yaml"), validAppConfigYAML())
	personasDir := t.TempDir()
	mustMkdir(t, filepath.Join(personasDir, "abandoned-coven"))
	srv := mustNewServer(t, Config{ConfigsDir: configsDir, PersonasDir: personasDir})

	body := `{"config_name":"ghost-watch","persona_set":"abandoned-coven"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no .yaml files") {
		t.Errorf("body should mention empty collection, got %s", w.Body.String())
	}
}

func TestCreateRun_RejectsBothConfigForms(t *testing.T) {
	personasDir := makePersonaCollection(t, "okay", []string{"x.yaml"})
	srv := mustNewServer(t, Config{PersonasDir: personasDir})

	body := `{"config_name":"x","config":{"name":"inline","base_url":"http://x"},"persona_set":"okay"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mutually exclusive") {
		t.Errorf("body should mention mutually exclusive, got %s", w.Body.String())
	}
}

func TestCreateRun_RejectsBothPersonaForms(t *testing.T) {
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML())
	personasDir := makePersonaCollection(t, "okay", []string{"x.yaml"})
	srv := mustNewServer(t, Config{ConfigsDir: configsDir, PersonasDir: personasDir})

	body := `{"config_name":"lochness","persona_set":"okay","personas":[{"name":"x"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "mutually exclusive") {
		t.Errorf("body should mention mutually exclusive, got %s", w.Body.String())
	}
}

func TestCreateRun_RejectsEmptyInlinePersonasArray(t *testing.T) {
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML())
	srv := mustNewServer(t, Config{ConfigsDir: configsDir})

	body := `{"config_name":"lochness","personas":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "empty") {
		t.Errorf("body should mention empty array, got %s", w.Body.String())
	}
}

func TestCreateRun_RejectsInlineConfigMissingRequiredFields(t *testing.T) {
	personasDir := makePersonaCollection(t, "okay", []string{"x.yaml"})
	srv := mustNewServer(t, Config{PersonasDir: personasDir})

	// Inline config missing required base_url — ParseAppConfig validate() rejects.
	body := `{"config":{"name":"missing-base-url"},"persona_set":"okay"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "inline config invalid") || !strings.Contains(w.Body.String(), "base_url") {
		t.Errorf("body should mention inline config + base_url, got %s", w.Body.String())
	}
}

func TestCreateRun_TreatsExplicitNullsAsAbsent(t *testing.T) {
	// A panel that pre-fills request shapes might send `"config": null`
	// alongside a real config_name. That should be treated the same as
	// "config absent", not flagged as mutually-exclusive.
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML())
	personasDir := makePersonaCollection(t, "okay", []string{"x.yaml"})
	srv := mustNewServer(t, Config{ConfigsDir: configsDir, PersonasDir: personasDir})

	body := `{"config_name":"lochness","config":null,"persona_set":"okay","personas":null}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	// Should NOT 400 with mutually-exclusive — should proceed to the
	// run lifecycle (and presumably 500 once it tries to dial the bogus
	// base_url, but that's past the validation we care about). We just
	// assert the status is NOT 400-mutually-exclusive.
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "mutually exclusive") {
		t.Errorf("explicit null should not trigger mutual-exclusion 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateRun_RejectsInlinePersonasNotArray(t *testing.T) {
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML())
	srv := mustNewServer(t, Config{ConfigsDir: configsDir})

	// Object instead of array.
	body := `{"config_name":"lochness","personas":{"name":"not-an-array"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "JSON array") {
		t.Errorf("body should mention JSON array, got %s", w.Body.String())
	}
}

func TestCreateRun_RejectsMalformedConfig(t *testing.T) {
	configsDir := t.TempDir()
	// LoadAppConfig parses YAML; non-YAML should fail config-load with 400.
	mustWrite(t, filepath.Join(configsDir, "broken.yaml"), "this is not: { valid yaml\n  ::\n")
	personasDir := makePersonaCollection(t, "okay", []string{"x.yaml"})
	srv := mustNewServer(t, Config{ConfigsDir: configsDir, PersonasDir: personasDir})

	body := `{"config_name":"broken","persona_set":"okay"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "loading config") {
		t.Errorf("body should mention config load failure, got %s", w.Body.String())
	}
}

// makePersonaCollection creates a fresh personas directory containing one
// collection subdirectory with empty .yaml files inside, and returns the
// personas directory path.
func makePersonaCollection(t *testing.T, name string, files []string) string {
	t.Helper()
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, name))
	for _, f := range files {
		mustWrite(t, filepath.Join(dir, name, f), "name: placeholder\n")
	}
	return dir
}

// validAppConfigYAML returns minimal valid YAML for LoadAppConfig — enough
// to pass parse, not enough to actually run a real run (no driver wiring).
// Used in tests that exercise validation paths past the config-load step.
func validAppConfigYAML() string {
	return `name: ghost-watch
base_url: http://localhost:9999/graphql
auth:
  type: graphql
  query: |
    mutation { login { token } }
  token_path: data.login.token
tools_from_schema: false
tools: []
`
}

func TestCreateRun_RejectsUnknownModelSpec(t *testing.T) {
	// An unknown <provider> prefix should land as a clean 400 before
	// the SSE stream opens — clients shouldn't get a long-poll
	// connection only to read a mid-stream run_error.
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML())
	personasDir := makePersonaCollection(t, "okay", []string{"x.yaml"})
	srv := mustNewServer(t, Config{ConfigsDir: configsDir, PersonasDir: personasDir})

	body := `{"config_name":"lochness","persona_set":"okay","model":"definitely-not-real:gpt-9000"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "definitely-not-real:gpt-9000") {
		t.Errorf("body should name the bad spec, got %s", w.Body.String())
	}
	// And it should hint at what's actually registered, so the client
	// can correct without round-tripping through docs.
	if !strings.Contains(w.Body.String(), "registered:") {
		t.Errorf("body should list registered prefixes, got %s", w.Body.String())
	}
}

func TestCreateRun_RequestModelOverridesAppConfigDefault(t *testing.T) {
	configsDir := t.TempDir()
	// App config sets a default_model that the request body should override.
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML()+"default_model: claude:sonnet-4-5\n")
	personasDir := t.TempDir()
	mustMkdir(t, filepath.Join(personasDir, "monsterhunters"))
	mustWrite(t, filepath.Join(personasDir, "monsterhunters", "ghost-hunter.yaml"), `name: Ghost Hunter
description: Investigates haunted forums.
goals:
  - Look around
behavior: lurker
`)

	prov := &stubProvider{
		responses: []*provider.Response{
			{Message: provider.Message{Role: provider.RoleAssistant, Content: "Bigfoot was here."}, StopReason: "end"},
		},
	}
	factory, seen := recordingFactory(prov)

	srv := mustNewServer(t, Config{
		ConfigsDir:      configsDir,
		PersonasDir:     personasDir,
		ProviderFactory: factory,
		DriverFactory: func(_ *config.AppConfig, _ *slog.Logger) driver.Driver {
			return newStubDriver()
		},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"config_name":"lochness","persona_set":"monsterhunters","model":"openai:gpt-4o"}`
	resp, err := http.Post(ts.URL+"/v1/runs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain SSE

	if seen() != "openai:gpt-4o" {
		t.Errorf("factory got spec %q, want %q (request body should win over app-config default)", seen(), "openai:gpt-4o")
	}
}

func TestCreateRun_AppConfigDefaultModelUsedWhenRequestOmits(t *testing.T) {
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML()+"default_model: openai:gpt-4o-mini\n")
	personasDir := t.TempDir()
	mustMkdir(t, filepath.Join(personasDir, "monsterhunters"))
	mustWrite(t, filepath.Join(personasDir, "monsterhunters", "yeti-tracker.yaml"), `name: Yeti Tracker
description: Looks for big footprints.
goals:
  - Look around
behavior: lurker
`)

	prov := &stubProvider{
		responses: []*provider.Response{
			{Message: provider.Message{Role: provider.RoleAssistant, Content: "Done."}, StopReason: "end"},
		},
	}
	factory, seen := recordingFactory(prov)

	srv := mustNewServer(t, Config{
		ConfigsDir:      configsDir,
		PersonasDir:     personasDir,
		ProviderFactory: factory,
		DriverFactory: func(_ *config.AppConfig, _ *slog.Logger) driver.Driver {
			return newStubDriver()
		},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// No "model" field in the body — should fall back to app config's default_model.
	body := `{"config_name":"lochness","persona_set":"monsterhunters"}`
	resp, err := http.Post(ts.URL+"/v1/runs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if seen() != "openai:gpt-4o-mini" {
		t.Errorf("factory got spec %q, want %q (app-config default_model should apply when request omits model)", seen(), "openai:gpt-4o-mini")
	}
}

func TestCreateRun_BudgetPerAgentTriggersExhaustedStop(t *testing.T) {
	// End-to-end check that POST /v1/runs threads budget_per_agent_usd
	// from the request body all the way down to the agent loop. Sized
	// so the first canned response alone blows past the budget.
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML())
	personasDir := t.TempDir()
	mustMkdir(t, filepath.Join(personasDir, "monsterhunters"))
	mustWrite(t, filepath.Join(personasDir, "monsterhunters", "wendigo-watcher.yaml"), `name: Wendigo Watcher
description: Looks for big footprints in the snow.
goals:
  - Look around
behavior: lurker
`)

	prov := &stubProvider{
		responses: []*provider.Response{
			{
				Message: provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{ID: "call_overspend", Name: "browse_blocs", Arguments: "{}"},
					},
				},
				StopReason: "tool_use",
				Usage: provider.Usage{
					InputTokens:  1_000_000,
					OutputTokens: 1_000_000,
					ModelID:      "claude:sonnet-4-5",
				},
			},
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Wrapping up. The Wendigo eluded me."},
				StopReason: "end",
				Usage:      provider.Usage{ModelID: "claude:sonnet-4-5"},
			},
		},
	}

	srv := mustNewServer(t, Config{
		ConfigsDir:      configsDir,
		PersonasDir:     personasDir,
		Cost:            cost.LoadDefaults(),
		ProviderFactory: func(_ string) (provider.Provider, error) { return prov, nil },
		DriverFactory: func(_ *config.AppConfig, _ *slog.Logger) driver.Driver {
			return newStubDriver()
		},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"config_name":"lochness","persona_set":"monsterhunters","budget_per_agent_usd":0.01}`
	resp, err := http.Post(ts.URL+"/v1/runs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	stream, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	if !strings.Contains(string(stream), `"stop_reason":"budget_exhausted"`) {
		t.Errorf("expected SSE stream to carry stop_reason=budget_exhausted, got:\n%s", stream)
	}
}

func TestCreateRun_MaxRunCostTriggersAggregateExhaustion(t *testing.T) {
	// Two personas, each with a tool-call-then-text response pattern.
	// First persona's first turn alone should blow the $0.05 aggregate
	// budget. Both personas should end up with stop_reason=budget_exhausted
	// (the second one is queued before the cap is hit, but its OnUsage
	// returns true on its first turn). The run_end event carries a
	// RunSummary with non-zero stopped_on_budget.
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML())
	personasDir := t.TempDir()
	mustMkdir(t, filepath.Join(personasDir, "monsterhunters"))
	for _, name := range []string{"hunter-a.yaml", "hunter-b.yaml"} {
		mustWrite(t, filepath.Join(personasDir, "monsterhunters", name), `name: `+strings.TrimSuffix(name, ".yaml")+`
description: Looks for things in the woods.
goals:
  - Look around
behavior: lurker
`)
	}

	// Each persona burns through its own canned-response queue. Make
	// each first turn expensive (1M+1M tokens against claude:sonnet-4-5
	// = $18) so the very first OnUsage call from each agent crosses
	// the $0.05 cap.
	expensive := func() *provider.Response {
		return &provider.Response{
			Message: provider.Message{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{
					{ID: "call_burn", Name: "browse_blocs", Arguments: "{}"},
				},
			},
			StopReason: "tool_use",
			Usage: provider.Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
				ModelID:      "claude:sonnet-4-5",
			},
		}
	}
	wrapUp := func() *provider.Response {
		return &provider.Response{
			Message:    provider.Message{Role: provider.RoleAssistant, Content: "Wrapping up."},
			StopReason: "end",
			Usage:      provider.Usage{ModelID: "claude:sonnet-4-5"},
		}
	}
	prov := &stubProvider{
		responses: []*provider.Response{expensive(), wrapUp(), expensive(), wrapUp()},
	}

	srv := mustNewServer(t, Config{
		ConfigsDir:      configsDir,
		PersonasDir:     personasDir,
		Cost:            cost.LoadDefaults(),
		ProviderFactory: func(_ string) (provider.Provider, error) { return prov, nil },
		DriverFactory: func(_ *config.AppConfig, _ *slog.Logger) driver.Driver {
			return newStubDriver()
		},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"config_name":"lochness","persona_set":"monsterhunters","max_run_cost_usd":0.05}`
	resp, err := http.Post(ts.URL+"/v1/runs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	stream, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	streamStr := string(stream)

	// At least one agent should have exited on budget.
	if !strings.Contains(streamStr, `"stop_reason":"budget_exhausted"`) {
		t.Errorf("expected SSE to contain stop_reason=budget_exhausted, got:\n%s", streamStr)
	}
	// run_end payload must carry the summary.
	if !strings.Contains(streamStr, `"kind":"run_end"`) {
		t.Errorf("expected run_end event in SSE, got:\n%s", streamStr)
	}
	if !strings.Contains(streamStr, `"stopped_on_budget"`) {
		t.Errorf("run_end payload should include stopped_on_budget, got:\n%s", streamStr)
	}
}

func TestCreateRun_BuiltinDefaultUsedWhenNothingSet(t *testing.T) {
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML())
	personasDir := t.TempDir()
	mustMkdir(t, filepath.Join(personasDir, "monsterhunters"))
	mustWrite(t, filepath.Join(personasDir, "monsterhunters", "moth-watcher.yaml"), `name: Moth Watcher
description: Awaits Mothman.
goals:
  - Look around
behavior: lurker
`)

	prov := &stubProvider{
		responses: []*provider.Response{
			{Message: provider.Message{Role: provider.RoleAssistant, Content: "Mothman not seen."}, StopReason: "end"},
		},
	}
	factory, seen := recordingFactory(prov)

	srv := mustNewServer(t, Config{
		ConfigsDir:      configsDir,
		PersonasDir:     personasDir,
		ProviderFactory: factory,
		DriverFactory: func(_ *config.AppConfig, _ *slog.Logger) driver.Driver {
			return newStubDriver()
		},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"config_name":"lochness","persona_set":"monsterhunters"}`
	resp, err := http.Post(ts.URL+"/v1/runs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if seen() != provider.DefaultModelSpec {
		t.Errorf("factory got spec %q, want %q (built-in default should apply when no other source is set)", seen(), provider.DefaultModelSpec)
	}
}

func TestCreateRun_EndToEndSSEStream(t *testing.T) {
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "lochness.yaml"), validAppConfigYAML())

	personasDir := t.TempDir()
	mustMkdir(t, filepath.Join(personasDir, "monsterhunters"))
	mustWrite(t, filepath.Join(personasDir, "monsterhunters", "nessie-watcher.yaml"), `name: Nessie Watcher
description: Loch-side cryptozoologist looking for groups to join.
goals:
  - Find a bloc to join
behavior: lurker
`)

	prov := &stubProvider{
		responses: []*provider.Response{
			{
				Message: provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{ID: "toolu_loch", Name: "browse_blocs", Arguments: "{}"},
					},
				},
				StopReason: "tool_use",
				Usage:      provider.Usage{InputTokens: 50, OutputTokens: 12},
			},
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Found the Loch Ness Network. Done."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 60, OutputTokens: 8},
			},
		},
	}

	srv := mustNewServer(t, Config{
		ConfigsDir:      configsDir,
		PersonasDir:     personasDir,
		ProviderFactory: func(_ string) (provider.Provider, error) { return prov, nil },
		DriverFactory: func(_ *config.AppConfig, _ *slog.Logger) driver.Driver {
			return newStubDriver()
		},
	})

	// httptest.ResponseRecorder doesn't implement http.Flusher; spin up a
	// real test server so the SSE handler can flush.
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"config_name":"lochness","persona_set":"monsterhunters"}`
	resp, err := http.Post(ts.URL+"/v1/runs", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: want 200, got %d (body: %s)", resp.StatusCode, bodyBytes)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: want text/event-stream, got %q", ct)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	events := parseSSEEvents(t, bodyBytes)
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = string(e.Event.Kind)
	}

	// Expected order: run_start, then per-agent (session_start, step×N,
	// session_end), then run_end. Agent count = 1 here.
	if len(events) < 5 {
		t.Fatalf("expected at least 5 events (run_start, session_start, step, session_end, run_end), got %d: %v", len(events), kinds)
	}
	if kinds[0] != "run_start" {
		t.Errorf("kinds[0]: want run_start, got %q (full: %v)", kinds[0], kinds)
	}
	if kinds[len(kinds)-1] != "run_end" {
		t.Errorf("last kind: want run_end, got %q (full: %v)", kinds[len(kinds)-1], kinds)
	}

	requireKind(t, kinds, "session_start")
	requireKind(t, kinds, "step")
	requireKind(t, kinds, "session_end")

	// Per-agent events should carry the persona name.
	for _, e := range events {
		switch e.Event.Kind {
		case "run_start", "run_end", "run_error":
			if e.Persona != "" {
				t.Errorf("orchestrator event %s should not have a persona, got %q", e.Event.Kind, e.Persona)
			}
		default:
			if e.Persona != "Nessie Watcher" {
				t.Errorf("agent event %s: want persona=Nessie Watcher, got %q", e.Event.Kind, e.Persona)
			}
		}
	}
}

// recordingFactory wraps a stubProvider and remembers the spec it was
// built for. Lets tests assert which spec the resolution code passed
// through without coupling to the provider package's wire shape.
//
// The factory closure is invoked from the HTTP handler goroutine; the
// returned reader is called from the test goroutine after the response
// closes. The mutex makes that handoff explicit so a future refactor
// that splits the read/write boundary doesn't introduce a silent race.
func recordingFactory(prov provider.Provider) (func(spec string) (provider.Provider, error), func() string) {
	var (
		mu   sync.Mutex
		seen string
	)
	factory := func(spec string) (provider.Provider, error) {
		mu.Lock()
		seen = spec
		mu.Unlock()
		return prov, nil
	}
	read := func() string {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
	return factory, read
}

// stubProvider returns canned responses in sequence; mirrors the pattern
// used in agent_test.go's mockProvider.
type stubProvider struct {
	mu        sync.Mutex
	responses []*provider.Response
	idx       int
}

func (s *stubProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool) (*provider.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx >= len(s.responses) {
		// Default to a closing turn so the agent's loop terminates cleanly.
		return &provider.Response{
			Message:    provider.Message{Role: provider.RoleAssistant, Content: "out of canned responses"},
			StopReason: "end",
		}, nil
	}
	r := s.responses[s.idx]
	s.idx++
	return r, nil
}

// stubDriver advertises a single tool and echoes a canned result for it.
// Doesn't implement Authenticator or Registrar, so the agent skips both.
type stubDriver struct {
	tools []provider.Tool
}

func newStubDriver() *stubDriver {
	return &stubDriver{
		tools: []provider.Tool{
			{Name: "browse_blocs", Description: "List blocs", Parameters: map[string]any{"type": "object"}},
		},
	}
}

func (d *stubDriver) Tools() []provider.Tool { return d.tools }
func (d *stubDriver) Execute(_ context.Context, call provider.ToolCall) (*provider.ToolResult, error) {
	return &provider.ToolResult{
		ToolID:  call.ID,
		Content: `[{"id":"bloc-loch","name":"Loch Ness Network","members":340}]`,
	}, nil
}
func (d *stubDriver) Close() error { return nil }

var _ driver.Driver = (*stubDriver)(nil)

// parseSSEEvents reads an SSE-formatted body (data: <json>\n\n per
// event) and returns the parsed RunEvents.
func parseSSEEvents(t *testing.T, body []byte) []RunEvent {
	t.Helper()
	var events []RunEvent
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var ev RunEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("failed to decode SSE event %q: %v", payload, err)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	return events
}

func requireKind(t *testing.T, kinds []string, want string) {
	t.Helper()
	for _, k := range kinds {
		if k == want {
			return
		}
	}
	t.Errorf("expected kind %q in stream, got %v", want, kinds)
}

func TestCreateRun_EndToEndSSEStream_InlineInputs(t *testing.T) {
	// Same shape as TestCreateRun_EndToEndSSEStream but exercises the
	// inline path: no files on disk, the panel sends config + personas
	// in the request body.
	prov := &stubProvider{
		responses: []*provider.Response{
			{
				Message: provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{ID: "toolu_yowie", Name: "browse_blocs", Arguments: "{}"},
					},
				},
				StopReason: "tool_use",
				Usage:      provider.Usage{InputTokens: 50, OutputTokens: 12},
			},
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Done."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 60, OutputTokens: 8},
			},
		},
	}

	srv := mustNewServer(t, Config{
		ProviderFactory: func(_ string) (provider.Provider, error) { return prov, nil },
		DriverFactory: func(_ *config.AppConfig, _ *slog.Logger) driver.Driver {
			return newStubDriver()
		},
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{
		"config": {
			"name": "Yowie Watch",
			"base_url": "http://localhost:9999/graphql",
			"auth": {
				"type": "graphql",
				"query": "mutation { login { token } }",
				"token_path": "data.login.token"
			},
			"tools_from_schema": false,
			"tools": []
		},
		"personas": [
			{
				"name": "Outback Watcher",
				"description": "Bush-camping cryptozoologist hunting yowie tracks.",
				"goals": ["Find a bloc to join"],
				"behavior": "lurker"
			}
		]
	}`

	resp, err := http.Post(ts.URL+"/v1/runs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: want 200, got %d (body: %s)", resp.StatusCode, bodyBytes)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	events := parseSSEEvents(t, bodyBytes)
	if len(events) < 5 {
		t.Fatalf("expected at least 5 events, got %d", len(events))
	}
	if string(events[0].Event.Kind) != "run_start" {
		t.Errorf("first event should be run_start, got %q", events[0].Event.Kind)
	}
	if string(events[len(events)-1].Event.Kind) != "run_end" {
		t.Errorf("last event should be run_end, got %q", events[len(events)-1].Event.Kind)
	}

	// run_start payload should reflect the inline source.
	startPayload, _ := events[0].Event.Payload.(map[string]any)
	sources, _ := startPayload["sources"].(map[string]any)
	if sources["config"] != "inline" || sources["personas"] != "inline" {
		t.Errorf("run_start sources: want both inline, got %+v", sources)
	}

	// Per-agent events carry the inline persona's name.
	sawAgentEvent := false
	for _, ev := range events {
		if ev.Persona == "Outback Watcher" {
			sawAgentEvent = true
			break
		}
	}
	if !sawAgentEvent {
		t.Errorf("expected per-agent events tagged with the inline persona's name")
	}
}

func TestCreateRun_EndToEndSSEStream_MixedInlineAndNamed(t *testing.T) {
	// Inline config + named persona set. Tests that the resolution
	// helpers handle a mix without surprises.
	personasDir := t.TempDir()
	mustMkdir(t, filepath.Join(personasDir, "monsterhunters"))
	mustWrite(t, filepath.Join(personasDir, "monsterhunters", "champ.yaml"), `name: Champ Spotter
description: Lake Champlain regular.
goals:
  - Find a bloc to join
behavior: lurker
`)

	prov := &stubProvider{
		responses: []*provider.Response{
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Nothing to do."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 20, OutputTokens: 5},
			},
		},
	}

	srv := mustNewServer(t, Config{
		PersonasDir:     personasDir,
		ProviderFactory: func(_ string) (provider.Provider, error) { return prov, nil },
		DriverFactory: func(_ *config.AppConfig, _ *slog.Logger) driver.Driver {
			return newStubDriver()
		},
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{
		"config": {
			"name": "Inline Cryptid Network",
			"base_url": "http://localhost:9999/graphql",
			"auth": {"type":"graphql","query":"mutation { login { token } }","token_path":"data.login.token"},
			"tools_from_schema": false,
			"tools": []
		},
		"persona_set": "monsterhunters"
	}`
	resp, err := http.Post(ts.URL+"/v1/runs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: want 200, got %d (body: %s)", resp.StatusCode, bodyBytes)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	events := parseSSEEvents(t, bodyBytes)
	startPayload, _ := events[0].Event.Payload.(map[string]any)
	sources, _ := startPayload["sources"].(map[string]any)
	if sources["config"] != "inline" || sources["personas"] != "name" {
		t.Errorf("run_start sources: want config=inline + personas=name, got %+v", sources)
	}
}

func TestCreateRun_EndToEndSSEStream_NamedConfigInlinePersonas(t *testing.T) {
	// Symmetric counterpart to MixedInlineAndNamed: named config from
	// disk + inline personas in the request body. Verifies the
	// resolution helpers handle either direction of mix.
	configsDir := t.TempDir()
	mustWrite(t, filepath.Join(configsDir, "swamp-watch.yaml"), validAppConfigYAML())

	prov := &stubProvider{
		responses: []*provider.Response{
			{
				Message:    provider.Message{Role: provider.RoleAssistant, Content: "Nothing to do."},
				StopReason: "end",
				Usage:      provider.Usage{InputTokens: 20, OutputTokens: 5},
			},
		},
	}

	srv := mustNewServer(t, Config{
		ConfigsDir:      configsDir,
		ProviderFactory: func(_ string) (provider.Provider, error) { return prov, nil },
		DriverFactory: func(_ *config.AppConfig, _ *slog.Logger) driver.Driver {
			return newStubDriver()
		},
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{
		"config_name": "swamp-watch",
		"personas": [
			{
				"name": "Bayou Lurker",
				"description": "Cajun country swamp explorer cataloguing cryptid rumors.",
				"goals": ["Find a bloc to join"],
				"behavior": "lurker"
			}
		]
	}`
	resp, err := http.Post(ts.URL+"/v1/runs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: want 200, got %d (body: %s)", resp.StatusCode, bodyBytes)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	events := parseSSEEvents(t, bodyBytes)
	startPayload, _ := events[0].Event.Payload.(map[string]any)
	sources, _ := startPayload["sources"].(map[string]any)
	if sources["config"] != "name" || sources["personas"] != "inline" {
		t.Errorf("run_start sources: want config=name + personas=inline, got %+v", sources)
	}
}

func TestRunRequest_MaxStepsClamping(t *testing.T) {
	cases := []struct {
		name string
		req  RunRequest
		want int
	}{
		{"unset uses default", RunRequest{}, 50},
		{"zero uses default", RunRequest{MaxSteps: 0}, 50},
		{"negative uses default", RunRequest{MaxSteps: -7}, 50},
		{"in-range passes through", RunRequest{MaxSteps: 75}, 75},
		{"at ceiling passes through", RunRequest{MaxSteps: 200}, 200},
		{"over ceiling clamps", RunRequest{MaxSteps: 9999}, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.maxSteps(); got != tc.want {
				t.Errorf("maxSteps: want %d, got %d", tc.want, got)
			}
		})
	}
}
