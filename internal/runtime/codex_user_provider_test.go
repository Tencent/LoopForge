package runtime

import (
	"slices"
	"testing"
)

func TestCodexUserProviderArgvInsertsProviderBeforePrompt(t *testing.T) {
	providerArgs := func(baseURL string) []string {
		return []string{
			"-c", `model_provider="agentflow_user"`,
			"-c", `model_providers.agentflow_user.name="User API"`,
			"-c", `model_providers.agentflow_user.base_url="` + baseURL + `"`,
			"-c", `model_providers.agentflow_user.env_key="CODEX_API_KEY"`,
			"-c", `model_providers.agentflow_user.wire_api="responses"`,
			"-c", "model_providers.agentflow_user.requires_openai_auth=false",
		}
	}
	cases := []struct {
		name    string
		baseURL string
		wantURL string
	}{
		{"default base url", "", "https://api.openai.com/v1"},
		{"blank base url", "   ", "https://api.openai.com/v1"},
		{"custom base url is trimmed", "  https://proxy.example/v1 ", "https://proxy.example/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []string{"codex", "exec", "--json", "prompt"}
			original := slices.Clone(in)
			got, err := CodexUserProviderArgv(in, tc.baseURL)
			if err != nil {
				t.Fatal(err)
			}
			want := append([]string{"codex", "exec", "--json"}, providerArgs(tc.wantURL)...)
			want = append(want, "prompt")
			if !slices.Equal(got, want) {
				t.Fatalf("argv = %#v\nwant %#v", got, want)
			}
			if !slices.Equal(in, original) {
				t.Fatalf("input argv was modified: %#v", in)
			}
		})
	}
}

func TestCodexUserProviderArgvRejectsShortInvocation(t *testing.T) {
	for _, argv := range [][]string{nil, {"codex"}} {
		if _, err := CodexUserProviderArgv(argv, ""); err == nil || err.Error() != "invalid Codex invocation" {
			t.Fatalf("CodexUserProviderArgv(%#v) error = %v, want invalid Codex invocation", argv, err)
		}
	}
}
