package domain

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRunnerSemanticVersionCompatibility(t *testing.T) {
	data, err := os.ReadFile("testdata/runner-version-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Have, Want string
		OK         bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Have+"/"+tc.Want, func(t *testing.T) {
			if got := versionAtLeast(tc.Have, tc.Want); got != tc.OK {
				t.Errorf("versionAtLeast(%q,%q)=%v, want %v", tc.Have, tc.Want, got, tc.OK)
			}
		})
	}
}

func TestRunnerDefaultSelectionDistinguishesUnknownVersion(t *testing.T) {
	for _, version := range []string{"", "dev", "0.8.0+bad!"} {
		got := (Runner{Status: RunnerStatusOnline, RunnerVersion: version}).DefaultSelectionBlocker()
		if !strings.Contains(got, "无法判断") || strings.Contains(got, "过低") {
			t.Errorf("%q: %s", version, got)
		}
	}
	if got := (Runner{Status: RunnerStatusOnline, RunnerVersion: "0.8.0+internal"}).DefaultSelectionBlocker(); got != "" {
		t.Errorf("build metadata rejected: %s", got)
	}
}
