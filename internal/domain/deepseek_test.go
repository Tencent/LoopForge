package domain

import "testing"

func TestBinaryProvider(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{provider: ExecutorKindDeepSeek, want: "codex"},
		{provider: " DeepSeek ", want: "codex"},
		{provider: "codex", want: "codex"},
		{provider: "claude-code", want: "claude-code"},
		{provider: " dsh ", want: "dsh"},
		{provider: "", want: ""},
	}
	for _, tc := range cases {
		if got := BinaryProvider(tc.provider); got != tc.want {
			t.Errorf("BinaryProvider(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}
