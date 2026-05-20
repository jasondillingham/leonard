package parse

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jasondillingham/leonard/internal/store"
)

// openapiHTTPMethods is the set of HTTP method keys an OpenAPI
// "Path Item" object can contain. Anything outside this set under
// `paths.<path>.` is metadata (parameters, summary, etc.) — not
// an operation.
var openapiHTTPMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true,
	"delete": true, "head": true, "options": true, "trace": true,
}

// ExtractOpenAPI inspects a Swagger 2.0 / OpenAPI 3.x file (JSON
// or YAML) and emits symbols for operations and schemas. The
// "operation" symbol name is "GET /users/{id}"-shaped — useful
// for verify_symbol when Claude wants to confirm a path exists.
// Schemas (under components.schemas or definitions) become types.
//
// Line numbers stay at 1 — OpenAPI specs are structured documents,
// not source files; per-element line tracking would require a
// position-aware YAML/JSON walker. Refinement for a later version
// if find_symbol's `:line` field starts driving real workflows.
func ExtractOpenAPI(path string, src []byte) ([]store.Symbol, error) {
	doc, err := parseStructured(src)
	if err != nil {
		return nil, fmt.Errorf("openapi: parse %s: %w", path, err)
	}
	if doc == nil {
		return nil, nil
	}
	module := moduleQualifier(path)
	var out []store.Symbol

	// Walk paths: doc["paths"] is a map[path]Path-Item where each
	// Path-Item has HTTP-method keys whose values are operations.
	if paths, ok := doc["paths"].(map[string]any); ok {
		// Sort path keys so the symbol-emission order is stable.
		pathNames := make([]string, 0, len(paths))
		for p := range paths {
			pathNames = append(pathNames, p)
		}
		sort.Strings(pathNames)
		for _, pathName := range pathNames {
			item, ok := paths[pathName].(map[string]any)
			if !ok {
				continue
			}
			methodNames := make([]string, 0, len(item))
			for k := range item {
				if openapiHTTPMethods[strings.ToLower(k)] {
					methodNames = append(methodNames, k)
				}
			}
			sort.Strings(methodNames)
			for _, m := range methodNames {
				opName := strings.ToUpper(m) + " " + pathName
				out = append(out, store.Symbol{
					FilePath:      path,
					Name:          opName,
					QualifiedName: module + "." + opName,
					Kind:          "method",
					Signature:     "endpoint " + opName,
					StartLine:     1,
					EndLine:       1,
					Exported:      true,
				})
			}
		}
	}

	// Walk schemas: OpenAPI 3 has them at components.schemas; Swagger 2
	// uses top-level `definitions`. Try both.
	var schemas map[string]any
	if comps, ok := doc["components"].(map[string]any); ok {
		if s, ok := comps["schemas"].(map[string]any); ok {
			schemas = s
		}
	}
	if schemas == nil {
		if s, ok := doc["definitions"].(map[string]any); ok {
			schemas = s
		}
	}
	if schemas != nil {
		schemaNames := make([]string, 0, len(schemas))
		for n := range schemas {
			schemaNames = append(schemaNames, n)
		}
		sort.Strings(schemaNames)
		for _, n := range schemaNames {
			out = append(out, store.Symbol{
				FilePath:      path,
				Name:          n,
				QualifiedName: module + "." + n,
				Kind:          "type",
				Signature:     "schema " + n,
				StartLine:     1,
				EndLine:       1,
				Exported:      true,
			})
		}
	}

	return out, nil
}

// parseStructured tries JSON first (cheap and strict), falls back
// to YAML. Both are accepted because real-world API specs ship in
// either format.
func parseStructured(src []byte) (map[string]any, error) {
	var jsonDoc map[string]any
	if err := json.Unmarshal(src, &jsonDoc); err == nil {
		return jsonDoc, nil
	}
	var yamlDoc map[string]any
	if err := yaml.Unmarshal(src, &yamlDoc); err != nil {
		return nil, err
	}
	return normalizeYAMLMap(yamlDoc), nil
}

// normalizeYAMLMap converts yaml.v3's map[any]any maps (the form
// it emits when keys aren't all strings) into map[string]any so
// the rest of ExtractOpenAPI can use uniform access. Map values
// that are themselves maps get recursively normalized. Non-string
// keys are dropped — OpenAPI keys are all strings by spec.
func normalizeYAMLMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = normalizeYAMLValue(v)
	}
	return out
}

func normalizeYAMLValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return normalizeYAMLMap(x)
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, vv := range x {
			if ks, ok := k.(string); ok {
				m[ks] = normalizeYAMLValue(vv)
			}
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeYAMLValue(e)
		}
		return out
	default:
		return v
	}
}
