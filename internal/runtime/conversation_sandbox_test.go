package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestConversationCLISetupUsesSandboxProxy(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", "")
	argsPath := filepath.Join(t.TempDir(), "setup-args")
	t.Setenv("LOOPFORGE_TEST_SETUP_ARGS", argsPath)
	docker := writeScript(t, `#!/bin/sh
set -eu
case "$*" in
  *"id -u; id -g"*) printf '1000\n1000\n' ;;
  *loopforge-cli-available*) exit 127 ;;
  *"loopforge-cli-setup"*)
    printf '%s\n' "$@" > "$LOOPFORGE_TEST_SETUP_ARGS" ;;
  *loopforge-cli-verify*) printf '/opt/loopforge/conversation-cli/.local/bin/agy\n' ;;
esac
`)
	if err := os.Symlink(docker, filepath.Join(bin, "docker")); err != nil {
		t.Fatal(err)
	}
	sb := &dockerSandbox{name: "workspace", profile: domain.SandboxProfile{Environment: map[string]string{
		"HTTPS_PROXY": "http://proxy.example.test:3128",
		"no_proxy":    "localhost",
		"BASH_ENV":    "/workspace/profile.sh",
	}}}
	if _, err := ensureConversationCLI(context.Background(), sb, AgentCLIAntigravity, "/workspace/home"); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{"HTTPS_PROXY=http://proxy.example.test:3128", "no_proxy=localhost"} {
		if !strings.Contains(string(args), "\n-e\n"+option+"\n") {
			t.Errorf("installer missing proxy option %s", option)
		}
	}
	if strings.Contains(string(args), "BASH_ENV=") {
		t.Error("installer inherited unrelated workspace shell configuration")
	}
}

func TestConversationScriptInstallerRetriesDownload(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail string
	}{
		{name: "recovers after TLS error", fail: "once"},
		{name: "returns persistent TLS error", fail: "always"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("LOOPFORGE_TEST_DOWNLOAD_MARKER", filepath.Join(t.TempDir(), "first-attempt"))
			t.Setenv("LOOPFORGE_TEST_DOWNLOAD_FAILURE", tc.fail)
			curl := writeScript(t, `#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  case "$1" in -o) output=$2; shift ;; esac
  shift
done
echo "download attempted" >&2
if [ -e "$LOOPFORGE_TEST_DOWNLOAD_MARKER" ] && [ "$LOOPFORGE_TEST_DOWNLOAD_FAILURE" = once ]; then
  printf '%s' complete > "$output"
  exit 0
fi
touch "$LOOPFORGE_TEST_DOWNLOAD_MARKER"
printf '%s' incomplete > "$output"
echo "curl: (35) SSL routines::wrong version number" >&2
exit 35
`)
			bash := writeScript(t, `#!/bin/sh
echo "installer invoked"
for installer do :; done
test "$(cat "$installer")" = complete
`)
			for name, script := range map[string]string{"curl": curl, "bash": bash} {
				if err := os.Symlink(script, filepath.Join(bin, name)); err != nil {
					t.Fatal(err)
				}
			}

			output, err := exec.Command("sh", "-ec", scriptCLIInstaller(agyInstallerURL)).CombinedOutput()
			if strings.Count(string(output), "download attempted") != 2 {
				t.Fatalf("installer download did not retry once: %s", output)
			}
			ran := strings.Count(string(output), "installer invoked")
			if tc.fail == "once" {
				if err != nil || ran != 1 {
					t.Fatalf("verified download did not install: %v\n%s", err, output)
				}
			} else if stop, ok := err.(*exec.ExitError); !ok || stop.ExitCode() != 35 || ran != 0 {
				t.Fatalf("failed transfer ran the installer or lost curl's exit code: %v\n%s", err, output)
			}
		})
	}
}
