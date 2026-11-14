package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallZCodeApprovalWritesResumeScriptAndConfigWithoutExistingConfig(t *testing.T) {
	root := t.TempDir()
	home := AgentCLIHome{Base: root, Rel: "home"}
	if err := installZCodeApproval(root, home, "launch"); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(root, "zcode-resume-model.py"))
	if err != nil || !bytes.Equal(script, conversationZCodeResumeScript()) {
		t.Fatalf("resume script not written: %v", err)
	}
	configured, hook := zcodePreToolUseCommand(t, readJSONFile(t, filepath.Join(root, "home", zcodeConfigPath)))
	if configured["enabled"] != true || hook["command"] != "launch" {
		t.Fatalf("approval hook not installed: %#v %#v", configured, hook)
	}
}

func TestInstallZCodeApprovalRejectsMalformedConfig(t *testing.T) {
	root := t.TempDir()
	home := AgentCLIHome{Base: root, Rel: "home"}
	path := filepath.Join(root, "home", zcodeConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installZCodeApproval(root, home, "launch"); err == nil {
		t.Fatal("malformed ZCode config must not be overwritten")
	}
	if data, _ := os.ReadFile(path); string(data) != "{" {
		t.Fatalf("malformed config rewritten: %q", data)
	}
}

func TestInstallZCodeApprovalRejectsDirectoryAtConfigPath(t *testing.T) {
	root := t.TempDir()
	home := AgentCLIHome{Base: root, Rel: "home"}
	// A directory at the config path fails to read with an error other than
	// "not exist", which must abort instead of starting from an empty config.
	if err := os.MkdirAll(filepath.Join(root, "home", zcodeConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installZCodeApproval(root, home, "launch"); err == nil {
		t.Fatal("unreadable ZCode config must fail")
	}
}

func TestInstallZCodeApprovalKeepsConfigItCannotRead(t *testing.T) {
	root := t.TempDir()
	home := AgentCLIHome{Base: root, Rel: "home"}
	path := filepath.Join(root, "home", zcodeConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{"mcp":{"agentflow_session":{"type":"http"}}}`
	// Write-only: reading fails with a permission error while a rewrite would
	// still succeed, so ignoring the read error would silently drop MCP.
	if err := os.WriteFile(path, []byte(original), 0o200); err != nil {
		t.Fatal(err)
	}
	err := installZCodeApproval(root, home, "launch")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		// Root ignores file modes: the read succeeds and the config is merged.
		if err != nil {
			t.Fatal(err)
		}
		config := readJSONFile(t, path)
		zcodePreToolUseCommand(t, config)
		if config["mcp"] == nil {
			t.Fatalf("MCP dropped: %#v", config)
		}
		return
	}
	if err == nil {
		t.Fatal("unreadable ZCode config must fail")
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatalf("unreadable config rewritten: %q", data)
	}
}

func TestInstallZCodeApprovalRejectsInvalidHome(t *testing.T) {
	root := t.TempDir()
	if err := installZCodeApproval(root, AgentCLIHome{Base: root, Rel: "."}, "launch"); err == nil {
		t.Fatal("invalid agent CLI home must fail")
	}
}

func TestInstallConversationApprovalZCodeUsesNativeHookAndReportsConfigErrors(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallConversationApproval(root, AgentCLIZCode, CommandSpec{Argv: []string{"zcode"}}); err != nil {
		t.Fatal(err)
	}
	_, hook := zcodePreToolUseCommand(t, readJSONFile(t, filepath.Join(root, "home", zcodeConfigPath)))
	if want := "python3 " + shellSingleQuote(filepath.Join(root, "native-approval.py")); hook["command"] != want {
		t.Fatalf("hook command = %#v, want %q", hook["command"], want)
	}

	if err := os.WriteFile(filepath.Join(root, "home", zcodeConfigPath), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallConversationApproval(root, AgentCLIZCode, CommandSpec{Argv: []string{"zcode"}}); err == nil {
		t.Fatal("ZCode config error must be returned")
	}
}
