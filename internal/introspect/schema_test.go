package introspect

import "testing"

func TestTypeRef_IsRequired(t *testing.T) {
	required := &TypeRef{Kind: "NON_NULL", OfType: &TypeRef{Kind: "SCALAR", Name: "String"}}
	if !required.IsRequired() {
		t.Error("expected NON_NULL wrapper to count as required")
	}
	optional := &TypeRef{Kind: "SCALAR", Name: "String"}
	if optional.IsRequired() {
		t.Error("expected bare scalar to count as optional")
	}
	var nilRef *TypeRef
	if nilRef.IsRequired() {
		t.Error("nil type ref should not be required")
	}
}

func TestTypeRef_Unwrap(t *testing.T) {
	// [String!]! — non-null list of non-null strings
	ref := &TypeRef{
		Kind: "NON_NULL",
		OfType: &TypeRef{
			Kind: "LIST",
			OfType: &TypeRef{
				Kind: "NON_NULL",
				OfType: &TypeRef{Kind: "SCALAR", Name: "String"},
			},
		},
	}
	got := ref.Unwrap()
	if got.Name != "String" {
		t.Errorf("expected unwrap to find String, got %q", got.Name)
	}
}

func TestTypeRef_IsList(t *testing.T) {
	tests := []struct {
		name string
		ref  *TypeRef
		want bool
	}{
		{
			name: "bare scalar",
			ref:  &TypeRef{Kind: "SCALAR", Name: "Int"},
			want: false,
		},
		{
			name: "list of scalars",
			ref:  &TypeRef{Kind: "LIST", OfType: &TypeRef{Kind: "SCALAR", Name: "Int"}},
			want: true,
		},
		{
			name: "non-null list",
			ref: &TypeRef{
				Kind:   "NON_NULL",
				OfType: &TypeRef{Kind: "LIST", OfType: &TypeRef{Kind: "SCALAR", Name: "Int"}},
			},
			want: true,
		},
		{
			name: "non-null scalar (not list)",
			ref:  &TypeRef{Kind: "NON_NULL", OfType: &TypeRef{Kind: "SCALAR", Name: "Int"}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.IsList(); got != tt.want {
				t.Errorf("IsList() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTypeRef_GraphQLString(t *testing.T) {
	tests := []struct {
		name string
		ref  *TypeRef
		want string
	}{
		{"bare scalar", &TypeRef{Kind: "SCALAR", Name: "String"}, "String"},
		{"non-null scalar", &TypeRef{Kind: "NON_NULL", OfType: &TypeRef{Kind: "SCALAR", Name: "ID"}}, "ID!"},
		{
			"non-null list of non-null strings",
			&TypeRef{
				Kind: "NON_NULL",
				OfType: &TypeRef{
					Kind: "LIST",
					OfType: &TypeRef{
						Kind:   "NON_NULL",
						OfType: &TypeRef{Kind: "SCALAR", Name: "String"},
					},
				},
			},
			"[String!]!",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.GraphQLString(); got != tt.want {
				t.Errorf("GraphQLString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTypeRef_IsScalarOrEnum(t *testing.T) {
	scalar := &TypeRef{Kind: "SCALAR", Name: "Int"}
	enum := &TypeRef{Kind: "ENUM", Name: "PostType"}
	object := &TypeRef{Kind: "OBJECT", Name: "VoterBloc"}
	wrappedScalar := &TypeRef{Kind: "NON_NULL", OfType: &TypeRef{Kind: "SCALAR", Name: "ID"}}

	if !scalar.IsScalarOrEnum() {
		t.Error("expected scalar to be leaf")
	}
	if !enum.IsScalarOrEnum() {
		t.Error("expected enum to be leaf")
	}
	if object.IsScalarOrEnum() {
		t.Error("expected object NOT to be leaf")
	}
	if !wrappedScalar.IsScalarOrEnum() {
		t.Error("expected wrapped scalar to be leaf")
	}
}

func TestSchema_TypeByName(t *testing.T) {
	s := &Schema{Types: []Type{
		{Name: "VoterBloc", Kind: "OBJECT"},
		{Name: "Sasquatch", Kind: "OBJECT"},
	}}
	if got := s.TypeByName("Sasquatch"); got == nil || got.Kind != "OBJECT" {
		t.Errorf("expected to find Sasquatch, got %v", got)
	}
	if got := s.TypeByName("Mothman"); got != nil {
		t.Errorf("expected nil for missing type, got %v", got)
	}
}

func TestCamelToSnake(t *testing.T) {
	tests := []struct{ in, want string }{
		{"browseBlocs", "browse_blocs"},
		{"voterBlocs", "voter_blocs"},
		{"me", "me"},
		{"already_snake", "already_snake"},
		{"registerForTest", "register_for_test"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := camelToSnake(tt.in); got != tt.want {
			t.Errorf("camelToSnake(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
