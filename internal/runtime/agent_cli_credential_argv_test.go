package runtime

import (
	"reflect"
	"strings"
	"testing"
)

// TestApplyAgentCLICredentialKeepsArgv goes through the exported entry point
// for every CLI that only writes configuration: the command line the caller
// built must come back unchanged, otherwise the agent would be launched with
// a missing or truncated command.
func TestApplyAgentCLICredentialKeepsArgv(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		cred    ProviderCredential
		options AgentCLIOptions
	}{
		{AgentCLIOpenCode, ProviderCredential{APIKey: "k", BaseURL: "https://gw.example", Protocol: AgentCLIProtocolChat}, AgentCLIOptions{Model: "m", SystemPromptFile: "s.md"}},
		{AgentCLIOpenCode, ProviderCredential{APIKey: "k", BaseURL: "https://gw.example", Protocol: AgentCLIProtocolMessages}, AgentCLIOptions{Model: "m"}},
		{AgentCLIAntigravity, ProviderCredential{APIKey: "k", BaseURL: "https://gw.example", Protocol: AgentCLIProtocolChat}, AgentCLIOptions{Model: "m"}},
		{AgentCLIKimi, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: "m", ApprovalCommand: "/hook.sh"}},
		{AgentCLIKimi, ProviderCredential{APIKey: "k", BaseURL: "https://api.example/v1", Protocol: AgentCLIProtocolMessages}, AgentCLIOptions{Model: "m"}},
		{AgentCLIPi, ProviderCredential{APIKey: "k", BaseURL: "https://api.example", Protocol: AgentCLIProtocolResponses}, AgentCLIOptions{Model: "m"}},
	} {
		home := t.TempDir()
		argv := []string{"cli", "--flag", "-p", "hi"}
		got, env, err := ApplyAgentCLICredential(tc.kind, tc.cred, tc.options, HostAgentCLIHome(home), home, append([]string(nil), argv...))
		if err != nil {
			t.Fatalf("%s %s: %v", tc.kind, tc.cred.Protocol, err)
		}
		if !reflect.DeepEqual(got, argv) {
			t.Errorf("%s %s: argv = %q, want %q unchanged", tc.kind, tc.cred.Protocol, got, argv)
		}
		if len(env) == 0 {
			t.Errorf("%s %s: no environment returned", tc.kind, tc.cred.Protocol)
		}
	}
}

// TestApplyAgentCLICredentialRejectsUnknownKind checks the dispatch falls
// through to an error, not to an empty launch, for a CLI it does not know.
func TestApplyAgentCLICredentialRejectsUnknownKind(t *testing.T) {
	home := t.TempDir()
	argv, env, err := ApplyAgentCLICredential("no-such-cli", ProviderCredential{APIKey: "k", BaseURL: "https://api.example"}, AgentCLIOptions{Model: "m"}, HostAgentCLIHome(home), home, []string{"cli"})
	if err == nil || !strings.Contains(err.Error(), `unknown agent CLI "no-such-cli"`) || argv != nil || env != nil {
		t.Fatalf("argv=%q env=%v err=%v; want an unknown agent CLI error and nothing to run", argv, env, err)
	}
}
