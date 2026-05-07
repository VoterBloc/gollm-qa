package introspect

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const cryptidSchemaResponse = `{
  "data": {
    "__schema": {
      "queryType": { "kind": "OBJECT", "name": "Query" },
      "mutationType": { "kind": "OBJECT", "name": "Mutation" },
      "types": [
        {
          "kind": "OBJECT",
          "name": "Query",
          "description": "",
          "fields": [
            {
              "name": "sightings",
              "description": "List recent cryptid sightings.",
              "args": [
                {"name": "region", "description": "Geographic region", "defaultValue": null,
                 "type": {"kind": "SCALAR", "name": "String", "ofType": null}},
                {"name": "limit", "description": "Max results", "defaultValue": "10",
                 "type": {"kind": "SCALAR", "name": "Int", "ofType": null}}
              ],
              "type": {
                "kind": "LIST",
                "name": null,
                "ofType": {"kind": "OBJECT", "name": "Sighting", "ofType": null}
              }
            }
          ],
          "inputFields": null,
          "enumValues": null
        },
        {
          "kind": "OBJECT",
          "name": "Sighting",
          "description": "A reported cryptid sighting.",
          "fields": [
            {"name": "id", "description": "", "args": [],
             "type": {"kind": "NON_NULL", "name": null,
                      "ofType": {"kind": "SCALAR", "name": "ID", "ofType": null}}},
            {"name": "name", "description": "", "args": [],
             "type": {"kind": "SCALAR", "name": "String", "ofType": null}}
          ],
          "inputFields": null,
          "enumValues": null
        }
      ]
    }
  }
}`

func TestParseSchemaResponse(t *testing.T) {
	schema, err := parseSchemaResponse([]byte(cryptidSchemaResponse))
	if err != nil {
		t.Fatalf("parseSchemaResponse() error: %v", err)
	}

	if schema.QueryType.Name != "Query" {
		t.Errorf("expected query type 'Query', got %q", schema.QueryType.Name)
	}
	if schema.MutationType.Name != "Mutation" {
		t.Errorf("expected mutation type 'Mutation', got %q", schema.MutationType.Name)
	}
	if len(schema.Types) != 2 {
		t.Fatalf("expected 2 types, got %d", len(schema.Types))
	}

	queryType := schema.TypeByName("Query")
	if queryType == nil || len(queryType.Fields) != 1 {
		t.Fatalf("expected Query type with 1 field, got %v", queryType)
	}
	sightings := queryType.Fields[0]
	if sightings.Name != "sightings" {
		t.Errorf("expected field 'sightings', got %q", sightings.Name)
	}
	if len(sightings.Args) != 2 {
		t.Errorf("expected 2 args on sightings, got %d", len(sightings.Args))
	}

	// Verify nested type unwrapping survived JSON parsing.
	if !sightings.Type.IsList() {
		t.Error("expected sightings return type to be a list")
	}
	if sightings.Type.Unwrap().Name != "Sighting" {
		t.Errorf("expected unwrapped type 'Sighting', got %q", sightings.Type.Unwrap().Name)
	}
}

func TestParseSchemaResponse_GraphQLErrors(t *testing.T) {
	resp := `{"errors": [{"message": "the schema is haunted"}]}`
	_, err := parseSchemaResponse([]byte(resp))
	if err == nil {
		t.Fatal("expected error for GraphQL errors block")
	}
	if !strings.Contains(err.Error(), "haunted") {
		t.Errorf("expected error to surface GraphQL message, got: %v", err)
	}
}

func TestIntrospect_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header from the caller made it to the wire.
		if r.Header.Get("Authorization") != "Bearer admin-jwt-loch-ness" {
			t.Errorf("expected admin auth header, got %q", r.Header.Get("Authorization"))
		}

		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		query, _ := req["query"].(string)
		if !strings.Contains(query, "__schema") {
			t.Errorf("expected introspection query, got: %s", query)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(cryptidSchemaResponse))
	}))
	defer server.Close()

	schema, err := Introspect(context.Background(), server.URL, map[string]string{
		"Authorization": "Bearer admin-jwt-loch-ness",
	})
	if err != nil {
		t.Fatalf("Introspect() error: %v", err)
	}
	if schema.TypeByName("Sighting") == nil {
		t.Error("expected Sighting type in parsed schema")
	}
}

func TestIntrospect_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("forbidden, you are not Bigfoot enough"))
	}))
	defer server.Close()

	_, err := Introspect(context.Background(), server.URL, nil)
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected status code in error, got: %v", err)
	}
}
