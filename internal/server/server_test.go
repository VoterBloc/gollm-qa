package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHealth_ReturnsOK(t *testing.T) {
	srv := New(Config{}, nil)
	w := do(t, srv, http.MethodGet, "/health")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var got map[string]string
	mustDecode(t, w.Body, &got)
	if got["status"] != "ok" {
		t.Errorf("status field: want %q, got %q", "ok", got["status"])
	}
}

func TestListConfigs_ReturnsSortedYAMLFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "jersey-devil-network.yaml"), "name: jersey-devil")
	mustWrite(t, filepath.Join(dir, "bigfoot-appreciation-society.yml"), "name: bigfoot")
	mustWrite(t, filepath.Join(dir, "README.md"), "ignored")
	mustMkdir(t, filepath.Join(dir, "ignored-subdir"))

	srv := New(Config{ConfigsDir: dir}, nil)
	w := do(t, srv, http.MethodGet, "/v1/configs")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", w.Code, w.Body.String())
	}

	var got struct {
		Items []FileEntry `json:"items"`
	}
	mustDecode(t, w.Body, &got)

	if len(got.Items) != 2 {
		t.Fatalf("items: want 2, got %d (%+v)", len(got.Items), got.Items)
	}
	// Sorted alphabetically by stem: bigfoot... < jersey-devil...
	if got.Items[0].Name != "bigfoot-appreciation-society" {
		t.Errorf("items[0].name: want %q, got %q", "bigfoot-appreciation-society", got.Items[0].Name)
	}
	if got.Items[0].File != "bigfoot-appreciation-society.yml" {
		t.Errorf("items[0].file: want %q, got %q", "bigfoot-appreciation-society.yml", got.Items[0].File)
	}
	if got.Items[1].Name != "jersey-devil-network" {
		t.Errorf("items[1].name: want %q, got %q", "jersey-devil-network", got.Items[1].Name)
	}
}

func TestListConfigs_MissingDirIsEmpty(t *testing.T) {
	srv := New(Config{ConfigsDir: "/nonexistent/path/to/cryptid-archive"}, nil)
	w := do(t, srv, http.MethodGet, "/v1/configs")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 (empty list), got %d", w.Code)
	}
	var got struct {
		Items []FileEntry `json:"items"`
	}
	mustDecode(t, w.Body, &got)
	if len(got.Items) != 0 {
		t.Errorf("items: want empty, got %v", got.Items)
	}
}

func TestListCampaigns_ReturnsYAMLFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "lure-the-mothman.yaml"), "name: mothman")

	srv := New(Config{CampaignsDir: dir}, nil)
	w := do(t, srv, http.MethodGet, "/v1/campaigns")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var got struct {
		Items []FileEntry `json:"items"`
	}
	mustDecode(t, w.Body, &got)
	if len(got.Items) != 1 || got.Items[0].Name != "lure-the-mothman" {
		t.Errorf("items: want one mothman lure, got %+v", got.Items)
	}
}

func TestListPersonas_DistinguishesFilesAndCollections(t *testing.T) {
	dir := t.TempDir()

	// Loose persona file at the top level.
	mustWrite(t, filepath.Join(dir, "ogopogo-skeptic.yaml"), "name: ogopogo-skeptic")

	// Collection: a subdirectory of personas, two .yaml inside, one ignored .md.
	mustMkdir(t, filepath.Join(dir, "lake-cryptid-believers"))
	mustWrite(t, filepath.Join(dir, "lake-cryptid-believers", "champ.yaml"), "name: champ")
	mustWrite(t, filepath.Join(dir, "lake-cryptid-believers", "nessie.yml"), "name: nessie")
	mustWrite(t, filepath.Join(dir, "lake-cryptid-believers", "README.md"), "ignored")

	// Empty collection should report count=0.
	mustMkdir(t, filepath.Join(dir, "abandoned-coven"))

	srv := New(Config{PersonasDir: dir}, nil)
	w := do(t, srv, http.MethodGet, "/v1/personas")

	var got struct {
		Items []PersonaEntry `json:"items"`
	}
	mustDecode(t, w.Body, &got)

	if len(got.Items) != 3 {
		t.Fatalf("items: want 3 (one file + two collections), got %d (%+v)", len(got.Items), got.Items)
	}

	// Sorted by name: abandoned-coven, lake-cryptid-believers, ogopogo-skeptic.
	if got.Items[0].Name != "abandoned-coven" || got.Items[0].Kind != "collection" || got.Items[0].Count != 0 {
		t.Errorf("items[0]: want abandoned-coven collection count=0, got %+v", got.Items[0])
	}
	if got.Items[1].Name != "lake-cryptid-believers" || got.Items[1].Kind != "collection" || got.Items[1].Count != 2 {
		t.Errorf("items[1]: want lake-cryptid-believers collection count=2, got %+v", got.Items[1])
	}
	if got.Items[2].Name != "ogopogo-skeptic" || got.Items[2].Kind != "file" || got.Items[2].File != "ogopogo-skeptic.yaml" {
		t.Errorf("items[2]: want ogopogo-skeptic file, got %+v", got.Items[2])
	}
}

func TestOpenAPISpec_IsValidJSONAndAdvertisesPaths(t *testing.T) {
	srv := New(Config{}, nil)
	w := do(t, srv, http.MethodGet, "/openapi.json")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: want application/json, got %q", ct)
	}
	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if got["openapi"] == nil {
		t.Errorf("spec missing the 'openapi' field")
	}
	paths, ok := got["paths"].(map[string]any)
	if !ok {
		t.Fatalf("spec missing the 'paths' object")
	}
	for _, want := range []string{"/health", "/v1/configs", "/v1/campaigns", "/v1/personas"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("spec missing path %q", want)
		}
	}
}

// --- helpers ---

func do(t *testing.T, srv *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustDecode(t *testing.T, r io.Reader, v any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
