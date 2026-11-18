package domain

import "testing"

func TestNormalizeGeminiBaseURL(t *testing.T) {
	for raw, want := range map[string]string{
		"":                                  "",
		" https://example.test/v1beta/ ":    "https://example.test",
		"https://example.test/proxy/v1beta": "https://example.test/proxy",
		"https://example.test/proxy/":       "https://example.test/proxy",
		"http://localhost:8080":             "http://localhost:8080",
	} {
		got, err := NormalizeGeminiBaseURL(raw)
		if err != nil || got != want {
			t.Errorf("NormalizeGeminiBaseURL(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"https://example.test/v1beta/interactions", "https://example.test/v1beta/models/model", "https://example.test?key=fixture", "https://user:fixture@example.test", "file:///tmp/models"} {
		if _, err := NormalizeGeminiBaseURL(raw); err == nil {
			t.Errorf("accepted invalid service root %q", raw)
		}
	}
}
