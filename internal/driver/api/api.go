// Package api implements the API-level driver for interacting with target
// applications via GraphQL or REST endpoints.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/VoterBloc/gollm-qa/internal/config"
	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// Driver executes GraphQL operations against a target application.
// One instance per agent — each holds its own auth token.
type Driver struct {
	baseURL     string
	httpClient  *http.Client
	tools       []config.ToolConfig
	toolIndex   map[string]*config.ToolConfig
	authToken   string
	authConfig  config.AuthConfig
	adminConfig config.AdminConfig
	logger      *slog.Logger
}

// New creates an API driver from an app config.
func New(cfg *config.AppConfig, logger *slog.Logger) *Driver {
	if logger == nil {
		logger = slog.Default()
	}

	index := make(map[string]*config.ToolConfig, len(cfg.Tools))
	for i := range cfg.Tools {
		index[cfg.Tools[i].Name] = &cfg.Tools[i]
	}

	return &Driver{
		baseURL:     cfg.BaseURL,
		httpClient:  &http.Client{},
		tools:       cfg.Tools,
		toolIndex:   index,
		authConfig:  cfg.Auth,
		adminConfig: cfg.Admin,
		logger:      logger,
	}
}

// SetAuthToken installs a pre-issued auth token directly, bypassing the
// login round-trip. Used by admin flows (e.g. purge) that authenticate
// with a long-lived token instead of email/password. Caller is responsible
// for ensuring token is non-empty; passing "" silently clears the auth
// state and subsequent requests will go out unauthenticated.
func (d *Driver) SetAuthToken(token string) {
	d.authToken = token
}

// Login authenticates with the target app and stores the token.
func (d *Driver) Login(ctx context.Context, identifier, password string) error {
	variables := map[string]any{
		"identifier": identifier,
		"password":   password,
	}

	body, err := d.doGraphQL(ctx, d.authConfig.Query, variables, false)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}

	// Check for GraphQL errors.
	if errs := gjson.Get(body, "errors"); errs.Exists() {
		return fmt.Errorf("login failed: %s", errs.Raw)
	}

	token := gjson.Get(body, d.authConfig.TokenPath)
	if !token.Exists() {
		return fmt.Errorf("login response missing token at path %q", d.authConfig.TokenPath)
	}

	d.authToken = token.String()
	d.logger.Info("authenticated successfully")
	return nil
}

// Register creates a new user via the configured register mutation (e.g.
// registerForTest in VoterBloc). The mutation receives a single $input
// variable shaped from the supplied input map — apps that follow GraphQL
// input-type conventions can pass any RegisterInput shape this way without
// hardcoding it in Go. If the register response includes a token
// (register_token_path set), it is stored on the driver and the caller does
// not need to follow up with Login.
func (d *Driver) Register(ctx context.Context, input map[string]any) error {
	if d.authConfig.RegisterQuery == "" {
		return fmt.Errorf("register: no register_query configured")
	}

	variables := map[string]any{"input": input}

	body, err := d.doGraphQL(ctx, d.authConfig.RegisterQuery, variables, false)
	if err != nil {
		return fmt.Errorf("register request: %w", err)
	}

	if errs := gjson.Get(body, "errors"); errs.Exists() {
		return fmt.Errorf("register failed: %s", errs.Raw)
	}

	if d.authConfig.RegisterTokenPath != "" {
		token := gjson.Get(body, d.authConfig.RegisterTokenPath)
		if !token.Exists() {
			return fmt.Errorf("register response missing token at path %q", d.authConfig.RegisterTokenPath)
		}
		d.authToken = token.String()
	}

	d.logger.Info("registered successfully")
	return nil
}

// Purge runs the configured admin purge mutation and returns the JSON at
// admin.purge_result_path. The caller is responsible for installing an
// admin token first (via SetAuthToken or Login); Purge just sends the
// mutation with whatever auth token the driver currently holds.
func (d *Driver) Purge(ctx context.Context) (string, error) {
	if d.adminConfig.PurgeQuery == "" {
		return "", fmt.Errorf("purge: no admin.purge_query configured")
	}

	body, err := d.doGraphQL(ctx, d.adminConfig.PurgeQuery, nil, true)
	if err != nil {
		return "", fmt.Errorf("purge request: %w", err)
	}

	if errs := gjson.Get(body, "errors"); errs.Exists() {
		return "", fmt.Errorf("purge failed: %s", errs.Raw)
	}

	if d.adminConfig.PurgeResultPath == "" {
		return body, nil
	}
	result := gjson.Get(body, d.adminConfig.PurgeResultPath)
	if !result.Exists() {
		return "", fmt.Errorf("purge response missing data at path %q", d.adminConfig.PurgeResultPath)
	}
	return result.Raw, nil
}

// Tools returns provider.Tool definitions derived from the tool configs.
func (d *Driver) Tools() []provider.Tool {
	tools := make([]provider.Tool, len(d.tools))
	for i := range d.tools {
		tools[i] = d.tools[i].ToProviderTool()
	}
	return tools
}

// Execute handles a tool call from the LLM.
func (d *Driver) Execute(ctx context.Context, call provider.ToolCall) (*provider.ToolResult, error) {
	tc, ok := d.toolIndex[call.Name]
	if !ok {
		return &provider.ToolResult{
			ToolID:  call.ID,
			Content: fmt.Sprintf("unknown tool: %s", call.Name),
			IsError: true,
		}, nil
	}

	// Parse the LLM's arguments.
	var variables map[string]any
	if call.Arguments != "" && call.Arguments != "{}" {
		if err := json.Unmarshal([]byte(call.Arguments), &variables); err != nil {
			return &provider.ToolResult{
				ToolID:  call.ID,
				Content: fmt.Sprintf("invalid arguments: %v", err),
				IsError: true,
			}, nil
		}
	}

	body, err := d.doGraphQL(ctx, tc.Query, variables, true)
	if err != nil {
		return &provider.ToolResult{
			ToolID:  call.ID,
			Content: fmt.Sprintf("request failed: %v", err),
			IsError: true,
		}, nil
	}

	// Check for GraphQL errors.
	if errs := gjson.Get(body, "errors"); errs.Exists() {
		return &provider.ToolResult{
			ToolID:  call.ID,
			Content: fmt.Sprintf("GraphQL error: %s", errs.Raw),
			IsError: true,
		}, nil
	}

	// Extract the relevant portion of the response.
	var content string
	if tc.ResultPath != "" {
		result := gjson.Get(body, tc.ResultPath)
		if result.Exists() {
			content = result.Raw
		} else {
			content = body
		}
	} else {
		content = body
	}

	// Append context hints if configured.
	if tc.Context != "" {
		content = content + "\n\n---\n" + tc.Context
	}

	return &provider.ToolResult{
		ToolID:  call.ID,
		Content: content,
	}, nil
}

// Close is a no-op for the API driver.
func (d *Driver) Close() error { return nil }

// doGraphQL sends a GraphQL request and returns the raw response body.
func (d *Driver) doGraphQL(ctx context.Context, query string, variables map[string]any, withAuth bool) (string, error) {
	reqBody := map[string]any{
		"query": query,
	}
	if len(variables) > 0 {
		reqBody["variables"] = variables
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", d.baseURL, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if withAuth && d.authToken != "" {
		headerValue := strings.ReplaceAll(d.authConfig.HeaderValue, "{{token}}", d.authToken)
		req.Header.Set(d.authConfig.HeaderName, headerValue)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	const maxResponseSize = 10 << 20 // 10 MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}

	return string(data), nil
}
