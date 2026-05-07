package introspect

import (
	"strings"
	"testing"

	"github.com/VoterBloc/gollm-qa/internal/config"
)

// fixtureSchema is a hand-built schema that mirrors a slice of VoterBloc-shaped
// surface: a paginated query with mixed args, an input-object-driven mutation,
// nested objects, and a couple of scalar/enum patterns.
func fixtureSchema() *Schema {
	stringT := &TypeRef{Kind: "SCALAR", Name: "String"}
	intT := &TypeRef{Kind: "SCALAR", Name: "Int"}
	idT := &TypeRef{Kind: "SCALAR", Name: "ID"}
	nonNullID := &TypeRef{Kind: "NON_NULL", OfType: idT}
	nonNullString := &TypeRef{Kind: "NON_NULL", OfType: stringT}

	blocObj := &TypeRef{Kind: "OBJECT", Name: "VoterBloc"}
	blocListObj := &TypeRef{Kind: "OBJECT", Name: "VoterBlocList"}
	registerInput := &TypeRef{Kind: "INPUT_OBJECT", Name: "RegisterInput"}
	authResp := &TypeRef{Kind: "OBJECT", Name: "AuthResponse"}
	userObj := &TypeRef{Kind: "OBJECT", Name: "User"}

	return &Schema{
		QueryType:    &TypeRef{Kind: "OBJECT", Name: "Query"},
		MutationType: &TypeRef{Kind: "OBJECT", Name: "Mutation"},
		Types: []Type{
			{
				Kind: "OBJECT",
				Name: "Query",
				Fields: []Field{
					{
						Name:        "voterBlocs",
						Description: "Browse voter blocs.",
						Args: []InputValue{
							{Name: "search", Description: "search by name", Type: stringT},
							{Name: "state", Description: "two-letter state code", Type: stringT},
							{Name: "limit", Description: "max results", Type: intT},
						},
						Type: blocListObj,
					},
					{
						Name:        "me",
						Description: "Current user's profile.",
						Type:        userObj,
					},
				},
			},
			{
				Kind: "OBJECT",
				Name: "Mutation",
				Fields: []Field{
					{
						Name:        "registerForTest",
						Description: "Test-only registration; auto-verifies email.",
						Args: []InputValue{
							{Name: "input", Description: "registration payload",
								Type: &TypeRef{Kind: "NON_NULL", OfType: registerInput}},
						},
						Type: authResp,
					},
				},
			},
			{
				Kind: "OBJECT",
				Name: "VoterBlocList",
				Fields: []Field{
					{Name: "items", Type: &TypeRef{Kind: "LIST", OfType: blocObj}},
					{Name: "totalCount", Type: nonNullID},
				},
			},
			{
				Kind: "OBJECT",
				Name: "VoterBloc",
				Fields: []Field{
					{Name: "id", Type: nonNullID},
					{Name: "name", Type: nonNullString},
					{Name: "memberCount", Type: intT},
				},
			},
			{
				Kind: "OBJECT",
				Name: "User",
				Fields: []Field{
					{Name: "id", Type: nonNullID},
					{Name: "username", Type: nonNullString},
				},
			},
			{
				Kind: "OBJECT",
				Name: "AuthResponse",
				Fields: []Field{
					{Name: "token", Type: nonNullString},
					{Name: "user", Type: userObj},
				},
			},
			{
				Kind: "INPUT_OBJECT",
				Name: "RegisterInput",
				InputFields: []InputValue{
					{Name: "email", Type: nonNullString},
					{Name: "username", Type: nonNullString},
					{Name: "password", Type: nonNullString},
				},
			},
		},
	}
}

func findTool(tools []config.ToolConfig, name string) *config.ToolConfig {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

func TestGenerateTools_NamesAndCounts(t *testing.T) {
	tools := GenerateTools(fixtureSchema(), Options{})

	if len(tools) != 3 {
		t.Fatalf("expected 3 tools (voterBlocs, me, registerForTest), got %d", len(tools))
	}
	for _, want := range []string{"voter_blocs", "me", "register_for_test"} {
		if findTool(tools, want) == nil {
			t.Errorf("expected tool %q in generated set", want)
		}
	}
}

func TestGenerateTools_IncludeFilter(t *testing.T) {
	tools := GenerateTools(fixtureSchema(), Options{Include: []string{"voterBlocs"}})
	if len(tools) != 1 || tools[0].Name != "voter_blocs" {
		t.Errorf("expected only voter_blocs tool, got %v", tools)
	}
}

func TestGenerateTools_ExcludeFilter(t *testing.T) {
	tools := GenerateTools(fixtureSchema(), Options{Exclude: []string{"registerForTest"}})
	if findTool(tools, "register_for_test") != nil {
		t.Error("expected register_for_test to be excluded")
	}
}

func TestGenerateTools_QueryShape(t *testing.T) {
	tools := GenerateTools(fixtureSchema(), Options{})
	blocs := findTool(tools, "voter_blocs")
	if blocs == nil {
		t.Fatal("voter_blocs tool not found")
	}

	q := blocs.Query
	for _, want := range []string{
		"query Query_voterBlocs",
		"$search: String",
		"$limit: Int",
		"voterBlocs(search: $search, state: $state, limit: $limit)",
		"items {",
		"totalCount",
		"id",
		"name",
		"memberCount",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("expected query to contain %q, got:\n%s", want, q)
		}
	}
	if blocs.ResultPath != "data.voterBlocs" {
		t.Errorf("unexpected result_path: %q", blocs.ResultPath)
	}
}

func TestGenerateTools_MutationWithInputObject(t *testing.T) {
	tools := GenerateTools(fixtureSchema(), Options{})
	register := findTool(tools, "register_for_test")
	if register == nil {
		t.Fatal("register_for_test tool not found")
	}

	q := register.Query
	for _, want := range []string{
		"mutation Mutation_registerForTest",
		"$input: RegisterInput!",
		"registerForTest(input: $input)",
		"token",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("expected mutation to contain %q, got:\n%s", want, q)
		}
	}

	if len(register.Parameters) != 1 {
		t.Fatalf("expected 1 param on register_for_test, got %d", len(register.Parameters))
	}
	p := register.Parameters[0]
	if p.Name != "input" || !p.Required {
		t.Errorf("expected required input param, got %+v", p)
	}
	if !strings.Contains(p.Description, "email") || !strings.Contains(p.Description, "RegisterInput") {
		t.Errorf("expected input shape hint in description, got %q", p.Description)
	}
}

func TestGenerateTools_LeafQueryNoArgs(t *testing.T) {
	tools := GenerateTools(fixtureSchema(), Options{})
	me := findTool(tools, "me")
	if me == nil {
		t.Fatal("me tool not found")
	}
	if len(me.Parameters) != 0 {
		t.Errorf("expected no params on me, got %v", me.Parameters)
	}
	if !strings.Contains(me.Query, "me { id username }") {
		t.Errorf("expected me to expand to scalar selection set, got:\n%s", me.Query)
	}
}

func TestGenerateTools_SkipsMetaTypes(t *testing.T) {
	schema := fixtureSchema()
	queryType := schema.TypeByName("Query")
	queryType.Fields = append(queryType.Fields, Field{
		Name: "__schema",
		Type: &TypeRef{Kind: "OBJECT", Name: "Query"},
	})

	tools := GenerateTools(schema, Options{})
	for _, tc := range tools {
		if strings.HasPrefix(tc.Name, "__") {
			t.Errorf("expected meta-fields to be skipped, found %q", tc.Name)
		}
	}
}
