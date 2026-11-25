package preview

import "testing"

func TestPositiveIntFromEnvHelpers(t *testing.T) {
	const key = "AGENTFLOW_PREVIEW_TEST_ENV_INT"
	cases := []struct {
		name    string
		raw     string
		wantInt int
		want64  int64
	}{
		{name: "unset-empty", raw: "", wantInt: 7, want64: 8},
		{name: "blank", raw: "   ", wantInt: 7, want64: 8},
		{name: "valid", raw: "42", wantInt: 42, want64: 42},
		{name: "trimmed", raw: " 13 ", wantInt: 13, want64: 13},
		{name: "zero", raw: "0", wantInt: 7, want64: 8},
		{name: "negative", raw: "-5", wantInt: 7, want64: 8},
		{name: "malformed", raw: "12abc", wantInt: 7, want64: 8},
		{name: "float", raw: "1.5", wantInt: 7, want64: 8},
		{name: "large-int64", raw: "9999999999", wantInt: 9999999999, want64: 9999999999},
		{name: "overflow-int64", raw: "99999999999999999999", wantInt: 7, want64: 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, tc.raw)
			if got := intFromEnv(key, 7); got != tc.wantInt {
				t.Fatalf("intFromEnv(%q) = %d, want %d", tc.raw, got, tc.wantInt)
			}
			if got := int64FromEnv(key, 8); got != tc.want64 {
				t.Fatalf("int64FromEnv(%q) = %d, want %d", tc.raw, got, tc.want64)
			}
		})
	}
}

func TestPositiveIntFromEnvRespectsBitSize(t *testing.T) {
	const key = "AGENTFLOW_PREVIEW_TEST_ENV_BITS"
	t.Setenv(key, "70000")
	if got := positiveIntFromEnv(key, 3, 16); got != 3 {
		t.Fatalf("16-bit overflow should fall back, got %d", got)
	}
	if got := positiveIntFromEnv(key, 3, 32); got != 70000 {
		t.Fatalf("32-bit value = %d, want 70000", got)
	}
}
