// Package introspect queries a GraphQL endpoint's __schema and converts the
// response into gollm tool configs. Removes the need to hand-author every
// tool's query string in YAML, and catches schema drift at config-load time
// rather than on first call.
package introspect

import "strings"

// Schema is the parsed result of a GraphQL introspection query.
type Schema struct {
	QueryType    *TypeRef `json:"queryType"`
	MutationType *TypeRef `json:"mutationType"`
	Types        []Type   `json:"types"`
}

// TypeByName returns the named type definition, or nil if absent.
func (s *Schema) TypeByName(name string) *Type {
	for i := range s.Types {
		if s.Types[i].Name == name {
			return &s.Types[i]
		}
	}
	return nil
}

// Type is a top-level type definition (object, input, enum, scalar, etc.).
type Type struct {
	Kind        string       `json:"kind"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Fields      []Field      `json:"fields"`      // for OBJECT and INTERFACE
	InputFields []InputValue `json:"inputFields"` // for INPUT_OBJECT
	EnumValues  []EnumValue  `json:"enumValues"`  // for ENUM
}

// Field is a field on an object type, including its arguments and return type.
type Field struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Args        []InputValue `json:"args"`
	Type        *TypeRef     `json:"type"`
}

// InputValue is a function argument or input-object field.
type InputValue struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Type         *TypeRef `json:"type"`
	DefaultValue string   `json:"defaultValue"`
}

// EnumValue is a single member of an enum type.
type EnumValue struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// TypeRef is a reference to a type, possibly wrapped in NonNull and List
// modifiers. The introspection response models these as recursive: a
// non-null list of strings is `NON_NULL(LIST(NON_NULL(SCALAR(String))))`.
type TypeRef struct {
	Kind   string   `json:"kind"`   // OBJECT, SCALAR, ENUM, INPUT_OBJECT, INTERFACE, UNION, LIST, NON_NULL
	Name   string   `json:"name"`   // empty for LIST and NON_NULL wrappers
	OfType *TypeRef `json:"ofType"` // wrapped type for LIST/NON_NULL
}

// IsRequired reports whether the outermost wrapper is NON_NULL.
func (t *TypeRef) IsRequired() bool {
	return t != nil && t.Kind == "NON_NULL"
}

// Unwrap strips NON_NULL and LIST wrappers and returns the named type ref.
// Useful for asking "what's the underlying type of this field" without
// caring whether it's optional or wrapped in a list.
func (t *TypeRef) Unwrap() *TypeRef {
	for t != nil && (t.Kind == "NON_NULL" || t.Kind == "LIST") {
		t = t.OfType
	}
	return t
}

// IsList reports whether the type is a list (possibly inside a NON_NULL).
func (t *TypeRef) IsList() bool {
	if t == nil {
		return false
	}
	if t.Kind == "LIST" {
		return true
	}
	if t.Kind == "NON_NULL" {
		return t.OfType.IsList()
	}
	return false
}

// GraphQLString renders the type ref back to GraphQL syntax (e.g. `[String!]!`).
// Used when generating the operation header for a tool's query.
func (t *TypeRef) GraphQLString() string {
	if t == nil {
		return ""
	}
	switch t.Kind {
	case "NON_NULL":
		return t.OfType.GraphQLString() + "!"
	case "LIST":
		return "[" + t.OfType.GraphQLString() + "]"
	default:
		return t.Name
	}
}

// IsScalarOrEnum reports whether the underlying named type is a scalar or
// enum (i.e. a leaf value, not an object). Used by the selection-set
// heuristic to decide whether to expand a field.
func (t *TypeRef) IsScalarOrEnum() bool {
	u := t.Unwrap()
	return u != nil && (u.Kind == "SCALAR" || u.Kind == "ENUM")
}

// camelToSnake converts camelCase to snake_case. "browseBlocs" → "browse_blocs",
// "voterBlocs" → "voter_blocs". Leaves already-snake-cased input alone.
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
