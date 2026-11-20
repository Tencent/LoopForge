package engine

import (
	"testing"
	"time"
)

func TestIdleTTLFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", time.Hour},
		{"6h", 6 * time.Hour},
		{"30m", 30 * time.Minute},
		{" 90m ", 90 * time.Minute},
		{"nonsense", time.Hour},
		{"0s", time.Hour},
		{"-1h", time.Hour},
	}
	for _, tc := range cases {
		t.Setenv("AGENTFLOW_WORKSPACE_IDLE_TTL", tc.raw)
		if got := idleTTLFromEnv(); got != tc.want {
			t.Errorf("idleTTLFromEnv(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
