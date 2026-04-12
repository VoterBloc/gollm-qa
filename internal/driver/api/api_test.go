package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/VoterBloc/gollm-qa/internal/config"
	"github.com/VoterBloc/gollm-qa/internal/provider"
)

func testAppConfig(url string) *config.AppConfig {
	return &config.AppConfig{
		Name:    "Cryptid Social Network",
		BaseURL: url,
		Auth: config.AuthConfig{
			Type: "graphql",
			Query: `mutation Login($identifier: String!, $password: String!) {
				login(identifier: $identifier, password: $password) { token user { id username } }
			}`,
			TokenPath:   "data.login.token",
			HeaderName:  "Authorization",
			HeaderValue: "Bearer {{token}}",
		},
		Tools: []config.ToolConfig{
			{
				Name:        "browse_cryptids",
				Description: "Browse reported cryptid sightings.",
				Query: `query Cryptids($region: String, $limit: Int) {
					cryptids(region: $region, limit: $limit) {
						items { id name region credibility_score }
						totalCount
					}
				}`,
				Parameters: []config.ParamConfig{
					{Name: "region", Type: "string", Description: "Geographic region"},
					{Name: "limit", Type: "integer", Description: "Max results"},
				},
				ResultPath: "data.cryptids",
				Context:    "You just browsed cryptids. To report a sighting, use report_sighting.",
			},
			{
				Name:        "report_sighting",
				Description: "Report a cryptid sighting.",
				Query: `mutation Report($name: String!, $location: String!) {
					reportSighting(name: $name, location: $location) { id createdAt }
				}`,
				Parameters: []config.ParamConfig{
					{Name: "name", Type: "string", Description: "What you saw", Required: true},
					{Name: "location", Type: "string", Description: "Where", Required: true},
				},
				ResultPath: "data.reportSighting",
			},
		},
	}
}

func TestDriver_Login(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		// Verify it's a login request.
		query, _ := body["query"].(string)
		if !strings.Contains(query, "Login") {
			t.Errorf("expected login query, got: %s", query)
		}

		vars, _ := body["variables"].(map[string]any)
		if vars["identifier"] != "mothman@pointpleasant.wv" {
			t.Errorf("unexpected identifier: %v", vars["identifier"])
		}

		resp := `{"data": {"login": {"token": "jwt-for-mothman-42", "user": {"id": "1", "username": "mothman"}}}}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	drv := New(cfg, nil)

	err := drv.Login(context.Background(), "mothman@pointpleasant.wv", "BridgeCollapse1967!")
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}

	if drv.authToken != "jwt-for-mothman-42" {
		t.Errorf("expected token 'jwt-for-mothman-42', got %q", drv.authToken)
	}
}

func TestDriver_Login_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"errors": [{"message": "Invalid credentials, you imposter"}]}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	drv := New(cfg, nil)

	err := drv.Login(context.Background(), "fake@fake.com", "wrong")
	if err == nil {
		t.Fatal("expected login error")
	}
	if !strings.Contains(err.Error(), "imposter") {
		t.Errorf("expected error to contain GraphQL message, got: %v", err)
	}
}

func TestDriver_Tools(t *testing.T) {
	cfg := testAppConfig("http://localhost")
	drv := New(cfg, nil)

	tools := drv.Tools()

	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "browse_cryptids" {
		t.Errorf("expected first tool 'browse_cryptids', got %q", tools[0].Name)
	}
	if tools[1].Name != "report_sighting" {
		t.Errorf("expected second tool 'report_sighting', got %q", tools[1].Name)
	}
}

func TestDriver_Execute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header is set.
		auth := r.Header.Get("Authorization")
		if auth != "Bearer jwt-chupacabra" {
			t.Errorf("expected auth header 'Bearer jwt-chupacabra', got %q", auth)
		}

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		vars, _ := body["variables"].(map[string]any)
		region, _ := vars["region"].(string)

		resp := map[string]any{
			"data": map[string]any{
				"cryptids": map[string]any{
					"items": []map[string]any{
						{"id": "1", "name": "Chupacabra", "region": region, "credibility_score": 3},
						{"id": "2", "name": "Jersey Devil", "region": "Pine Barrens", "credibility_score": 7},
					},
					"totalCount": 2,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	drv := New(cfg, nil)
	drv.authToken = "jwt-chupacabra"

	call := provider.ToolCall{
		ID:        "toolu_cryptid_hunt",
		Name:      "browse_cryptids",
		Arguments: `{"region": "Southwest", "limit": 10}`,
	}

	result, err := drv.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if result.ToolID != "toolu_cryptid_hunt" {
		t.Errorf("expected tool ID 'toolu_cryptid_hunt', got %q", result.ToolID)
	}

	// Should contain the extracted data.
	if !strings.Contains(result.Content, "Chupacabra") {
		t.Error("expected result to contain 'Chupacabra'")
	}

	// Should contain context hint.
	if !strings.Contains(result.Content, "report_sighting") {
		t.Error("expected result to contain context hint about report_sighting")
	}
}

func TestDriver_Execute_UnknownTool(t *testing.T) {
	cfg := testAppConfig("http://localhost")
	drv := New(cfg, nil)

	call := provider.ToolCall{
		ID:   "toolu_nope",
		Name: "summon_cthulhu",
	}

	result, err := drv.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error for unknown tool")
	}
	if !strings.Contains(result.Content, "unknown tool") {
		t.Errorf("unexpected error content: %s", result.Content)
	}
}

func TestDriver_Execute_InvalidArguments(t *testing.T) {
	cfg := testAppConfig("http://localhost")
	drv := New(cfg, nil)

	call := provider.ToolCall{
		ID:        "toolu_bad",
		Name:      "browse_cryptids",
		Arguments: "not valid json at all",
	}

	result, err := drv.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error for invalid arguments")
	}
	if !strings.Contains(result.Content, "invalid arguments") {
		t.Errorf("unexpected error content: %s", result.Content)
	}
}

func TestDriver_Execute_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"errors": [{"message": "You are not authorized to browse cryptids"}]}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	drv := New(cfg, nil)
	drv.authToken = "expired-jwt"

	call := provider.ToolCall{
		ID:        "toolu_denied",
		Name:      "browse_cryptids",
		Arguments: `{}`,
	}

	result, err := drv.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for GraphQL error")
	}
	if !strings.Contains(result.Content, "not authorized") {
		t.Errorf("expected GraphQL error in content, got: %s", result.Content)
	}
}

func TestDriver_Execute_NoContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"data": {"reportSighting": {"id": "99", "createdAt": "2026-04-10"}}}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	drv := New(cfg, nil)

	call := provider.ToolCall{
		ID:        "toolu_report",
		Name:      "report_sighting",
		Arguments: `{"name": "Mothman", "location": "Point Pleasant Bridge"}`,
	}

	result, err := drv.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	// report_sighting has no context field, so no separator should be appended.
	if strings.Contains(result.Content, "---") {
		t.Error("expected no context separator for tool without context")
	}
}
