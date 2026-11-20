package runtime

import (
	"strings"
	"testing"
)

func TestExtraRunCredentialsNoSources(t *testing.T) {
	if got := ExtraRunCredentials(42); got != nil {
		t.Fatalf("no sources: want nil, got %v", got)
	}
}

func TestExtraRunCredentialsMergesSources(t *testing.T) {
	restore := swapExtraCredentialSources(
		func(userID int64) map[string]string {
			if userID != 7 {
				t.Errorf("source got userID %d, want 7", userID)
			}
			return map[string]string{"gateway": "g1", "shared": "first"}
		},
		func(userID int64) map[string]string {
			return map[string]string{"registry": "r1", "shared": "second"}
		},
	)
	defer restore()

	got := ExtraRunCredentials(7)
	want := map[string]string{"gateway": "g1", "registry": "r1", "shared": "second"}
	if len(got) != len(want) {
		t.Fatalf("merged credentials: got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("credential %q: got %q, want %q", k, got[k], v)
		}
	}
}

func TestBindExtraCredentialEnv(t *testing.T) {
	sb := &localSandbox{id: "extra-cred-test", workspace: t.TempDir()}

	command := CommandSpec{
		Argv:               []string{"sh", "-c", "exit 0"},
		CredentialBindings: []CredentialBinding{BindExtra},
		ExtraCredentialEnv: map[string]string{
			"TOOL_GATEWAY_TOKEN": "gateway",
			"TOOL_MISSING_TOKEN": "absent",
		},
	}
	credentials := RunCredentials{Extra: map[string]string{"gateway": "tok-1"}}

	bound, err := bindCommand(t.Context(), sb, sb.workspace, command, credentials)
	if err != nil {
		t.Fatalf("bindCommand: %v", err)
	}
	if got := bound.environment["TOOL_GATEWAY_TOKEN"]; got != "tok-1" {
		t.Errorf("TOOL_GATEWAY_TOKEN: got %q, want %q", got, "tok-1")
	}
	// A missing credential must clear the variable instead of letting a child
	// inherit the host process's own value.
	if got := bound.environment["TOOL_MISSING_TOKEN"]; got != "" {
		t.Errorf("TOOL_MISSING_TOKEN: got %q, want empty", got)
	}

	// Without the binding the mapping is inert.
	plain := command
	plain.CredentialBindings = nil
	bound, err = bindCommand(t.Context(), sb, sb.workspace, plain, credentials)
	if err != nil {
		t.Fatalf("bindCommand without binding: %v", err)
	}
	if _, ok := bound.environment["TOOL_GATEWAY_TOKEN"]; ok {
		t.Errorf("TOOL_GATEWAY_TOKEN exported without BindExtra binding")
	}
}

// swapExtraCredentialSources replaces the registered sources for a test and
// returns a func restoring the previous set.
func swapExtraCredentialSources(srcs ...ExtraCredentialSource) func() {
	extraCredentialMu.Lock()
	prev := extraCredentialSources
	extraCredentialSources = srcs
	extraCredentialMu.Unlock()
	return func() {
		extraCredentialMu.Lock()
		extraCredentialSources = prev
		extraCredentialMu.Unlock()
	}
}

func TestCompactCommandRedactsCredentialEnvKeysByOrigin(t *testing.T) {
	args := []string{"docker", "exec", "-e", "AWS_SECRET_ACCESS_KEY=super-secret", "-e", "PATH=/usr/bin", "container", "sh"}
	got := compactCommand(args, "aws_secret_access_key")
	if strings.Contains(got, "super-secret") {
		t.Fatalf("credential value leaked into command line: %s", got)
	}
	if !strings.Contains(got, "AWS_SECRET_ACCESS_KEY=<redacted>") {
		t.Fatalf("expected origin-based redaction, got: %s", got)
	}
	if !strings.Contains(got, "PATH=/usr/bin") {
		t.Fatal("unrelated environment must not be redacted")
	}
}

func TestBindExtraMarksCredentialEnvKeysForRedaction(t *testing.T) {
	sb := &localSandbox{id: "extra-cred-redact", workspace: t.TempDir()}
	command := CommandSpec{
		Argv:               []string{"sh", "-c", "exit 0"},
		CredentialBindings: []CredentialBinding{BindExtra},
		ExtraCredentialEnv: map[string]string{"AWS_SECRET_ACCESS_KEY": "gateway"},
	}
	bound, err := bindCommand(t.Context(), sb, sb.workspace, command, RunCredentials{Extra: map[string]string{"gateway": "tok-1"}})
	if err != nil {
		t.Fatalf("bindCommand: %v", err)
	}
	found := false
	for _, key := range bound.credentialEnvKeys {
		if key == "AWS_SECRET_ACCESS_KEY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("credentialEnvKeys not populated: %v", bound.credentialEnvKeys)
	}
}

// TestConversationPathBindsExtraCredentialEnv mirrors the ConversationProcessPool
// rebuild in conversation_process.go: after ConversationCommandFor and the
// CommandSpec reconstruction, BindExtra must still inject (and clear) the
// mapped environment — the conversation child must never inherit the host
// process's own value.
func TestConversationPathBindsExtraCredentialEnv(t *testing.T) {
	sb := &localSandbox{id: "extra-cred-conversation", workspace: t.TempDir()}
	command := CommandSpec{
		Argv:               []string{"cli", "--model", "m"},
		Provider:           "codebuddy",
		CredentialBindings: []CredentialBinding{BindExtra},
		ExtraCredentialEnv: map[string]string{"MY_TOOL_TOKEN": "gateway", "MY_ABSENT_TOKEN": "absent"},
	}

	// Wire format (what travels to the execution machine).
	wire := ConversationCommandFor(command, RunCredentials{Extra: map[string]string{"gateway": "host-owned-value"}})
	if wire.ExtraCredentialEnv["MY_TOOL_TOKEN"] != "gateway" {
		t.Fatalf("ConversationCommandFor dropped ExtraCredentialEnv: %+v", wire.ExtraCredentialEnv)
	}

	// Pool-side reconstruction (conversation_process.go) followed by binding.
	rebuilt := CommandSpec{
		Argv:               append([]string(nil), wire.Argv...),
		Provider:           wire.Provider,
		CredentialBindings: wire.Bindings,
		ExtraCredentialEnv: wire.ExtraCredentialEnv,
	}
	bound, err := bindCommand(t.Context(), sb, sb.workspace, rebuilt, wire.Credentials)
	if err != nil {
		t.Fatalf("bindCommand: %v", err)
	}
	if got := bound.environment["MY_TOOL_TOKEN"]; got != "host-owned-value" {
		t.Errorf("MY_TOOL_TOKEN: got %q, want the user credential", got)
	}
	if got := bound.environment["MY_ABSENT_TOKEN"]; got != "" {
		t.Errorf("MY_ABSENT_TOKEN: got %q, want empty (host value must be cleared)", got)
	}
}

func TestCommandSpecWithCredentialEnvNoopWithoutDeclaration(t *testing.T) {
	spec := CommandSpec{Argv: []string{"sh", "-c", "exit 0"}, CredentialBindings: []CredentialBinding{BindGit}}
	out, err := CommandSpecWithCredentialEnv(spec, nil, RunCredentials{})
	if err != nil {
		t.Fatalf("CommandSpecWithCredentialEnv: %v", err)
	}
	if len(out.CredentialBindings) != 1 || out.CredentialBindings[0] != BindGit {
		t.Fatalf("bindings changed without declaration: %v", out.CredentialBindings)
	}
	if out.ExtraCredentialEnv != nil {
		t.Fatalf("ExtraCredentialEnv set without declaration: %v", out.ExtraCredentialEnv)
	}
}

func TestCommandSpecWithCredentialEnvAppendsBinding(t *testing.T) {
	spec := CommandSpec{Argv: []string{"sh", "-c", "exit 0"}, CredentialBindings: []CredentialBinding{BindGit, BindMCP}}
	env := map[string]string{"BG_USER_TOKEN": "knotToken"}
	out, err := CommandSpecWithCredentialEnv(spec, env, RunCredentials{Extra: map[string]string{"knotToken": "tok-1"}})
	if err != nil {
		t.Fatalf("CommandSpecWithCredentialEnv: %v", err)
	}
	if len(out.CredentialBindings) != 3 || out.CredentialBindings[2] != BindExtra {
		t.Fatalf("BindExtra not appended: %v", out.CredentialBindings)
	}
	if out.ExtraCredentialEnv["BG_USER_TOKEN"] != "knotToken" {
		t.Fatalf("ExtraCredentialEnv not mapped: %v", out.ExtraCredentialEnv)
	}
	if len(spec.CredentialBindings) != 2 {
		t.Fatal("input spec bindings mutated")
	}
}

func TestCommandSpecWithCredentialEnvFailsOnMissingCredential(t *testing.T) {
	spec := CommandSpec{Argv: []string{"sh", "-c", "exit 0"}}
	env := map[string]string{"BG_USER_TOKEN": "knotToken"}
	_, err := CommandSpecWithCredentialEnv(spec, env, RunCredentials{})
	if err == nil {
		t.Fatal("missing declared credential must fail")
	}
	if !strings.Contains(err.Error(), "knotToken") {
		t.Fatalf("error must name the credential kind: %v", err)
	}
}
