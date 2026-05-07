package introspect

import (
	"fmt"
	"sort"
	"strings"

	"github.com/VoterBloc/gollm-qa/internal/config"
)

// Options control which operations become tools and how nested objects expand
// in the auto-generated selection sets.
type Options struct {
	// Include is an optional allowlist of GraphQL field names (camelCase, as
	// they appear on Query/Mutation). Empty = include everything.
	Include []string
	// Exclude is an optional denylist. Applied after Include.
	Exclude []string
	// MaxSelectionDepth caps how deep object expansion goes in selection sets.
	// 1 means "scalars on the top type only"; 2 means "scalars on top type
	// plus scalars one layer down through nested objects." Default 2.
	MaxSelectionDepth int
}

// GenerateTools walks the schema and returns a ToolConfig per top-level
// query and mutation field. The generated configs are drop-in replacements
// for hand-written entries in apps/<app>.yaml's tools block.
//
// unmatched lists Include/Exclude entries that didn't match any GraphQL
// operation. Empty in the happy case; non-empty when a user wrote a name
// that doesn't exist in the schema (e.g. snake_case "voter_blocs" instead
// of camelCase "voterBlocs"). Callers should surface these as warnings.
func GenerateTools(schema *Schema, opts Options) (tools []config.ToolConfig, unmatched []string) {
	if opts.MaxSelectionDepth <= 0 {
		opts.MaxSelectionDepth = 2
	}

	includeMatched := matchedSet(opts.Include)
	excludeMatched := matchedSet(opts.Exclude)

	if schema.QueryType != nil {
		queryType := schema.TypeByName(schema.QueryType.Name)
		if queryType != nil {
			for _, field := range queryType.Fields {
				if shouldSkip(field.Name, includeMatched, excludeMatched) {
					continue
				}
				tools = append(tools, generateTool(schema, "query", field, opts))
			}
		}
	}
	if schema.MutationType != nil {
		mutType := schema.TypeByName(schema.MutationType.Name)
		if mutType != nil {
			for _, field := range mutType.Fields {
				if shouldSkip(field.Name, includeMatched, excludeMatched) {
					continue
				}
				tools = append(tools, generateTool(schema, "mutation", field, opts))
			}
		}
	}

	for name, matched := range includeMatched {
		if !matched {
			unmatched = append(unmatched, name)
		}
	}
	for name, matched := range excludeMatched {
		if !matched {
			unmatched = append(unmatched, name)
		}
	}
	return tools, unmatched
}

// shouldSkip checks meta-prefix, allowlist, and denylist. Mutates the
// passed-in matched maps so the caller can detect entries that never
// matched any schema operation. Exclude is checked before include so an
// entry on both lists still counts as "matched" for warning purposes.
func shouldSkip(name string, include, exclude map[string]bool) bool {
	if strings.HasPrefix(name, "__") {
		return true // GraphQL meta-queries
	}
	excluded := false
	if _, ok := exclude[name]; ok {
		exclude[name] = true
		excluded = true
	}
	if len(include) > 0 {
		if _, ok := include[name]; ok {
			include[name] = true
		} else {
			return true
		}
	}
	return excluded
}

func generateTool(schema *Schema, opType string, field Field, opts Options) config.ToolConfig {
	tc := config.ToolConfig{
		Name:        camelToSnake(field.Name),
		Description: descriptionOrFallback(field, opType),
		Parameters:  argsToParams(schema, field.Args),
		Query:       buildOperation(schema, opType, field, opts),
		ResultPath:  "data." + field.Name,
	}
	return tc
}

func descriptionOrFallback(field Field, opType string) string {
	if d := strings.TrimSpace(field.Description); d != "" {
		return d
	}
	return fmt.Sprintf("%s the %s GraphQL %s.", strings.Title(opType[:1])+opType[1:], field.Name, opType) //nolint:staticcheck
}

// maxInputObjectDepth caps recursion when expanding INPUT_OBJECT inputFields
// into nested ParamConfig.Properties. Real-world GraphQL inputs rarely nest
// past 2-3 levels; this limit is mostly defense against self-referential
// types (e.g. tree-shaped filters that contain themselves).
const maxInputObjectDepth = 5

// argsToParams converts GraphQL arguments to gollm's ParamConfig. INPUT_OBJECT
// args expand into nested Properties; LIST args expand into Items; ENUM args
// carry their values as EnumValues. The result is a JSON-Schema-shaped tree
// that ToProviderTool can render directly for the LLM tool spec — no
// stringified-input hack, no "pass JSON for complex parameters" prose.
func argsToParams(schema *Schema, args []InputValue) []config.ParamConfig {
	params := make([]config.ParamConfig, 0, len(args))
	for _, arg := range args {
		params = append(params, gqlInputToParam(schema, arg.Name, arg.Description, arg.Type, 0))
	}
	return params
}

// gqlInputToParam recursively maps a GraphQL TypeRef to a ParamConfig. The
// outermost NON_NULL determines Required; all inner NON_NULLs are unwrapped
// to find the underlying kind (SCALAR/ENUM/LIST/INPUT_OBJECT).
func gqlInputToParam(schema *Schema, name, desc string, t *TypeRef, depth int) config.ParamConfig {
	p := config.ParamConfig{
		Name:        name,
		Description: strings.TrimSpace(desc),
		Type:        "string",
	}
	// Malformed introspection (missing TypeRef, or NON_NULL/LIST without
	// OfType) falls back to a plain string param rather than panicking.
	if t == nil {
		return p
	}
	p.Required = t.IsRequired()
	inner := t
	if inner.Kind == "NON_NULL" {
		if inner.OfType == nil {
			return p
		}
		inner = inner.OfType
	}

	switch inner.Kind {
	case "LIST":
		p.Type = "array"
		if inner.OfType == nil {
			break
		}
		// The element type may itself be NON_NULL; recurse normally and
		// let the called function unwrap.
		elem := gqlInputToParam(schema, "", "", inner.OfType, depth+1)
		// Items don't need a name field in JSON Schema and Required only
		// makes sense at parent boundaries — strip both.
		elem.Name = ""
		elem.Required = false
		p.Items = &elem
	case "ENUM":
		p.Type = "string"
		if def := schema.TypeByName(inner.Name); def != nil {
			for _, v := range def.EnumValues {
				p.EnumValues = append(p.EnumValues, v.Name)
			}
		}
	case "INPUT_OBJECT":
		p.Type = "object"
		if depth >= maxInputObjectDepth {
			// Recursion truncated. Without a hint the LLM sees a bare
			// {"type": "object"} with no shape info; surface the type
			// name so it has something to reason about.
			if p.Description == "" && inner.Name != "" {
				p.Description = fmt.Sprintf("%s (nested structure, recursion truncated)", inner.Name)
			}
			break
		}
		def := schema.TypeByName(inner.Name)
		if def == nil {
			break
		}
		for _, f := range def.InputFields {
			p.Properties = append(p.Properties,
				gqlInputToParam(schema, f.Name, f.Description, f.Type, depth+1))
		}
	case "SCALAR":
		switch inner.Name {
		case "Int":
			p.Type = "integer"
		case "Float":
			p.Type = "number"
		case "Boolean":
			p.Type = "boolean"
		default:
			p.Type = "string"
		}
	default:
		p.Type = "string"
	}
	return p
}

// buildOperation renders the full GraphQL operation string, including the
// header (variable declarations) and the body (field call + selection set).
func buildOperation(schema *Schema, opType string, field Field, opts Options) string {
	var b strings.Builder

	header := operationHeader(opType, field)
	b.WriteString(header)
	b.WriteString(" {\n  ")
	b.WriteString(field.Name)
	if len(field.Args) > 0 {
		b.WriteString("(")
		for i, arg := range field.Args {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(arg.Name)
			b.WriteString(": $")
			b.WriteString(arg.Name)
		}
		b.WriteString(")")
	}

	selection := renderSelection(schema, field.Type, 1, opts.MaxSelectionDepth)
	if selection != "" {
		b.WriteString(" ")
		b.WriteString(selection)
	}
	b.WriteString("\n}\n")
	return b.String()
}

func operationHeader(opType string, field Field) string {
	name := strings.Title(opType[:1]) + opType[1:] + "_" + field.Name //nolint:staticcheck
	if len(field.Args) == 0 {
		return opType + " " + name
	}
	var parts []string
	for _, arg := range field.Args {
		parts = append(parts, "$"+arg.Name+": "+arg.Type.GraphQLString())
	}
	return fmt.Sprintf("%s %s(%s)", opType, name, strings.Join(parts, ", "))
}

// renderSelection produces a `{ field1 field2 nested { sub1 sub2 } }`
// selection set for the given return type. It expands object subfields up
// to maxDepth layers; beyond that, nested objects are dropped (the LLM
// won't see them in responses, but the call still succeeds — vs. asking
// for fields that don't exist, which fails the whole operation).
func renderSelection(schema *Schema, t *TypeRef, depth, maxDepth int) string {
	u := t.Unwrap()
	if u == nil || u.Kind == "SCALAR" || u.Kind == "ENUM" {
		return "" // leaf — no selection set needed
	}
	def := schema.TypeByName(u.Name)
	if def == nil || len(def.Fields) == 0 {
		return ""
	}

	var parts []string
	for _, f := range def.Fields {
		// Skip fields that take args at the leaf level — invoking them would
		// require choosing argument values, which auto-generation can't do.
		if len(f.Args) > 0 {
			continue
		}
		if f.Type.IsScalarOrEnum() {
			parts = append(parts, f.Name)
			continue
		}
		if depth >= maxDepth {
			continue
		}
		sub := renderSelection(schema, f.Type, depth+1, maxDepth)
		if sub != "" {
			parts = append(parts, f.Name+" "+sub)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return "{ " + strings.Join(parts, " ") + " }"
}

// matchedSet returns a map keyed by items, with values starting at false
// (not yet matched). shouldSkip flips them to true when used.
func matchedSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = false
	}
	return m
}
