package mcpserver

import (
	"encoding/json"
	"testing"
)

// TestValidateInputValueErrorMessages pins the exact error text and the first
// failing path for each validation stage so splitting validateInputValue into
// helpers cannot change observable behaviour.
func TestValidateInputValueErrorMessages(t *testing.T) {
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "number"}
	cases := []struct {
		name   string
		schema map[string]any
		value  any
		want   string
	}{
		{"object type", map[string]any{"type": "object"}, "x", "v must be an object"},
		{"implicit object", map[string]any{"properties": map[string]any{}}, 1, "v must be an object"},
		{"required []string missing", map[string]any{"required": []string{"a"}}, map[string]any{}, "v.a is required"},
		{"required []any null", map[string]any{"required": []any{"a", 3}}, map[string]any{"a": nil}, "v.a is required"},
		{"required blank string", map[string]any{"required": []any{"a"}}, map[string]any{"a": "  "}, "v.a is required"},
		{"nested property", map[string]any{"properties": map[string]any{"a": str}}, map[string]any{"a": 1}, "v.a must be a string"},
		{"array items", map[string]any{"type": "array", "items": str}, []any{"x", 2}, "v[1] must be a string"},
		{"array type", map[string]any{"type": "array"}, map[string]any{}, "v must be an array"},
		{"numeric type", num, "1", "v must be numeric"},
		{"integer", map[string]any{"type": "integer"}, float64(1.5), "v must be an integer"},
		{"boolean", map[string]any{"type": "boolean"}, 0, "v must be a boolean"},
		{"enum", map[string]any{"enum": []any{float64(1), "a"}}, "b", "v has an undocumented value"},
		{"empty enum", map[string]any{"enum": []any{}}, "a", "v has an undocumented value"},
		{"anyOf none", map[string]any{"anyOf": []any{str, num}}, true, "v must satisfy one of the documented input shapes"},
		{"oneOf many", map[string]any{"oneOf": []any{str, map[string]any{}}}, "x", "v must satisfy one of the documented input shapes"},
		{"allOf first error", map[string]any{"allOf": []any{map[string]any{}, num}}, "x", "v must be numeric"},
		{"additional property", map[string]any{"properties": map[string]any{"a": str}, "additionalProperties": false}, map[string]any{"a": "x", "b": 1}, "v.b is not a documented parameter; put HTTP payload fields in body"},
	}
	for _, tc := range cases {
		err := validateInputValue(tc.schema, tc.value, "v")
		if err == nil || err.Error() != tc.want {
			t.Fatalf("%s: err=%v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestValidateInputValueAccepts(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		value  any
	}{
		{"untyped", map[string]any{}, struct{}{}},
		{"enum number json", map[string]any{"type": "integer", "enum": []any{float64(2)}}, json.Number("2")},
		{"required present", map[string]any{"required": []string{"a"}, "properties": map[string]any{"b": map[string]any{"type": "string"}}}, map[string]any{"a": 0, "b": nil}},
		{"additional allowed", map[string]any{"properties": map[string]any{}, "additionalProperties": true}, map[string]any{"x": 1}},
		{"anyOf second", map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "boolean"}}}, true},
	}
	for _, tc := range cases {
		if err := validateInputValue(tc.schema, tc.value, "v"); err != nil {
			t.Fatalf("%s: unexpected err %v", tc.name, err)
		}
	}
}
