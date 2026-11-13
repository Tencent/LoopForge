package runtime

import (
	"reflect"
	"testing"
)

func TestDropArgWithValueRemovesEveryFlagAndForm(t *testing.T) {
	in := []string{"cli", "--resume", "s1", "-p", "--tools=Read", "--model", "m", "--tools", "Bash", "--resume=s2", "prompt"}
	got := dropArgWithValue(in, "--resume", "--tools")
	want := []string{"cli", "-p", "--model", "m", "prompt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dropArgWithValue = %v, want %v", got, want)
	}
	if single := dropArgWithValue([]string{"cli", "--resume", "s1", "-p"}, "--resume"); !reflect.DeepEqual(single, []string{"cli", "-p"}) {
		t.Fatalf("single flag = %v", single)
	}
	if len(in) != 11 || in[1] != "--resume" {
		t.Fatalf("input mutated: %v", in)
	}
}

func TestIsolateCodeBuddyFlagsReplacesAllIsolationFlags(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "binary head with separate and equals forms",
			in:   []string{"codebuddy", "-p", "--plugin-dir", "/x", "--setting-sources=project", "--permission-mode=dontAsk", "--output-format", "stream-json"},
			want: []string{"codebuddy", "-p", "--output-format", "stream-json", "-y", "--permission-mode", "bypassPermissions", "--setting-sources", "none"},
		},
		{
			name: "no binary head keeps existing skip-permissions flag",
			in:   []string{"--dangerously-skip-permissions", "--permission-mode", "plan", "-p"},
			want: []string{"--dangerously-skip-permissions", "-p", "--permission-mode", "bypassPermissions", "--setting-sources", "none"},
		},
		{
			name: "empty",
			in:   nil,
			want: []string{"-y", "--permission-mode", "bypassPermissions", "--setting-sources", "none"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isolateCodeBuddyFlagsWithSource(tc.in, "none"); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
