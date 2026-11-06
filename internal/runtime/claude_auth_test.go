package runtime

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func stubClaudeAuthProbe(t *testing.T, probe func(baseURL, apiKey, scheme string) (bool, bool)) {
	t.Helper()
	previous := claudeAuthProbe
	claudeAuthProbe = probe
	claudeAuthCache = sync.Map{}
	t.Cleanup(func() {
		claudeAuthProbe = previous
		claudeAuthCache = sync.Map{}
	})
}

// fakeClaudeGateway accepts the key only through the given header scheme and
// answers 400 for the empty body, like a real Messages endpoint.
func fakeClaudeGateway(t *testing.T, scheme string, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		ok := (scheme == "bearer" && r.Header.Get("Authorization") == "Bearer secret") ||
			(scheme == "x-api-key" && r.Header.Get("x-api-key") == "secret")
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClaudeCodeCredentialEnvironment(t *testing.T) {
	stubClaudeAuthProbe(t, probeClaudeAuth)

	official := ClaudeCodeCredentialEnvironment(" secret ", "")
	if official["ANTHROPIC_API_KEY"] != "secret" || official["ANTHROPIC_BASE_URL"] != claudeOfficialBaseURL || official["ANTHROPIC_AUTH_TOKEN"] != "" {
		t.Fatalf("official API env = %v", official)
	}

	var bearerHits int
	bearer := fakeClaudeGateway(t, "bearer", &bearerHits)
	env := ClaudeCodeCredentialEnvironment("secret", bearer.URL+"/")
	if env["ANTHROPIC_AUTH_TOKEN"] != "secret" || env["ANTHROPIC_API_KEY"] != "" || env["ANTHROPIC_BASE_URL"] != bearer.URL {
		t.Fatalf("bearer gateway env = %v", env)
	}

	var apiKeyHits int
	apiKeyOnly := fakeClaudeGateway(t, "x-api-key", &apiKeyHits)
	env = ClaudeCodeCredentialEnvironment("secret", apiKeyOnly.URL)
	if env["ANTHROPIC_API_KEY"] != "secret" || env["ANTHROPIC_AUTH_TOKEN"] != "" || env["ANTHROPIC_BASE_URL"] != apiKeyOnly.URL {
		t.Fatalf("x-api-key gateway did not fall back: %v", env)
	}
	if apiKeyHits != 2 {
		t.Fatalf("x-api-key gateway probes = %d, want bearer then x-api-key", apiKeyHits)
	}
	ClaudeCodeCredentialEnvironment("secret", apiKeyOnly.URL)
	if apiKeyHits != 2 {
		t.Fatalf("probe result not cached: %d probes", apiKeyHits)
	}

	var rejectHits int
	rejectAll := fakeClaudeGateway(t, "none", &rejectHits)
	env = ClaudeCodeCredentialEnvironment("secret", rejectAll.URL)
	if env["ANTHROPIC_AUTH_TOKEN"] != "secret" || env["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("gateway rejecting both should keep bearer: %v", env)
	}

	env = ClaudeCodeCredentialEnvironment("secret", "http://127.0.0.1:9")
	if env["ANTHROPIC_AUTH_TOKEN"] != "secret" || env["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("unreachable gateway should keep bearer: %v", env)
	}
}
