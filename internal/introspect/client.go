package introspect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// introspectionQuery is the standard GraphQL introspection query, restricted
// to the slices we actually consume (no directives, no possibleTypes for
// unions/interfaces — gollm doesn't generate fragments).
const introspectionQuery = `query GollmIntrospect {
  __schema {
    queryType { name }
    mutationType { name }
    types {
      kind
      name
      description
      fields(includeDeprecated: false) {
        name
        description
        args {
          name
          description
          defaultValue
          type { ...TypeRef }
        }
        type { ...TypeRef }
      }
      inputFields {
        name
        description
        defaultValue
        type { ...TypeRef }
      }
      enumValues(includeDeprecated: false) {
        name
        description
      }
    }
  }
}

fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType {
                kind
                name
              }
            }
          }
        }
      }
    }
  }
}`

// Introspect fetches and parses the schema from a GraphQL endpoint. headers
// is optional and lets the caller pass auth (e.g. an admin token) if the
// endpoint requires it.
func Introspect(ctx context.Context, endpoint string, headers map[string]string) (*Schema, error) {
	body, err := json.Marshal(map[string]any{"query": introspectionQuery})
	if err != nil {
		return nil, fmt.Errorf("marshaling introspection query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("introspection request: %w", err)
	}
	defer resp.Body.Close()

	const maxResponseSize = 32 << 20 // 32 MB — schemas can be large
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("reading introspection response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("introspection HTTP %d: %s", resp.StatusCode, string(data))
	}

	return parseSchemaResponse(data)
}

// parseSchemaResponse parses a GraphQL introspection response into a Schema.
// Surfaced as a separate function so tests can feed in canned JSON without
// standing up an httptest server for every case.
func parseSchemaResponse(data []byte) (*Schema, error) {
	var resp struct {
		Data struct {
			Schema struct {
				QueryType    *TypeRef `json:"queryType"`
				MutationType *TypeRef `json:"mutationType"`
				Types        []Type   `json:"types"`
			} `json:"__schema"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshaling introspection response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("introspection errors: %v", resp.Errors)
	}
	if resp.Data.Schema.QueryType == nil {
		return nil, fmt.Errorf("introspection response missing queryType")
	}
	return &Schema{
		QueryType:    resp.Data.Schema.QueryType,
		MutationType: resp.Data.Schema.MutationType,
		Types:        resp.Data.Schema.Types,
	}, nil
}
