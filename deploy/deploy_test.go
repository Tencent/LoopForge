package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBindCLIServiceStartsUserBusWhenSessionMissing(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	runner := filepath.Join(home, "runner-source")
	if err := os.WriteFile(runner, []byte("runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(home, "fake-bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceLog := filepath.Join(home, "service.log")
	busReady := filepath.Join(home, "bus-ready")
	for name, script := range map[string]string{
		"uname": "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; *) echo Linux ;; esac\n",
		"curl": `#!/bin/sh
case "$*" in
  *runners/health*) exit 0 ;;
  *agentflow-gate-mcp*)
    previous=""
    for argument in "$@"; do
      if [ "$previous" = -o ]; then printf gate-helper > "$argument"; exit 0; fi
      previous="$argument"
    done
    exit 2
    ;;
esac
exit 22
`,
		"loginctl": "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_SERVICE_LOG\"\nexit 0\n",
		"sudo":     "#!/bin/sh\nprintf 'sudo %s\\n' \"$*\" >> \"$TEST_SERVICE_LOG\"\nexit 1\n",
		"systemctl": `#!/bin/sh
printf '%s\n' "$*" >> "$TEST_SERVICE_LOG"
case "$1" in
  --user)
    if [ ! -f "$TEST_BUS_READY" ] || [ -z "${XDG_RUNTIME_DIR:-}" ]; then
      echo 'Failed to connect to bus: $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined' >&2
      exit 1
    fi
    exit 0
    ;;
esac
case " $* " in
  *" start user@"*)
    : > "$TEST_BUS_READY"
    exit 0
    ;;
esac
exit 0
`,
	} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "XDG_RUNTIME_DIR=") || strings.HasPrefix(entry, "DBUS_SESSION_BUS_ADDRESS=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"HOME="+home,
		"PATH="+fakeBin+":/usr/bin:/bin",
		"AGENTFLOW_BROWSER=off",
		"TEST_SERVICE_LOG="+serviceLog,
		"TEST_BUS_READY="+busReady,
	)
	cmd := exec.Command("/bin/bash", "bind-cli.sh",
		"--server", "http://isolated.example",
		"--token", "bind-test",
		"--from", runner,
		"--bin-dir", binDir,
		"--service",
	)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("service install failed: %v\n%s", err, out)
	}
	text := string(out)
	if strings.Contains(text, "Failed to connect to bus") {
		t.Fatalf("dbus error leaked to the installer output:\n%s", text)
	}
	if !strings.Contains(text, "已接通 systemd 用户总线") || !strings.Contains(text, "systemd 服务已安装并启动") {
		t.Fatalf("user bus was not brought up:\n%s", text)
	}
	log, err := os.ReadFile(serviceLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(log)
	if strings.Contains(logText, "sudo ") {
		t.Fatalf("installer invoked sudo: %s", logText)
	}
	if !strings.Contains(logText, "enable-linger") || !strings.Contains(logText, "start user@") || !strings.Contains(logText, "--user enable") {
		t.Fatalf("user manager was not started before enable: %s", logText)
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config/systemd/user/agentflow-runner.service"))
	if err != nil || !strings.Contains(string(unit), "Restart=always") {
		t.Fatalf("user unit missing: %q %v", unit, err)
	}
}
