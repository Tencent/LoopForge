package runtime

import (
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

	bound, err := bindCommand(sb, sb.workspace, command, credentials)
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
	bound, err = bindCommand(sb, sb.workspace, plain, credentials)
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
