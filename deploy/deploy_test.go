package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// Without systemd, --service keeps the runner in the background instead of
// stopping with an init-system error.
func TestBindCLIServiceRunsInBackgroundWithoutSystemd(t *testing.T) {
	// A user without a crontab: `crontab -l` exits 1 with "no crontab for".
	text, cronFile := runBindCLIWithoutSystemd(t, "#!/bin/sh\nif [ \"${1:-}\" = -l ]; then echo \"no crontab for $(id -un)\" >&2; exit 1; fi\ncat > \"$TEST_CRON_FILE\"\nexit 0\n")
	if strings.Contains(text, "crontab") {
		t.Fatalf("a missing crontab must not produce a crontab warning:\n%s", text)
	}
	cron, err := os.ReadFile(cronFile)
	if err != nil || !strings.Contains(string(cron), "@reboot") || !strings.Contains(string(cron), "run-service.sh") {
		t.Fatalf("reboot hook missing: %q %v", cron, err)
	}
}

// Installing the reboot hook keeps every existing crontab entry.
func TestBindCLIServiceKeepsExistingCrontabEntries(t *testing.T) {
	text, cronFile := runBindCLIWithoutSystemd(t, "#!/bin/sh\nif [ \"${1:-}\" = -l ]; then printf '0 3 * * * /usr/local/bin/backup\\n@reboot /old/run-service.sh # agentflow-runner\\n'; exit 0; fi\ncat > \"$TEST_CRON_FILE\"\nexit 0\n")
	if strings.Contains(text, "crontab") {
		t.Fatalf("unexpected crontab warning:\n%s", text)
	}
	cron, err := os.ReadFile(cronFile)
	if err != nil {
		t.Fatalf("crontab not written: %v\n%s", err, text)
	}
	body := string(cron)
	if !strings.Contains(body, "0 3 * * * /usr/local/bin/backup") {
		t.Fatalf("existing entry dropped: %q", body)
	}
	if strings.Contains(body, "/old/run-service.sh") || strings.Count(body, "# agentflow-runner") != 1 {
		t.Fatalf("stale reboot hook not replaced exactly once: %q", body)
	}
}

// When `crontab -l` fails for any reason other than "no crontab", the
// installer must not write a crontab: doing so would replace the user's
// existing entries with only the runner hook.
func TestBindCLIServiceLeavesCrontabAloneWhenReadFails(t *testing.T) {
	text, cronFile := runBindCLIWithoutSystemd(t, "#!/bin/sh\nif [ \"${1:-}\" = -l ]; then echo 'crontab: cannot open spool file: Permission denied' >&2; exit 1; fi\ncat > \"$TEST_CRON_FILE\"\nexit 0\n")
	if _, err := os.Stat(cronFile); !os.IsNotExist(err) {
		body, _ := os.ReadFile(cronFile)
		t.Fatalf("crontab was rewritten after a failed read: %q\n%s", body, text)
	}
	if !strings.Contains(text, "读取 crontab 失败") || !strings.Contains(text, "Permission denied") {
		t.Fatalf("read failure not reported:\n%s", text)
	}
}

// runBindCLIWithoutSystemd installs with --service on a machine whose
// systemctl cannot reach a bus, using crontabScript as the fake crontab. It
// returns the installer output and the file a `crontab -` write lands in.
func runBindCLIWithoutSystemd(t *testing.T, crontabScript string) (string, string) {
	t.Helper()
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	marker := filepath.Join(home, "started")
	runner := filepath.Join(home, "runner-source")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\necho started >> "+marker+"\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(home, ".agentflow-runner", "run-service.sh")
	t.Cleanup(func() {
		_ = exec.Command("pkill", "-f", script).Run()
	})
	fakeBin := filepath.Join(home, "fake-bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	cronFile := filepath.Join(home, "crontab")
	for name, body := range map[string]string{
		"uname": "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; *) echo Linux ;; esac\n",
		"curl": `#!/bin/sh
case "$*" in
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
		"systemctl": "#!/bin/sh\necho 'Failed to connect to bus: Host is down' >&2\nexit 1\n",
		"crontab":   crontabScript,
	} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := make([]string, 0, len(os.Environ())+5)
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
		"TEST_CRON_FILE="+cronFile,
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
	if strings.Contains(text, "Failed to connect to bus") || strings.Contains(text, "不能安装常驻服务") {
		t.Fatalf("installer stopped on a missing init system:\n%s", text)
	}
	if !strings.Contains(text, "安装完成，已在后台运行") {
		t.Fatalf("background start not reported:\n%s", text)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if body, readErr := os.ReadFile(marker); readErr == nil && strings.Contains(string(body), "started") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner was not started:\n%s", text)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(home, ".config/systemd/user/agentflow-runner.service")); !os.IsNotExist(err) {
		t.Fatal("wrote a systemd unit on a machine without systemd")
	}
	return text, cronFile
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
