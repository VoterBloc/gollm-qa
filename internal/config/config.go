// Package config handles loading app-specific configuration and persona
// definitions. The tool is app-agnostic — all target app details live in
// config files.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/VoterBloc/gollm-qa/internal/agent"
	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// AppConfig defines a target application and the tools agents can use.
type AppConfig struct {
	Name    string       `yaml:"name"`
	BaseURL string       `yaml:"base_url"`
	Auth    AuthConfig   `yaml:"auth"`
	Tools   []ToolConfig `yaml:"tools"`
	Admin   AdminConfig  `yaml:"admin,omitempty"`
}

// AdminConfig describes admin-only operations (data purge, etc.). Reuses
// AppConfig.Auth's login mutation — admin and regular users share the same
// login surface in the apps we currently target.
type AdminConfig struct {
	// IdentifierEnv is the env var name to read the admin identifier from.
	IdentifierEnv string `yaml:"identifier_env"`
	// PasswordEnv is the env var name to read the admin password from.
	PasswordEnv string `yaml:"password_env"`
	// PurgeQuery is the GraphQL mutation that wipes synthetic data.
	PurgeQuery string `yaml:"purge_query"`
	// PurgeResultPath is a gjson path to the purge report inside the response.
	PurgeResultPath string `yaml:"purge_result_path"`
}

// AuthConfig defines how agents authenticate with the target app.
type AuthConfig struct {
	// Type is the auth mechanism — currently only "graphql" is supported.
	Type string `yaml:"type"`
	// Query is the GraphQL mutation (or REST endpoint) for login.
	Query string `yaml:"query"`
	// TokenPath is a gjson path to extract the token from the response.
	TokenPath string `yaml:"token_path"`
	// RegisterQuery is the GraphQL mutation used to create a new user (e.g.
	// registerForTest). Optional — only needed for run modes that seed users.
	RegisterQuery string `yaml:"register_query"`
	// RegisterTokenPath is a gjson path to extract the token from the register
	// response. If empty, register is treated as create-only and the caller is
	// expected to follow up with Login.
	RegisterTokenPath string `yaml:"register_token_path"`
	// HeaderName is the HTTP header for the auth token (default "Authorization").
	HeaderName string `yaml:"header_name"`
	// HeaderValue is the header value template. {{token}} is replaced with the actual token.
	HeaderValue string `yaml:"header_value"`
}

// ToolConfig maps an LLM-visible tool to a GraphQL operation.
type ToolConfig struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description"`
	Query       string        `yaml:"query"`
	Parameters  []ParamConfig `yaml:"parameters"`
	ResultPath  string        `yaml:"result_path"`
	// Context is free-form text appended to the tool result before the LLM sees it.
	// Use it to provide affordance hints: what the agent can do next given what it just saw.
	Context string `yaml:"context"`
}

// ParamConfig defines a single parameter for a tool.
type ParamConfig struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"` // "string", "integer", "boolean"
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
}

// LoadAppConfig reads and parses an app config YAML file.
func LoadAppConfig(path string) (*AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading app config: %w", err)
	}

	var cfg AppConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing app config: %w", err)
	}

	// Apply defaults.
	if cfg.Auth.HeaderName == "" {
		cfg.Auth.HeaderName = "Authorization"
	}
	if cfg.Auth.HeaderValue == "" {
		cfg.Auth.HeaderValue = "Bearer {{token}}"
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (cfg *AppConfig) validate() error {
	if cfg.Name == "" {
		return fmt.Errorf("app config: name is required")
	}
	if cfg.BaseURL == "" {
		return fmt.Errorf("app config: base_url is required")
	}
	if cfg.Auth.Type != "" && cfg.Auth.Query == "" {
		return fmt.Errorf("app config: auth.query is required when auth.type is set")
	}
	if cfg.Auth.Type != "" && cfg.Auth.TokenPath == "" {
		return fmt.Errorf("app config: auth.token_path is required when auth.type is set")
	}
	for i, t := range cfg.Tools {
		if t.Name == "" {
			return fmt.Errorf("app config: tools[%d].name is required", i)
		}
		if t.Query == "" {
			return fmt.Errorf("app config: tools[%d].query is required", i)
		}
	}
	return nil
}

// LoadPersonas reads all YAML files from a directory and returns personas.
func LoadPersonas(dir string) ([]*agent.Persona, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading persona directory: %w", err)
	}

	// Sort for deterministic ordering across filesystems.
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	var personas []*agent.Persona
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading persona %s: %w", entry.Name(), err)
		}

		var p agent.Persona
		if err := yaml.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("parsing persona %s: %w", entry.Name(), err)
		}

		personas = append(personas, &p)
	}

	return personas, nil
}

// ToProviderTool converts a ToolConfig to the provider.Tool format the LLM sees.
func (tc *ToolConfig) ToProviderTool() provider.Tool {
	properties := make(map[string]any)
	var required []string

	for _, p := range tc.Parameters {
		prop := map[string]any{
			"type":        mapParamType(p.Type),
			"description": p.Description,
		}
		properties[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}

	params := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		params["required"] = required
	}

	return provider.Tool{
		Name:        tc.Name,
		Description: tc.Description,
		Parameters:  params,
	}
}

func mapParamType(t string) string {
	switch strings.ToLower(t) {
	case "string":
		return "string"
	case "integer", "int":
		return "integer"
	case "boolean", "bool":
		return "boolean"
	case "number", "float":
		return "number"
	default:
		return "string"
	}
}
