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

// runBindCLIService runs `bind-cli.sh --service` with fake uname, curl, id
// and the given systemctl/loginctl/sudo scripts. It returns the combined
// output, the exit error and the commands the fakes recorded.
func runBindCLIService(t *testing.T, fakes map[string]string) (string, error, string) {
	t.Helper()
	home := t.TempDir()
	runner := filepath.Join(home, "runner-source")
	if err := os.WriteFile(runner, []byte("runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(home, "fake-bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"uname": "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; *) echo Linux ;; esac\n",
		"id":    "#!/bin/sh\ncase \"$1\" in -u) echo 4242 ;; -un) echo tester ;; *) /usr/bin/id \"$@\" ;; esac\n",
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
	}
	for name, script := range fakes {
		scripts[name] = script
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	serviceLog := filepath.Join(home, "service.log")
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
		"TEST_BUS_READY="+filepath.Join(home, "bus-ready"),
	)
	cmd := exec.Command("/bin/bash", "bind-cli.sh",
		"--server", "http://isolated.example",
		"--token", "bind-test",
		"--from", runner,
		"--bin-dir", filepath.Join(home, "bin"),
		"--service",
	)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	log, _ := os.ReadFile(serviceLog)
	return string(out), err, string(log)
}

// When neither the user nor passwordless sudo can start user@<uid>, the
// installer stops with the enable-linger hint instead of a raw dbus error.
func TestBindCLIServiceFailsWithLingerHintWhenUserBusUnavailable(t *testing.T) {
	out, err, log := runBindCLIService(t, map[string]string{
		"loginctl": "#!/bin/sh\nprintf 'loginctl %s\\n' \"$*\" >> \"$TEST_SERVICE_LOG\"\nexit 1\n",
		"sudo":     "#!/bin/sh\nprintf 'sudo %s\\n' \"$*\" >> \"$TEST_SERVICE_LOG\"\nexit 1\n",
		"systemctl": `#!/bin/sh
printf 'systemctl %s\n' "$*" >> "$TEST_SERVICE_LOG"
case "$1" in
  --user) echo 'Failed to connect to bus: No medium found' >&2; exit 1 ;;
esac
case " $* " in
  *" start user@"*) exit 1 ;;
esac
exit 0
`,
	})
	if err == nil {
		t.Fatalf("install succeeded without a user bus:\n%s", out)
	}
	if !strings.Contains(out, "sudo loginctl enable-linger tester") {
		t.Fatalf("missing enable-linger hint:\n%s", out)
	}
	if strings.Contains(out, "Failed to connect to bus") {
		t.Fatalf("dbus error leaked to the installer output:\n%s", out)
	}
	if !strings.Contains(log, "sudo -n loginctl enable-linger tester") || !strings.Contains(log, "sudo -n systemctl start user@4242.service") {
		t.Fatalf("passwordless sudo was not tried after the user call failed: %s", log)
	}
	for _, unwanted := range []string{"--user daemon-reload", "--user enable", "--user restart"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("installer ran %q without a user bus: %s", unwanted, log)
		}
	}
}

// Without systemd the installer explains that --service cannot be used.
func TestBindCLIServiceFailsClearlyWithoutSystemd(t *testing.T) {
	out, err, log := runBindCLIService(t, map[string]string{
		"loginctl":  "#!/bin/sh\nexit 1\n",
		"sudo":      "#!/bin/sh\nexit 1\n",
		"systemctl": "#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >> \"$TEST_SERVICE_LOG\"\necho 'System has not been booted with systemd' >&2\nexit 1\n",
	})
	if err == nil || !strings.Contains(out, "当前系统没有可用的 systemd") {
		t.Fatalf("want the no-systemd error, got err=%v:\n%s", err, out)
	}
	if strings.Contains(log, "start user@") {
		t.Fatalf("tried to start a user manager without systemd: %s", log)
	}
}

// A non-root user whose own loginctl call is refused still gets the bus
// through passwordless sudo, and linger keeps the service after logout.
func TestBindCLIServiceUsesPasswordlessSudoForLinger(t *testing.T) {
	out, err, log := runBindCLIService(t, map[string]string{
		"sudo": `#!/bin/sh
printf 'sudo %s\n' "$*" >> "$TEST_SERVICE_LOG"
[ "$1" = -n ] && shift
TEST_AS_ROOT=1 exec "$@"
`,
		"loginctl": "#!/bin/sh\n[ -n \"${TEST_AS_ROOT:-}\" ] || exit 1\nprintf 'loginctl %s\\n' \"$*\" >> \"$TEST_SERVICE_LOG\"\n",
		"systemctl": `#!/bin/sh
printf 'systemctl %s\n' "$*" >> "$TEST_SERVICE_LOG"
case "$1" in
  --user)
    [ -f "$TEST_BUS_READY" ] && [ -n "${XDG_RUNTIME_DIR:-}" ] && exit 0
    echo 'Failed to connect to bus' >&2; exit 1 ;;
esac
case " $* " in
  *" start user@"*)
    [ -n "${TEST_AS_ROOT:-}" ] || exit 1
    : > "$TEST_BUS_READY"
    ;;
esac
exit 0
`,
	})
	if err != nil {
		t.Fatalf("service install failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "退出登录后服务仍会保持运行") || !strings.Contains(out, "systemd 服务已安装并启动") {
		t.Fatalf("linger was not reported:\n%s", out)
	}
	if !strings.Contains(log, "loginctl enable-linger tester") || !strings.Contains(log, "sudo -n systemctl start user@4242.service") || !strings.Contains(log, "systemctl --user enable agentflow-runner.service") {
		t.Fatalf("unexpected service commands: %s", log)
	}
}
