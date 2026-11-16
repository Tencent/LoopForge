package managedcli

import (
	"testing"
	"time"
)

func TestReleaseGate(t *testing.T) {
	now := time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC)
	m := &Manager{Now: func() time.Time { return now }}
	old := now.Add(-2 * observationWindow)
	state := &State{Rejected: map[string]string{"codex": "3.0.0"}}
	for _, tc := range []struct {
		name, provider, from, version string
		published                     time.Time
		wantStatus, wantMessage       string
	}{
		{"same version", "codex", "2.0.0", "2.0.0", old, "unchanged", ""},
		{"downgrade", "codex", "2.0.0", "1.9.0", old, "skipped", "automatic downgrade refused"},
		{"rolled back", "codex", "2.0.0", "3.0.0", old, "skipped", "this release was rolled back; waiting for a newer release"},
		{"rejected only for its provider", "claude", "2.0.0", "3.0.0", old, "", ""},
		{"observation window", "codex", "2.0.0", "2.1.0", now.Add(-time.Hour), "skipped", "release is still in the observation window"},
		{"eligible upgrade", "codex", "2.0.0", "2.1.0", old, "", ""},
		{"first install", "codex", "", "2.1.0", old, "", ""},
		{"first install in observation window", "codex", "", "2.1.0", now, "skipped", "release is still in the observation window"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, message := m.releaseGate(state, tc.provider, tc.from, release{Version: tc.version, Published: tc.published})
			if status != tc.wantStatus || message != tc.wantMessage {
				t.Fatalf("releaseGate() = (%q, %q), want (%q, %q)", status, message, tc.wantStatus, tc.wantMessage)
			}
		})
	}
}
