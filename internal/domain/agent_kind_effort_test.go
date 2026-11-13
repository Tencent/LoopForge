package domain

import "testing"

func TestIsAgentCLIKindMatchesCaseInsensitivelyAfterTrim(t *testing.T) {
	cases := map[string]bool{
		"opencode":    true,
		"  Cursor  ":  true,
		"ZCODE":       true,
		"pi":          true,
		"codex":       false,
		"claude-code": false,
		"codebuddy":   false,
		"":            false,
		"open code":   false,
	}
	for kind, want := range cases {
		if got := IsAgentCLIKind(kind); got != want {
			t.Errorf("IsAgentCLIKind(%q) = %v, want %v", kind, got, want)
		}
	}
}

func TestValidateAgentEffortChecksPublishedLevels(t *testing.T) {
	cases := []struct {
		effort, kind, wantErr string
	}{
		{effort: "", kind: "unknown"},
		{effort: "   ", kind: "codex"},
		{effort: "ultra", kind: "codex"},
		{effort: " max ", kind: "claude-code"},
		{effort: "minimal", kind: " pi "},
		{effort: "high", kind: "kimi", wantErr: "当前 CLI 不支持设置思考强度"},
		{effort: "high", kind: "unknown", wantErr: "当前 CLI 不支持设置思考强度"},
		{effort: "ultracode", kind: "codex", wantErr: "思考强度取值不被当前 CLI 支持"},
		{effort: "HIGH", kind: "codex", wantErr: "思考强度取值不被当前 CLI 支持"},
		{effort: "xhigh", kind: "opencode", wantErr: "思考强度取值不被当前 CLI 支持"},
	}
	for _, tc := range cases {
		err := ValidateAgentEffort(tc.effort, tc.kind)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("ValidateAgentEffort(%q, %q) = %v, want nil", tc.effort, tc.kind, err)
			}
			continue
		}
		if err == nil || err.Error() != tc.wantErr {
			t.Errorf("ValidateAgentEffort(%q, %q) = %v, want %q", tc.effort, tc.kind, err, tc.wantErr)
		}
	}
}
