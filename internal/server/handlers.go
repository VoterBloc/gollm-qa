package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed openapi.json
var openAPISpec []byte

// registerRoutes mounts all Phase-1 endpoints. New phases should add their
// routes here so the route table stays surveyable in one place.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /openapi.json", s.handleOpenAPI)
	mux.HandleFunc("GET /v1/configs", s.handleListConfigs)
	mux.HandleFunc("GET /v1/campaigns", s.handleListCampaigns)
	mux.HandleFunc("GET /v1/personas", s.handleListPersonas)
	mux.HandleFunc("POST /v1/runs", s.handleCreateRun)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openAPISpec)
}

// FileEntry is a single .yaml file under one of the read-only directories.
// The panel uses Name (stem) for display and File for any subsequent
// fetch-by-path call.
type FileEntry struct {
	Name string `json:"name"`
	File string `json:"file"`
}

func (s *Server) handleListConfigs(w http.ResponseWriter, r *http.Request) {
	entries, err := listYAMLFiles(s.cfg.ConfigsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

func (s *Server) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	entries, err := listYAMLFiles(s.cfg.CampaignsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

// PersonaEntry distinguishes loose persona files from collection
// directories so the panel can render a tree view (collections expand to
// show their members; loose files don't).
type PersonaEntry struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // "file" or "collection"
	File  string `json:"file,omitempty"`
	Count int    `json:"count,omitempty"`
}

func (s *Server) handleListPersonas(w http.ResponseWriter, r *http.Request) {
	entries, err := listPersonaEntries(s.cfg.PersonasDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

// listYAMLFiles returns the .yaml/.yml files (non-recursive) in dir,
// sorted by stem. A missing directory yields an empty list rather than an
// error — the panel should render "no configs" instead of an error page
// when the user hasn't created any yet.
func listYAMLFiles(dir string) ([]FileEntry, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []FileEntry{}, nil
		}
		return nil, fmt.Errorf("reading %q: %w", dir, err)
	}
	out := make([]FileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		if !isYAML(de.Name()) {
			continue
		}
		out = append(out, FileEntry{Name: yamlStem(de.Name()), File: de.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// listPersonaEntries treats subdirectories of dir as collections (the
// `gollm seed` output shape) and top-level .yaml files as loose
// hand-written personas. Mirrors the actual layout produced by both code
// paths.
func listPersonaEntries(dir string) ([]PersonaEntry, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []PersonaEntry{}, nil
		}
		return nil, fmt.Errorf("reading %q: %w", dir, err)
	}
	out := make([]PersonaEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() {
			count, err := countYAMLFiles(filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			out = append(out, PersonaEntry{Name: name, Kind: "collection", Count: count})
			continue
		}
		if !isYAML(name) {
			continue
		}
		out = append(out, PersonaEntry{Name: yamlStem(name), Kind: "file", File: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func countYAMLFiles(dir string) (int, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading %q: %w", dir, err)
	}
	n := 0
	for _, de := range dirEntries {
		if !de.IsDir() && isYAML(de.Name()) {
			n++
		}
	}
	return n, nil
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

func yamlStem(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
