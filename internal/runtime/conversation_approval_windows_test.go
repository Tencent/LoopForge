package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWindowsNativeApprovalHookRunsFromNativeAndGitShells(t *testing.T) {
	var allow atomic.Bool
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Params struct {
				Name  string            `json:"name"`
				Input map[string]string `json:"input"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Params.Name != "Bash" || request.Params.Input["command"] != "exact approval 审批 %PATH% & 'quoted'" {
			t.Errorf("approval changed exact tool input: %+v", request)
		}
		received.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"allow": allow.Load(), "reason": "fixture decision"}})
	}))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "approval & spaces ' %PATH%")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "native-approval.py"), []byte(conversationApprovalPython), 0600); err != nil {
		t.Fatal(err)
	}
	launch, err := conversationApprovalLaunch(filepath.ToSlash(root))
	if err != nil {
		t.Fatal(err)
	}
	bash, err := findWindowsHostShell("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range []struct {
		name string
		argv []string
	}{{"cmd", []string{"cmd.exe", "/D", "/S", "/C", launch}}, {"Git Bash", []string{bash, "-c", launch}}, {"PowerShell", []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", launch + ";exit $LASTEXITCODE"}}} {
		t.Run(shell.name, func(t *testing.T) {
			for _, permitted := range []bool{false, true} {
				allow.Store(permitted)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				cmd := exec.CommandContext(ctx, shell.argv[0], shell.argv[1:]...)
				cmd.Env = append(os.Environ(), "AGENTFLOW_NATIVE_APPROVAL_URL="+server.URL)
				cmd.Stdin = strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"exact approval 审批 %PATH% & 'quoted'"}}`)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				cancel()
				if permitted && err != nil || !permitted && (cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 2) {
					t.Fatalf("approval allow=%v status=%v stdout=%s stderr=%s", permitted, err, &stdout, &stderr)
				}
				decision := "deny"
				if permitted {
					decision = "allow"
				}
				if !strings.Contains(stdout.String(), `"permissionDecision": "`+decision+`"`) {
					t.Fatalf("hook did not return %s: %s", decision, &stdout)
				}
			}
		})
	}
	if received.Load() != 6 {
		t.Fatalf("native hook callback count=%d, want 6", received.Load())
	}
	if err := os.Remove(filepath.Join(root, "native-approval.py")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd.exe", "/D", "/S", "/C", launch)
	if output, err := cmd.CombinedOutput(); err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 2 {
		t.Fatalf("missing approval script did not fail closed: %v %s", err, output)
	}
}

func TestWindowsNativeApprovalKeepsLinuxContainerLauncher(t *testing.T) {
	launch, err := conversationApprovalLaunch("/loopforge/conversation", "cursor")
	if err != nil || launch != "python3 '/loopforge/conversation/native-approval.py' cursor" {
		t.Fatalf("Linux container hook changed: %q %v", launch, err)
	}
}

func TestWindowsNativeApprovalRejectsPythonAppAliases(t *testing.T) {
	alias := filepath.Join(t.TempDir(), "Microsoft", "WindowsApps")
	if err := os.MkdirAll(alias, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alias, "python.exe"), []byte("not a real interpreter"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", alias)
	if _, err := conversationApprovalPythonExecutable(context.Background()); err == nil {
		t.Fatal("Windows App Execution Alias was accepted as an approval interpreter")
	}
}

func TestWindowsPlatformApprovalPreservesExistingPolicy(t *testing.T) {
	root := t.TempDir()
	for _, provider := range []string{"claude-code", "codebuddy", "codex"} {
		command := CommandSpec{
			Provider:    provider,
			Argv:        []string{provider, "--settings", filepath.Join(root, "current-settings.json"), "--disallowedTools", "Bash,Write", "-c", `hooks.PreToolUse=[{hooks=[{type="command",command="echo codex-native-guard.py",timeout=5}]}]`, "prompt"},
			environment: map[string]string{"AGENTFLOW_NATIVE_APPROVAL_URL": "fixture-capability", "UNCHANGED": "fixture-setting"},
		}
		beforeArgs := append([]string(nil), command.Argv...)
		beforeEnv := maps.Clone(command.environment)
		adapted, err := adaptPlatformConversationApproval(root, command)
		if err != nil || !reflect.DeepEqual(adapted.Argv, beforeArgs) || !reflect.DeepEqual(adapted.environment, beforeEnv) {
			t.Fatalf("Windows altered %s platform settings, flags or unrelated hooks: %v", provider, err)
		}
	}
}
