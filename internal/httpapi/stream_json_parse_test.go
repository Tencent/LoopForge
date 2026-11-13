package httpapi

import (
	"reflect"
	"testing"
)

func TestStripJSONFence(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:          `{"a":1}`,
		"  padded  ":       "padded",
		"```json\n{}\n```": "{}",
		"```\n{}\n```":     "{}",
		" ```json {} ``` ": "{}",
		"":                 "",
		"no fence ```":     "no fence",
	}
	for in, want := range cases {
		if got := stripJSONFence(in); got != want {
			t.Errorf("stripJSONFence(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseStreamJSONObject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want any
		ok   bool
	}{
		{"plain object", `{"ready":true}`, map[string]any{"ready": true}, true},
		{"fenced object", "```json\n{\"ready\":false}\n```", map[string]any{"ready": false}, true},
		{"surrounding prose", `结论如下 {"summary":"s"} 以上`, map[string]any{"summary": "s"}, true},
		{"result envelope", `{"type":"result","result":"{\"summary\":\"inner\"}"}`, map[string]any{"summary": "inner"}, true},
		{"no object", "just text", nil, false},
		{"reversed braces", "} {", nil, false},
		{"invalid json", `{"a":}`, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseStreamJSONObject(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (value %#v)", ok, tc.ok, got)
			}
			if tc.ok && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}
