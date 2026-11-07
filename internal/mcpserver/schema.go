package mcpserver

import (
	apidocs "agentflow/docs/api"
	"agentflow/internal/application"
	"encoding/json"
	"fmt"
	"github.com/goccy/go-yaml"
	"math"
	"strings"
	"sync"
)

var contractOnce sync.Once
var contract map[string]any

func contractObject(v any) map[string]any { m, _ := v.(map[string]any); return m }
func resolveContract(v any, depth int) any {
	if depth > 12 {
		return map[string]any{}
	}
	switch value := v.(type) {
	case map[string]any:
		if ref, ok := value["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			var target any = contract
			for _, key := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				target = contractObject(target)[key]
			}
			return resolveContract(target, depth+1)
		}
		out := map[string]any{}
		for key, item := range value {
			out[key] = resolveContract(item, depth)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = resolveContract(item, depth)
		}
		return out
	default:
		return v
	}
}

// ToolInputSchema uses the HTTP contract for both public and session MCP tools.
func ToolInputSchema(op application.Operation) map[string]any {
	contractOnce.Do(func() { _ = yaml.Unmarshal(apidocs.OpenAPI, &contract) })
	schema := map[string]any{"type": "object", "additionalProperties": false}
	properties := map[string]any{}
	required := []string{}
	names := pathParameterNames(op.Path)
	if len(names) > 0 {
		fields := map[string]any{}
		for _, name := range names {
			fields[name] = map[string]any{"type": "string", "minLength": 1}
		}
		properties["path"] = map[string]any{"type": "object", "properties": fields, "required": names, "additionalProperties": false}
		required = append(required, "path")
	}
	properties["query"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	// OpenAPI sometimes names a path parameter issueId where the manifest uses id.
	paths := contractObject(contract["paths"])
	var method map[string]any
	var route map[string]any
	for path, value := range paths {
		if normalizeContractPath(path) == normalizeContractPath(op.Path) {
			route = contractObject(value)
			method = contractObject(route[strings.ToLower(op.Method)])
			break
		}
	}
	queryFields := map[string]any{}
	queryRequired := []string{}
	routeParameters, _ := route["parameters"].([]any)
	methodParameters, _ := method["parameters"].([]any)
	for _, raw := range append(routeParameters, methodParameters...) {
		param := contractObject(resolveContract(raw, 0))
		if param["in"] != "query" {
			continue
		}
		name, _ := param["name"].(string)
		if name == "" {
			continue
		}
		field := map[string]any{"type": "string"}
		if description, ok := param["description"].(string); ok {
			field["description"] = description
		}
		queryFields[name] = field
		if needed, _ := param["required"].(bool); needed {
			queryRequired = append(queryRequired, name)
		}
	}
	if len(queryFields) > 0 {
		query := contractObject(properties["query"])
		query["properties"] = queryFields
		if len(queryRequired) > 0 {
			query["required"] = queryRequired
			required = append(required, "query")
		}
	}
	if op.Method == "POST" || op.Method == "PUT" {
		body := contractObject(resolveContract(method["requestBody"], 0))
		content := contractObject(body["content"])
		jsonBody := contractObject(content["application/json"])
		if definition := contractObject(jsonBody["schema"]); len(definition) > 0 {
			properties["body"] = definition
		} else {
			properties["body"] = map[string]any{"description": "JSON request body; read openapi_get for the endpoint contract"}
		}
		if needed, _ := body["required"].(bool); needed {
			required = append(required, "body")
		}
	}
	schema["properties"] = properties
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
func normalizeContractPath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "{") {
			parts[i] = "{}"
		}
	}
	return strings.Join(parts, "/")
}

// ValidateToolInput rejects missing contract fields before a draft is accepted.
func ValidateToolInput(op application.Operation, input map[string]any) error {
	return validateInputValue(ToolInputSchema(op), input, "arguments")
}
func validateInputValue(schema map[string]any, value any, path string) error {
	if schema["type"] == nil && (schema["required"] != nil || schema["properties"] != nil) {
		schema["type"] = "object"
	}
	for _, kind := range []string{"allOf", "anyOf", "oneOf"} {
		choices, _ := schema[kind].([]any)
		if len(choices) == 0 {
			continue
		}
		matches := 0
		for _, choice := range choices {
			if err := validateInputValue(contractObject(choice), value, path); err == nil {
				matches++
			} else if kind == "allOf" {
				return err
			}
		}
		if (kind != "allOf" && matches == 0) || (kind == "oneOf" && matches != 1) {
			return fmt.Errorf("%s must satisfy one of the documented input shapes", path)
		}
	}
	switch schema["type"] {
	case "object":
		fields, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		required := []string{}
		switch names := schema["required"].(type) {
		case []string:
			required = names
		case []any:
			for _, name := range names {
				if n, ok := name.(string); ok {
					required = append(required, n)
				}
			}
		}
		for _, name := range required {
			v, exists := fields[name]
			if !exists || v == nil || (func() bool { v, ok := v.(string); return ok && strings.TrimSpace(v) == "" })() {
				return fmt.Errorf("%s.%s is required", path, name)
			}
		}
		for name, definition := range contractObject(schema["properties"]) {
			if v, ok := fields[name]; ok && v != nil {
				if err := validateInputValue(contractObject(definition), v, path+"."+name); err != nil {
					return err
				}
			}
		}
	case "array":
		if items, ok := value.([]any); ok {
			for i, item := range items {
				if err := validateInputValue(contractObject(schema["items"]), item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		} else {
			return fmt.Errorf("%s must be an array", path)
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	case "integer", "number":
		var number float64
		switch n := value.(type) {
		case float64:
			number = n
		case json.Number:
			parsed, err := n.Float64()
			if err != nil {
				return fmt.Errorf("%s must be numeric", path)
			}
			number = parsed
		default:
			return fmt.Errorf("%s must be numeric", path)
		}
		if schema["type"] == "integer" && math.Trunc(number) != number {
			return fmt.Errorf("%s must be an integer", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	}
	if values, ok := schema["enum"].([]any); ok {
		raw, _ := json.Marshal(value)
		matched := false
		for _, allowed := range values {
			b, _ := json.Marshal(allowed)
			if string(raw) == string(b) {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("%s has an undocumented value", path)
		}
	}
	if forbidden, ok := schema["additionalProperties"].(bool); ok && !forbidden {
		fields, _ := value.(map[string]any)
		props := contractObject(schema["properties"])
		for key := range fields {
			if _, ok := props[key]; !ok {
				return fmt.Errorf("%s.%s is not a documented parameter; put HTTP payload fields in body", path, key)
			}
		}
	}

	return nil
}
