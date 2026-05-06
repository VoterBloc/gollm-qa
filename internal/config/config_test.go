package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VoterBloc/gollm-qa/internal/agent"
)

const testAppConfig = `
name: Sasquatch Tracker
base_url: https://api.sasquatch.app/graphql

auth:
  type: graphql
  query: |
    mutation Login($identifier: String!, $password: String!) {
      login(identifier: $identifier, password: $password) { token }
    }
  token_path: "data.login.token"

tools:
  - name: browse_sightings
    description: "Browse recent Bigfoot sightings in your area."
    query: |
      query Sightings($state: String, $limit: Int) {
        sightings(state: $state, limit: $limit) {
          items { id location description credibility }
        }
      }
    parameters:
      - name: state
        type: string
        description: "Two-letter state code"
      - name: limit
        type: integer
        description: "Max results"
    result_path: "data.sightings"
    context: |
      You just browsed sightings. To report your own, use report_sighting.

  - name: report_sighting
    description: "Report a Bigfoot sighting."
    query: |
      mutation ReportSighting($location: String!, $description: String!) {
        reportSighting(location: $location, description: $description) { id }
      }
    parameters:
      - name: location
        type: string
        description: "Where you saw Bigfoot"
        required: true
      - name: description
        type: string
        description: "What happened"
        required: true
    result_path: "data.reportSighting"
`

func TestLoadAppConfig(t *testing.T) {
	path := writeTempFile(t, "app.yaml", testAppConfig)

	cfg, err := LoadAppConfig(path)
	if err != nil {
		t.Fatalf("LoadAppConfig() error: %v", err)
	}

	if cfg.Name != "Sasquatch Tracker" {
		t.Errorf("expected name 'Sasquatch Tracker', got %q", cfg.Name)
	}
	if cfg.BaseURL != "https://api.sasquatch.app/graphql" {
		t.Errorf("unexpected base URL: %q", cfg.BaseURL)
	}
	if cfg.Auth.Type != "graphql" {
		t.Errorf("expected auth type 'graphql', got %q", cfg.Auth.Type)
	}
	if cfg.Auth.TokenPath != "data.login.token" {
		t.Errorf("unexpected token path: %q", cfg.Auth.TokenPath)
	}
}

func TestLoadAppConfig_RegisterFields(t *testing.T) {
	yaml := `
name: Bigfoot Appreciation Society
base_url: https://api.squatch.example/graphql
auth:
  type: graphql
  query: |
    mutation Login($identifier: String!, $password: String!) {
      login(identifier: $identifier, password: $password) { token }
    }
  token_path: "data.login.token"
  register_query: |
    mutation RegisterForTest($input: RegisterInput!) {
      registerForTest(input: $input) { token }
    }
  register_token_path: "data.registerForTest.token"
tools: []
`
	path := writeTempFile(t, "register.yaml", yaml)

	cfg, err := LoadAppConfig(path)
	if err != nil {
		t.Fatalf("LoadAppConfig() error: %v", err)
	}

	if cfg.Auth.RegisterQuery == "" {
		t.Error("expected register_query to be populated")
	}
	if cfg.Auth.RegisterTokenPath != "data.registerForTest.token" {
		t.Errorf("unexpected register_token_path: %q", cfg.Auth.RegisterTokenPath)
	}
}

func TestLoadAppConfig_AppliesDefaults(t *testing.T) {
	yaml := `
name: Minimal App
base_url: https://example.com
auth:
  type: graphql
  query: "mutation { login { token } }"
  token_path: "data.login.token"
tools: []
`
	path := writeTempFile(t, "minimal.yaml", yaml)

	cfg, err := LoadAppConfig(path)
	if err != nil {
		t.Fatalf("LoadAppConfig() error: %v", err)
	}

	if cfg.Auth.HeaderName != "Authorization" {
		t.Errorf("expected default header name 'Authorization', got %q", cfg.Auth.HeaderName)
	}
	if cfg.Auth.HeaderValue != "Bearer {{token}}" {
		t.Errorf("expected default header value 'Bearer {{token}}', got %q", cfg.Auth.HeaderValue)
	}
}

func TestLoadAppConfig_ToolsParsed(t *testing.T) {
	path := writeTempFile(t, "app.yaml", testAppConfig)

	cfg, err := LoadAppConfig(path)
	if err != nil {
		t.Fatalf("LoadAppConfig() error: %v", err)
	}

	if len(cfg.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(cfg.Tools))
	}

	browse := cfg.Tools[0]
	if browse.Name != "browse_sightings" {
		t.Errorf("expected tool name 'browse_sightings', got %q", browse.Name)
	}
	if len(browse.Parameters) != 2 {
		t.Errorf("expected 2 parameters, got %d", len(browse.Parameters))
	}
	if browse.Context == "" {
		t.Error("expected context to be set")
	}

	report := cfg.Tools[1]
	if report.Name != "report_sighting" {
		t.Errorf("expected tool name 'report_sighting', got %q", report.Name)
	}
	requiredCount := 0
	for _, p := range report.Parameters {
		if p.Required {
			requiredCount++
		}
	}
	if requiredCount != 2 {
		t.Errorf("expected 2 required params, got %d", requiredCount)
	}
}

func TestToProviderTool(t *testing.T) {
	tc := ToolConfig{
		Name:        "summon_golem",
		Description: "Bring a clay creature to life.",
		Parameters: []ParamConfig{
			{Name: "clay_type", Type: "string", Description: "Type of clay", Required: true},
			{Name: "height_inches", Type: "integer", Description: "How tall"},
			{Name: "is_friendly", Type: "boolean", Description: "Will it be nice"},
		},
	}

	tool := tc.ToProviderTool()

	if tool.Name != "summon_golem" {
		t.Errorf("expected name 'summon_golem', got %q", tool.Name)
	}
	if tool.Description != "Bring a clay creature to life." {
		t.Errorf("unexpected description: %q", tool.Description)
	}

	props, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties to be map[string]any")
	}
	if len(props) != 3 {
		t.Errorf("expected 3 properties, got %d", len(props))
	}

	clayProp, ok := props["clay_type"].(map[string]any)
	if !ok {
		t.Fatal("expected clay_type property")
	}
	if clayProp["type"] != "string" {
		t.Errorf("expected clay_type type 'string', got %q", clayProp["type"])
	}

	heightProp, ok := props["height_inches"].(map[string]any)
	if !ok {
		t.Fatal("expected height_inches property")
	}
	if heightProp["type"] != "integer" {
		t.Errorf("expected height_inches type 'integer', got %q", heightProp["type"])
	}

	required, ok := tool.Parameters["required"].([]string)
	if !ok {
		t.Fatal("expected required to be []string")
	}
	if len(required) != 1 || required[0] != "clay_type" {
		t.Errorf("expected required ['clay_type'], got %v", required)
	}
}

func TestToProviderTool_NoRequiredParams(t *testing.T) {
	tc := ToolConfig{
		Name:        "wander_aimlessly",
		Description: "Just vibe.",
		Parameters: []ParamConfig{
			{Name: "direction", Type: "string", Description: "Which way to wander"},
		},
	}

	tool := tc.ToProviderTool()

	if _, ok := tool.Parameters["required"]; ok {
		t.Error("expected no 'required' field when no params are required")
	}
}

func TestLoadPersonas(t *testing.T) {
	dir := t.TempDir()

	persona1 := `
name: Cornelius McMuffin
description: |
  A retired conspiracy theorist from Roswell who now runs a blog
  about lizard people in local government.
goals:
  - Investigate the city council
  - Post about chemtrails
behavior: engaged
tags:
  state: NM
  tinfoil_hat: "yes"
credentials:
  identifier: cornelius@lizardtruth.net
  password: Tr00thS33k3r!
`
	persona2 := `
name: Brenda Waffleton
description: A quiet librarian who only speaks in Dewey Decimal references.
goals:
  - Find a book club bloc
behavior: lurker
credentials:
  identifier: brenda@library.org
  password: 812.54Rulez
`

	writeTempFileIn(t, dir, "cornelius.yaml", persona1)
	writeTempFileIn(t, dir, "brenda.yml", persona2)
	writeTempFileIn(t, dir, "readme.txt", "not a persona") // should be skipped

	personas, err := LoadPersonas(dir)
	if err != nil {
		t.Fatalf("LoadPersonas() error: %v", err)
	}

	if len(personas) != 2 {
		t.Fatalf("expected 2 personas, got %d", len(personas))
	}

	// Find Cornelius.
	var cornelius *agent.Persona
	for _, p := range personas {
		if p.Name == "Cornelius McMuffin" {
			cornelius = p
			break
		}
	}
	if cornelius == nil {
		t.Fatal("expected to find Cornelius McMuffin")
	}
	if len(cornelius.Goals) != 2 {
		t.Errorf("expected 2 goals, got %d", len(cornelius.Goals))
	}
	if cornelius.Credentials.Identifier != "cornelius@lizardtruth.net" {
		t.Errorf("unexpected identifier: %q", cornelius.Credentials.Identifier)
	}
	if cornelius.Tags["tinfoil_hat"] != "yes" {
		t.Errorf("expected tinfoil_hat tag, got %v", cornelius.Tags)
	}
}

func TestLoadPersonas_SkipsNonYAML(t *testing.T) {
	dir := t.TempDir()
	writeTempFileIn(t, dir, "notes.txt", "these are not personas")
	writeTempFileIn(t, dir, "data.json", `{"not": "a persona"}`)

	personas, err := LoadPersonas(dir)
	if err != nil {
		t.Fatalf("LoadPersonas() error: %v", err)
	}
	if len(personas) != 0 {
		t.Errorf("expected 0 personas, got %d", len(personas))
	}
}

func TestLoadAppConfig_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		errText string
	}{
		{
			name:    "missing name",
			yaml:    `base_url: https://example.com`,
			errText: "name is required",
		},
		{
			name:    "missing base_url",
			yaml:    `name: My App`,
			errText: "base_url is required",
		},
		{
			name: "auth type without query",
			yaml: `
name: My App
base_url: https://example.com
auth:
  type: graphql
  token_path: "data.token"`,
			errText: "auth.query is required",
		},
		{
			name: "tool without name",
			yaml: `
name: My App
base_url: https://example.com
tools:
  - query: "query { things }"`,
			errText: "tools[0].name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, "bad.yaml", tt.yaml)
			_, err := LoadAppConfig(path)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.errText) {
				t.Errorf("expected error containing %q, got: %v", tt.errText, err)
			}
		})
	}
}

// --- helpers ---

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	return writeTempFileIn(t, dir, name, content)
}

func writeTempFileIn(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}
