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
		name string
		body string
	}{
		{"empty body", `{}`},
		{"missing config_name", `{"persona_set":"hauntings"}`},
		{"missing persona_set", `{"config_name":"chupacabra-network"}`},
		{"both blank", `{"config_name":"  ","persona_set":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status: want 400, got %d (body: %s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "config_name and persona_set are required") {
				t.Errorf("body should mention missing fields, got %s", w.Body.String())
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
		ProviderFactory: func() provider.Provider { return prov },
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
