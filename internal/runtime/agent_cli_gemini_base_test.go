package runtime

import "testing"

func TestAntigravityGeminiBaseDoesNotDuplicateAPIVersion(t *testing.T) {
	for _, endpoint := range []string{"https://gateway.example.test/v1beta", "https://gateway.example.test/google/v1beta/"} {
		home := t.TempDir()
		_, env, err := ApplyAgentCLICredential(AgentCLIAntigravity, ProviderCredential{APIKey: "fixture", BaseURL: endpoint}, AgentCLIOptions{Model: "gemini-fixture"}, HostAgentCLIHome(home), home, []string{"agy", "-p", "hello"})
		want := "https://gateway.example.test"
		if endpoint == "https://gateway.example.test/google/v1beta/" {
			want += "/google"
		}
		if err != nil || env["GOOGLE_GEMINI_BASE_URL"] != want || env["GEMINI_BASE_URL"] != want {
			t.Errorf("endpoint %q: env=%v err=%v, want %q", endpoint, env, err, want)
		}
	}
}

func TestAntigravityRejectsGeminiOperationURL(t *testing.T) {
	home := t.TempDir()
	_, _, err := ApplyAgentCLICredential(AgentCLIAntigravity, ProviderCredential{APIKey: "fixture", BaseURL: "https://gateway.example.test/v1beta/interactions"}, AgentCLIOptions{Model: "gemini-fixture"}, HostAgentCLIHome(home), home, []string{"agy", "-p", "hello"})
	if err == nil {
		t.Fatal("accepted an operation endpoint as the Gemini service root")
	}
}
