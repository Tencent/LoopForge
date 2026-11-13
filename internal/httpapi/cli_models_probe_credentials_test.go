package httpapi

import (
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestCLIProbeCredentialsMergesFormOverStored(t *testing.T) {
	str := func(s string) *string { return &s }
	storedUser := domain.User{
		ClaudeCodeAPIKey:  " stored-claude-key ",
		ClaudeCodeBaseURL: " https://stored.claude ",
		AgentCLICredentials: map[string]domain.AgentCLICredential{
			runtime.AgentCLIOpenCode: {
				APIKey:   "stored-oc-key",
				BaseURL:  "https://stored.oc",
				Protocol: runtime.AgentCLIProtocolResponses,
			},
		},
	}
	cases := []struct {
		name         string
		req          cliModelProbeRequest
		engine       string
		hasStored    bool
		wantKey      string
		wantBaseURL  string
		wantProtocol string
	}{
		{
			name:        "blank form falls back to stored credential",
			req:         cliModelProbeRequest{APIKey: "   "},
			engine:      assistantEngineClaude,
			hasStored:   true,
			wantKey:     "stored-claude-key",
			wantBaseURL: "https://stored.claude",
		},
		{
			name:        "form api key and base url win and are trimmed",
			req:         cliModelProbeRequest{APIKey: " form-key ", BaseURL: str(" https://form.example ")},
			engine:      assistantEngineClaude,
			hasStored:   true,
			wantKey:     "form-key",
			wantBaseURL: "https://form.example",
		},
		{
			name:      "empty base url means the official endpoint",
			req:       cliModelProbeRequest{BaseURL: str("")},
			engine:    assistantEngineClaude,
			hasStored: true,
			wantKey:   "stored-claude-key",
		},
		{
			name:         "null protocol keeps the stored agent cli protocol",
			req:          cliModelProbeRequest{},
			engine:       runtime.AgentCLIOpenCode,
			hasStored:    true,
			wantKey:      "stored-oc-key",
			wantBaseURL:  "https://stored.oc",
			wantProtocol: runtime.AgentCLIProtocolResponses,
		},
		{
			name:         "form protocol overrides the stored one",
			req:          cliModelProbeRequest{Protocol: str(runtime.AgentCLIProtocolMessages)},
			engine:       runtime.AgentCLIOpenCode,
			hasStored:    true,
			wantKey:      "stored-oc-key",
			wantBaseURL:  "https://stored.oc",
			wantProtocol: runtime.AgentCLIProtocolMessages,
		},
		{
			name:         "unknown form protocol normalizes to the cli default",
			req:          cliModelProbeRequest{Protocol: str("bogus")},
			engine:       runtime.AgentCLIOpenCode,
			hasStored:    true,
			wantKey:      "stored-oc-key",
			wantBaseURL:  "https://stored.oc",
			wantProtocol: runtime.AgentCLIProtocolChat,
		},
		{
			name:        "protocol is ignored for non agent clis",
			req:         cliModelProbeRequest{Protocol: str(runtime.AgentCLIProtocolMessages)},
			engine:      assistantEngineClaude,
			hasStored:   true,
			wantKey:     "stored-claude-key",
			wantBaseURL: "https://stored.claude",
		},
		{
			name:         "no stored user uses only the form values",
			req:          cliModelProbeRequest{APIKey: "form-key", Protocol: str(runtime.AgentCLIProtocolMessages)},
			engine:       runtime.AgentCLIOpenCode,
			hasStored:    false,
			wantKey:      "form-key",
			wantProtocol: runtime.AgentCLIProtocolMessages,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := domain.User{}
			if tc.hasStored {
				stored = storedUser
			}
			apiKey, baseURL, protocol := cliProbeCredentials(tc.req, tc.engine, stored, tc.hasStored)
			if apiKey != tc.wantKey || baseURL != tc.wantBaseURL || protocol != tc.wantProtocol {
				t.Fatalf("cliProbeCredentials() = (%q, %q, %q), want (%q, %q, %q)",
					apiKey, baseURL, protocol, tc.wantKey, tc.wantBaseURL, tc.wantProtocol)
			}
		})
	}
}
