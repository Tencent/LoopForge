package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Uninstalling drops only the runner's @reboot hook and writes every other
// crontab entry back unchanged.
func TestBindCLIUninstallRemovesOnlyRunnerCrontabEntry(t *testing.T) {
	text, cronFile := runBindCLIUninstall(t, "#!/bin/sh\nif [ \"${1:-}\" = -l ]; then printf '0 3 * * * /usr/local/bin/backup\\n@reboot /home/u/.agentflow-runner/run-service.sh # agentflow-runner\\n*/5 * * * * /usr/local/bin/sync\\n'; exit 0; fi\ncat > \"$TEST_CRON_FILE\"\nexit 0\n")
	if strings.Contains(text, "crontab") {
		t.Fatalf("unexpected crontab warning:\n%s", text)
	}
	cron, err := os.ReadFile(cronFile)
	if err != nil {
		t.Fatalf("crontab not rewritten on uninstall: %v\n%s", err, text)
	}
	body := string(cron)
	if strings.Contains(body, "agentflow-runner") {
		t.Fatalf("runner hook survived uninstall: %q", body)
	}
	for _, entry := range []string{"0 3 * * * /usr/local/bin/backup", "*/5 * * * * /usr/local/bin/sync"} {
		if !strings.Contains(body, entry) {
			t.Fatalf("uninstall dropped user entry %q: %q", entry, body)
		}
	}
}

// Without a runner hook in the crontab, uninstall leaves the crontab alone.
func TestBindCLIUninstallSkipsCrontabWithoutRunnerHook(t *testing.T) {
	text, cronFile := runBindCLIUninstall(t, "#!/bin/sh\nif [ \"${1:-}\" = -l ]; then printf '0 3 * * * /usr/local/bin/backup\\n'; exit 0; fi\ncat > \"$TEST_CRON_FILE\"\nexit 0\n")
	if _, err := os.Stat(cronFile); !os.IsNotExist(err) {
		body, _ := os.ReadFile(cronFile)
		t.Fatalf("crontab rewritten although it had no runner hook: %q\n%s", body, text)
	}
}

// When `crontab -l` fails on uninstall, the crontab is not rewritten and the
// user is told to remove the hook by hand.
func TestBindCLIUninstallLeavesCrontabAloneWhenReadFails(t *testing.T) {
	text, cronFile := runBindCLIUninstall(t, "#!/bin/sh\nif [ \"${1:-}\" = -l ]; then echo 'crontab: cannot open spool file: Permission denied' >&2; exit 1; fi\ncat > \"$TEST_CRON_FILE\"\nexit 0\n")
	if _, err := os.Stat(cronFile); !os.IsNotExist(err) {
		body, _ := os.ReadFile(cronFile)
		t.Fatalf("crontab rewritten after a failed read: %q\n%s", body, text)
	}
	if !strings.Contains(text, "读取 crontab 失败") || !strings.Contains(text, "Permission denied") || !strings.Contains(text, "手动删除") {
		t.Fatalf("read failure not reported on uninstall:\n%s", text)
	}
}

// A failed `crontab -` write on uninstall is reported instead of swallowed.
func TestBindCLIUninstallWarnsWhenCrontabWriteFails(t *testing.T) {
	text, _ := runBindCLIUninstall(t, "#!/bin/sh\nif [ \"${1:-}\" = -l ]; then printf '@reboot /x/run-service.sh # agentflow-runner\\n'; exit 0; fi\ncat >/dev/null\nexit 1\n")
	if !strings.Contains(text, "写回 crontab 失败") {
		t.Fatalf("write failure not reported on uninstall:\n%s", text)
	}
}

// A failed `crontab -` write during install is reported, and the install
// still finishes with the runner in the background.
func TestBindCLIServiceWarnsWhenCrontabWriteFails(t *testing.T) {
	text, cronFile := runBindCLIWithoutSystemd(t, "#!/bin/sh\nif [ \"${1:-}\" = -l ]; then printf '0 3 * * * /usr/local/bin/backup\\n'; exit 0; fi\ncat >/dev/null\nexit 1\n")
	if !strings.Contains(text, "写入 crontab 失败") {
		t.Fatalf("write failure not reported:\n%s", text)
	}
	if _, err := os.Stat(cronFile); !os.IsNotExist(err) {
		t.Fatalf("fake crontab unexpectedly stored a write: %v", err)
	}
}

// runBindCLIUninstall runs `bind-cli.sh --uninstall` against a Linux
// background install (run-service.sh present, no systemd) with crontabScript
// as the fake crontab. It returns the output and the file a `crontab -` write
// lands in. docker and pkill are stubbed so the test cannot touch real
// runner containers or processes on the machine running it.
func runBindCLIUninstall(t *testing.T, crontabScript string) (string, string) {
	t.Helper()
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	runnerHome := filepath.Join(home, ".agentflow-runner")
	if err := os.MkdirAll(runnerHome, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(runnerHome, "run-service.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(home, "fake-bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	cronFile := filepath.Join(home, "crontab")
	for name, body := range map[string]string{
		"uname":     "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; *) echo Linux ;; esac\n",
		"curl":      "#!/bin/sh\nexit 22\n",
		"systemctl": "#!/bin/sh\necho 'Failed to connect to bus: Host is down' >&2\nexit 1\n",
		"docker":    "#!/bin/sh\nexit 0\n",
		"pkill":     "#!/bin/sh\nexit 1\n",
		"crontab":   crontabScript,
	} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "XDG_RUNTIME_DIR=") || strings.HasPrefix(entry, "DBUS_SESSION_BUS_ADDRESS=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"HOME="+home,
		"PATH="+fakeBin+":/usr/bin:/bin",
		"TEST_CRON_FILE="+cronFile,
	)
	cmd := exec.Command("/bin/bash", "bind-cli.sh", "--uninstall", "--bin-dir", binDir)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("uninstall failed: %v\n%s", err, text)
	}
	if !strings.Contains(text, "已停止后台运行") || !strings.Contains(text, "卸载完成") {
		t.Fatalf("background uninstall not reported:\n%s", text)
	}
	if _, err := os.Stat(runnerHome); !os.IsNotExist(err) {
		t.Fatalf("runner home not removed: %v", err)
	}
	return text, cronFile
}
