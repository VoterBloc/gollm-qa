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

func TestDriver_Register(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		query, _ := body["query"].(string)
		if !strings.Contains(query, "registerForTest") {
			t.Errorf("expected registerForTest query, got: %s", query)
		}

		vars, _ := body["variables"].(map[string]any)
		input, ok := vars["input"].(map[string]any)
		if !ok {
			t.Fatalf("expected $input variable to be an object, got %T", vars["input"])
		}
		if input["email"] != "yeti@himalaya.example" {
			t.Errorf("unexpected email: %v", input["email"])
		}
		if input["username"] != "yeti_mountaineer" {
			t.Errorf("unexpected username: %v", input["username"])
		}

		resp := `{"data": {"registerForTest": {"token": "jwt-yeti-mountaineer"}}}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	cfg.Auth.RegisterQuery = `mutation RegisterForTest($input: RegisterInput!) {
		registerForTest(input: $input) { token }
	}`
	cfg.Auth.RegisterTokenPath = "data.registerForTest.token"
	drv := New(cfg, nil)

	err := drv.Register(context.Background(), map[string]any{
		"email":     "yeti@himalaya.example",
		"username":  "yeti_mountaineer",
		"password":  "AbominableSnow1!",
		"firstName": "Yeti",
		"lastName":  "Snowfoot",
	})
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	if drv.authToken != "jwt-yeti-mountaineer" {
		t.Errorf("expected token 'jwt-yeti-mountaineer', got %q", drv.authToken)
	}
}

func TestDriver_Register_NoTokenPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"data": {"registerForTest": {"id": "user-mothman-42"}}}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	cfg.Auth.RegisterQuery = `mutation { registerForTest { id } }`
	// No RegisterTokenPath — register-only flow, caller follows up with Login.
	drv := New(cfg, nil)

	if err := drv.Register(context.Background(), map[string]any{"email": "mothman@pointpleasant.wv"}); err != nil {
		t.Fatalf("Register() error: %v", err)
	}
	if drv.authToken != "" {
		t.Errorf("expected no token stored, got %q", drv.authToken)
	}
}

func TestDriver_Register_MissingQuery(t *testing.T) {
	cfg := testAppConfig("http://localhost")
	drv := New(cfg, nil)

	err := drv.Register(context.Background(), map[string]any{"email": "nobody@nowhere.example"})
	if err == nil {
		t.Fatal("expected error when register_query is empty")
	}
	if !strings.Contains(err.Error(), "no register_query") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDriver_Register_TokenPathMissingFromResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Server returns a perfectly valid response — just at a different path
		// than the config expects. Simulates a misconfigured register_token_path.
		resp := `{"data": {"registerForTest": {"user": {"id": "user-jackalope-7"}}}}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	cfg.Auth.RegisterQuery = `mutation { registerForTest { user { id } } }`
	cfg.Auth.RegisterTokenPath = "data.registerForTest.token"
	drv := New(cfg, nil)

	err := drv.Register(context.Background(), map[string]any{"email": "jackalope@wyoming.example"})
	if err == nil {
		t.Fatal("expected error when configured token path is absent from response")
	}
	if !strings.Contains(err.Error(), "missing token at path") {
		t.Errorf("expected 'missing token at path' error, got: %v", err)
	}
	if drv.authToken != "" {
		t.Errorf("expected no token stored on failure, got %q", drv.authToken)
	}
}

func TestDriver_Purge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify admin auth is sent.
		auth := r.Header.Get("Authorization")
		if auth != "Bearer admin-jwt-loch-ness" {
			t.Errorf("expected admin auth header, got %q", auth)
		}

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		query, _ := body["query"].(string)
		if !strings.Contains(query, "purgeTestData") {
			t.Errorf("expected purgeTestData query, got: %s", query)
		}

		resp := `{"data": {"purgeTestData": {"byTable": [{"table": "bigfoot_sightings", "deleted": 7}, {"table": "users", "deleted": 3}], "total": 10}}}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	cfg.Admin = config.AdminConfig{
		PurgeQuery:      `mutation { purgeTestData { byTable { table deleted } total } }`,
		PurgeResultPath: "data.purgeTestData",
	}
	drv := New(cfg, nil)
	drv.SetAuthToken("admin-jwt-loch-ness")

	report, err := drv.Purge(context.Background())
	if err != nil {
		t.Fatalf("Purge() error: %v", err)
	}
	if !strings.Contains(report, "bigfoot_sightings") {
		t.Errorf("expected report to contain bigfoot_sightings, got: %s", report)
	}
	if !strings.Contains(report, `"total": 10`) {
		t.Errorf("expected report to contain total: 10, got: %s", report)
	}
}

func TestDriver_Purge_MissingQuery(t *testing.T) {
	cfg := testAppConfig("http://localhost")
	// No Admin config set.
	drv := New(cfg, nil)

	_, err := drv.Purge(context.Background())
	if err == nil {
		t.Fatal("expected error when admin.purge_query is empty")
	}
	if !strings.Contains(err.Error(), "no admin.purge_query") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDriver_Purge_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"errors": [{"message": "Yeti tried to purge but lacks admin role"}]}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	cfg.Admin = config.AdminConfig{
		PurgeQuery:      `mutation { purgeTestData { total } }`,
		PurgeResultPath: "data.purgeTestData",
	}
	drv := New(cfg, nil)
	drv.SetAuthToken("non-admin-jwt")

	_, err := drv.Purge(context.Background())
	if err == nil {
		t.Fatal("expected purge error")
	}
	if !strings.Contains(err.Error(), "Yeti") {
		t.Errorf("expected GraphQL error in message, got: %v", err)
	}
}

func TestDriver_Purge_MissingResultPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"data": {"somethingElse": {"value": 1}}}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	cfg.Admin = config.AdminConfig{
		PurgeQuery:      `mutation { purgeTestData { total } }`,
		PurgeResultPath: "data.purgeTestData",
	}
	drv := New(cfg, nil)
	drv.SetAuthToken("jwt")

	_, err := drv.Purge(context.Background())
	if err == nil {
		t.Fatal("expected error when result_path is absent from response")
	}
	if !strings.Contains(err.Error(), "missing data at path") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDriver_Register_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"errors": [{"message": "Email already used by a sasquatch"}]}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	cfg := testAppConfig(server.URL)
	cfg.Auth.RegisterQuery = `mutation { registerForTest { token } }`
	cfg.Auth.RegisterTokenPath = "data.registerForTest.token"
	drv := New(cfg, nil)

	err := drv.Register(context.Background(), map[string]any{"email": "fake@fake.example"})
	if err == nil {
		t.Fatal("expected register error")
	}
	if !strings.Contains(err.Error(), "sasquatch") {
		t.Errorf("expected GraphQL error in message, got: %v", err)
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
