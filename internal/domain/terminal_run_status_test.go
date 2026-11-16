package domain

import "testing"

// TestTerminalRunStatus pins the terminal-status set shared by engine,
// httpapi (rooms and space lanes) and notification callers.
func TestTerminalRunStatus(t *testing.T) {
	cases := map[string]bool{
		"succeeded":  true,
		"failed":     true,
		"cancelled":  true,
		" failed\n":  true,
		"":           false,
		"queued":     false,
		"running":    false,
		"waiting":    false,
		"pending":    false,
		"Succeeded":  false,
		"cancelling": false,
	}
	for status, want := range cases {
		if got := TerminalRunStatus(status); got != want {
			t.Errorf("TerminalRunStatus(%q) = %v, want %v", status, got, want)
		}
	}
}
