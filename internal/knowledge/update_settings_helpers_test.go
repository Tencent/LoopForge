package knowledge

import (
	"reflect"
	"testing"
)

func TestCleanProjectSources(t *testing.T) {
	if got := cleanProjectSources(nil); got != nil {
		t.Fatalf("cleanProjectSources(nil) = %#v, want nil", got)
	}
	if got := cleanProjectSources(map[string][]string{}); got == nil || len(got) != 0 {
		t.Fatalf("cleanProjectSources(empty) = %#v, want empty non-nil map", got)
	}
	got := cleanProjectSources(map[string][]string{
		" p1 ": {" a ", "a", ""},
		" ":    {"x"},
		"p2":   nil,
	})
	want := map[string][]string{"p1": {"a"}, "p2": {}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanProjectSources = %#v, want %#v", got, want)
	}
}

func TestResolveSettingsToken(t *testing.T) {
	cases := []struct {
		name  string
		input SettingsInput
		want  string
	}{
		{"new token", SettingsInput{Token: " new "}, "new"},
		{"new bearer token", SettingsInput{Token: "Bearer new"}, "new"},
		{"new token wins over clear", SettingsInput{Token: "new", ClearToken: true}, "new"},
		{"blank keeps current", SettingsInput{Token: "  "}, "old"},
		{"clear drops current", SettingsInput{ClearToken: true}, ""},
	}
	for _, tc := range cases {
		if got := resolveSettingsToken(tc.input, "old"); got != tc.want {
			t.Errorf("%s: resolveSettingsToken = %q, want %q", tc.name, got, tc.want)
		}
	}
}
