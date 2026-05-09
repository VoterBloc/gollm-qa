package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateRun_RejectsInvalidJSON(t *testing.T) {
	srv := New(Config{}, nil)
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
	srv := New(Config{}, nil)
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
	srv := New(Config{ConfigsDir: configsDir, PersonasDir: personasDir}, nil)

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
	srv := New(Config{ConfigsDir: configsDir, PersonasDir: personasDir}, nil)

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
	srv := New(Config{ConfigsDir: configsDir, PersonasDir: personasDir}, nil)

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
	srv := New(Config{ConfigsDir: configsDir, PersonasDir: personasDir}, nil)

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

// statusFromRecorder is a tiny convenience for tests that only care
// about the status code.
func statusFromRecorder(w *httptest.ResponseRecorder) int {
	return w.Code
}

var _ = statusFromRecorder // keep helper exported for future tests
