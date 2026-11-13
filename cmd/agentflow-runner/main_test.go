package main

import (
	"reflect"
	"testing"
)

func TestConcurrencyFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 3},
		{"   ", 3},
		{"5", 5},
		{" 7 ", 7},
		{"0", 3},
		{"-2", 3},
		{"abc", 3},
	}
	for _, tc := range cases {
		t.Setenv("AGENTFLOW_RUNNER_CONCURRENCY", tc.env)
		if got := concurrencyFromEnv(3); got != tc.want {
			t.Errorf("concurrencyFromEnv(3) with env %q = %d, want %d", tc.env, got, tc.want)
		}
	}
}

func TestParseLabels(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{" , ,", nil},
		{"gpu", []string{"gpu"}},
		{" gpu , linux,,arm64 ", []string{"gpu", "linux", "arm64"}},
	}
	for _, tc := range cases {
		if got := parseLabels(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseLabels(%q) = %#v, want %#v", tc.raw, got, tc.want)
		}
	}
}
