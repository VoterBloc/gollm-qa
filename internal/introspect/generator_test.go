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
	tools, _ := GenerateTools(fixtureSchema(), Options{})

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
	tools, _ := GenerateTools(fixtureSchema(), Options{Include: []string{"voterBlocs"}})
	if len(tools) != 1 || tools[0].Name != "voter_blocs" {
		t.Errorf("expected only voter_blocs tool, got %v", tools)
	}
}

func TestGenerateTools_ExcludeFilter(t *testing.T) {
	tools, _ := GenerateTools(fixtureSchema(), Options{Exclude: []string{"registerForTest"}})
	if findTool(tools, "register_for_test") != nil {
		t.Error("expected register_for_test to be excluded")
	}
}

func TestGenerateTools_UnmatchedFilters(t *testing.T) {
	// Snake-case-instead-of-camel-case footgun: "voter_blocs" matches no
	// GraphQL operation; the unmatched return value catches it.
	_, unmatched := GenerateTools(fixtureSchema(), Options{
		Include: []string{"voterBlocs", "voter_blocs"},
		Exclude: []string{"deleteEverything"},
	})

	want := map[string]bool{"voter_blocs": true, "deleteEverything": true}
	if len(unmatched) != len(want) {
		t.Fatalf("expected 2 unmatched names, got %v", unmatched)
	}
	for _, name := range unmatched {
		if !want[name] {
			t.Errorf("unexpected unmatched entry %q", name)
		}
	}
}

func TestGenerateTools_AllFiltersMatched(t *testing.T) {
	_, unmatched := GenerateTools(fixtureSchema(), Options{
		Include: []string{"voterBlocs", "me"},
		Exclude: []string{"registerForTest"},
	})
	if len(unmatched) != 0 {
		t.Errorf("expected no unmatched filters, got %v", unmatched)
	}
}

func TestGenerateTools_QueryShape(t *testing.T) {
	tools, _ := GenerateTools(fixtureSchema(), Options{})
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
	tools, _ := GenerateTools(fixtureSchema(), Options{})
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
	// Input object now expands into Properties — no more stringified shape hint.
	if p.Type != "object" {
		t.Errorf("expected type 'object', got %q", p.Type)
	}
	if len(p.Properties) != 3 {
		t.Fatalf("expected 3 nested properties (email, username, password), got %d", len(p.Properties))
	}
	propNames := map[string]bool{}
	for _, sub := range p.Properties {
		propNames[sub.Name] = true
		if sub.Name == "email" && !sub.Required {
			t.Error("expected email to be required (NON_NULL)")
		}
		if sub.Type != "string" {
			t.Errorf("expected nested %q to be string, got %q", sub.Name, sub.Type)
		}
	}
	for _, want := range []string{"email", "username", "password"} {
		if !propNames[want] {
			t.Errorf("expected nested property %q on input", want)
		}
	}
}

func TestGenerateTools_LeafQueryNoArgs(t *testing.T) {
	tools, _ := GenerateTools(fixtureSchema(), Options{})
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

func TestGenerateTools_ListAndEnum(t *testing.T) {
	// Schema with a query taking [String!]! (required list of required strings)
	// and an enum arg. Verifies array Items and EnumValues handling.
	intT := &TypeRef{Kind: "SCALAR", Name: "Int"}
	stringT := &TypeRef{Kind: "SCALAR", Name: "String"}
	nonNullString := &TypeRef{Kind: "NON_NULL", OfType: stringT}
	listOfStrings := &TypeRef{
		Kind:   "NON_NULL",
		OfType: &TypeRef{Kind: "LIST", OfType: nonNullString},
	}
	statusEnum := &TypeRef{Kind: "ENUM", Name: "CampaignStatus"}

	schema := &Schema{
		QueryType: &TypeRef{Kind: "OBJECT", Name: "Query"},
		Types: []Type{
			{
				Kind: "OBJECT",
				Name: "Query",
				Fields: []Field{
					{
						Name: "searchCampaigns",
						Args: []InputValue{
							{Name: "tags", Type: listOfStrings, Description: "filter tags"},
							{Name: "status", Type: statusEnum, Description: "filter by status"},
							{Name: "limit", Type: intT},
						},
						Type: &TypeRef{Kind: "SCALAR", Name: "Int"},
					},
				},
			},
			{
				Kind: "ENUM",
				Name: "CampaignStatus",
				EnumValues: []EnumValue{
					{Name: "DRAFT"},
					{Name: "ACTIVE"},
					{Name: "EXPIRED"},
				},
			},
		},
	}

	tools, _ := GenerateTools(schema, Options{})
	tc := findTool(tools, "search_campaigns")
	if tc == nil {
		t.Fatal("search_campaigns tool not found")
	}
	if len(tc.Parameters) != 3 {
		t.Fatalf("expected 3 params, got %d", len(tc.Parameters))
	}

	// Tags: array of strings, required.
	tags := tc.Parameters[0]
	if tags.Type != "array" {
		t.Errorf("expected tags type 'array', got %q", tags.Type)
	}
	if !tags.Required {
		t.Error("expected tags to be required (NON_NULL outer)")
	}
	if tags.Items == nil || tags.Items.Type != "string" {
		t.Errorf("expected tags.items.type 'string', got %v", tags.Items)
	}

	// Status: enum, optional.
	status := tc.Parameters[1]
	if status.Type != "string" {
		t.Errorf("expected status type 'string' (enum), got %q", status.Type)
	}
	if status.Required {
		t.Error("expected status to be optional")
	}
	if len(status.EnumValues) != 3 {
		t.Errorf("expected 3 enum values, got %v", status.EnumValues)
	}

	// Limit: integer.
	if tc.Parameters[2].Type != "integer" {
		t.Errorf("expected limit type 'integer', got %q", tc.Parameters[2].Type)
	}
}

func TestGenerateTools_SelfReferentialInputCappedByDepth(t *testing.T) {
	// FilterInput { children: [FilterInput] } — recursive. Without the depth
	// cap the generator would loop forever; with it, expansion stops cleanly.
	filterInput := &TypeRef{Kind: "INPUT_OBJECT", Name: "FilterInput"}
	stringT := &TypeRef{Kind: "SCALAR", Name: "String"}

	schema := &Schema{
		QueryType: &TypeRef{Kind: "OBJECT", Name: "Query"},
		Types: []Type{
			{
				Kind: "OBJECT",
				Name: "Query",
				Fields: []Field{
					{
						Name: "search",
						Args: []InputValue{
							{Name: "filter", Type: filterInput},
						},
						Type: &TypeRef{Kind: "SCALAR", Name: "Int"},
					},
				},
			},
			{
				Kind: "INPUT_OBJECT",
				Name: "FilterInput",
				InputFields: []InputValue{
					{Name: "field", Type: stringT},
					{Name: "children", Type: &TypeRef{Kind: "LIST", OfType: filterInput}},
				},
			},
		},
	}

	// If the depth cap is broken this either OOMs or stack-overflows.
	tools, _ := GenerateTools(schema, Options{})
	tc := findTool(tools, "search")
	if tc == nil {
		t.Fatal("search tool not found")
	}
	// Just walk the tree — the test passes if generation completes.
	depth := paramDepth(tc.Parameters[0])
	if depth > maxInputObjectDepth+2 {
		t.Errorf("expected expansion bounded by maxInputObjectDepth, got depth %d", depth)
	}
}

func paramDepth(p config.ParamConfig) int {
	max := 0
	for _, sub := range p.Properties {
		if d := paramDepth(sub); d > max {
			max = d
		}
	}
	if p.Items != nil {
		if d := paramDepth(*p.Items); d > max {
			max = d
		}
	}
	return max + 1
}

func TestGenerateTools_SkipsMetaTypes(t *testing.T) {
	schema := fixtureSchema()
	queryType := schema.TypeByName("Query")
	queryType.Fields = append(queryType.Fields, Field{
		Name: "__schema",
		Type: &TypeRef{Kind: "OBJECT", Name: "Query"},
	})

	tools, _ := GenerateTools(schema, Options{})
	for _, tc := range tools {
		if strings.HasPrefix(tc.Name, "__") {
			t.Errorf("expected meta-fields to be skipped, found %q", tc.Name)
		}
	}
}
