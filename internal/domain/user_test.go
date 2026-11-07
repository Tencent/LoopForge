package domain

import "testing"

func TestNormalizeCodeBuddyInternetEnvironment(t *testing.T) {
	for raw, want := range map[string]string{
		"":              "",
		"  ":            "",
		"public":        "",
		"INTL":          "",
		"international": "",
		"国际站":           "",
		"internal":      "internal",
		" China ":       "internal",
		"cn":            "internal",
		"国内站":           "internal",
		"ioa":           "ioa",
		"iOA":           "ioa",
	} {
		if got, ok := NormalizeCodeBuddyInternetEnvironment(raw); !ok || got != want {
			t.Fatalf("NormalizeCodeBuddyInternetEnvironment(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	// The CLI would silently use the international edition for these.
	for _, raw := range []string{"intranet", "selfhosted", "cloudhosted", "国内", "io a"} {
		if got, ok := NormalizeCodeBuddyInternetEnvironment(raw); ok || got != "" {
			t.Fatalf("NormalizeCodeBuddyInternetEnvironment(%q) = %q, %v; want rejection", raw, got, ok)
		}
	}
}
