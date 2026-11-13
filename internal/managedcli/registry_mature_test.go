package managedcli

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMatureVersionSelectsNewestEligibleRelease(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := now.Add(-observationWindow)
	build := func(entries map[string]time.Duration, deprecated ...string) packageMetadata {
		metadata := packageMetadata{Versions: map[string]release{}, Times: map[string]json.RawMessage{}}
		for version, age := range entries {
			metadata.Versions[version] = release{Version: version}
			metadata.Times[version], _ = json.Marshal(now.Add(-age))
		}
		for _, version := range deprecated {
			entry := metadata.Versions[version]
			entry.Deprecated = "do not use"
			metadata.Versions[version] = entry
		}
		return metadata
	}
	cases := []struct {
		name     string
		metadata packageMetadata
		upper    string
		want     string
	}{
		{"picks newest mature", build(map[string]time.Duration{"1.0.0": 100 * time.Hour, "1.1.0": 36 * time.Hour, "1.2.0": time.Hour}), "1.2.0", "1.1.0"},
		{"bounded by upper", build(map[string]time.Duration{"1.1.0": 36 * time.Hour, "2.0.0": 72 * time.Hour}), "1.2.0", "1.1.0"},
		{"skips prerelease", build(map[string]time.Duration{"1.0.0": 72 * time.Hour, "1.1.0-beta.1": 72 * time.Hour}), "1.2.0", "1.0.0"},
		{"skips deprecated", build(map[string]time.Duration{"1.0.0": 72 * time.Hour, "1.1.0": 72 * time.Hour}, "1.1.0"), "1.2.0", "1.0.0"},
		{"none mature", build(map[string]time.Duration{"1.2.0": time.Hour}), "1.2.0", ""},
		{"upper itself when mature", build(map[string]time.Duration{"1.2.0": 48 * time.Hour}), "1.2.0", "1.2.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matureVersion(tc.metadata, tc.upper, cutoff); got != tc.want {
				t.Fatalf("matureVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMatureVersionIgnoresMissingOrInvalidPublishTime(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	metadata := packageMetadata{
		Versions: map[string]release{"1.0.0": {Version: "1.0.0"}, "1.1.0": {Version: "1.1.0"}, "1.2.0": {Version: "1.2.0"}},
		Times:    map[string]json.RawMessage{"1.1.0": json.RawMessage(`"not-a-time"`), "1.2.0": json.RawMessage(`"0001-01-01T00:00:00Z"`)},
	}
	if got := matureVersion(metadata, "1.2.0", cutoff); got != "" {
		t.Fatalf("matureVersion = %q, want empty", got)
	}
}
