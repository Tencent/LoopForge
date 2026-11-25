package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDockerConversationUserIdentity(t *testing.T) {
	if os.Getenv("AGENTFLOW_DOCKER_CONVERSATION_SMOKE") != "1" {
		t.Skip("set AGENTFLOW_DOCKER_CONVERSATION_SMOKE=1 with a Python3/CodeBuddy/Node image")
	}
	image := os.Getenv("AGENTFLOW_DOCKER_CONVERSATION_IMAGE")
	if image == "" {
		image = "loopforge-cli-lab:local"
	}
	for _, tc := range []struct {
		name, user string
		readOnly   bool
		missing    bool
	}{
		{name: "missing_numeric_uid", user: "58585:58585", missing: true},
		{name: "existing_nonroot", user: "1000:1000"},
		{name: "readonly_existing", user: "1000:1000", readOnly: true},
		{name: "readonly_missing", user: "58585:58585", readOnly: true, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			name := fmt.Sprintf("lf-cli-identity-%d", time.Now().UnixNano())
			args := []string{"run", "--pull=never", "-d", "--name", name, "--user", tc.user, "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--workdir", "/"}
			if tc.readOnly {
				args = append(args, "--read-only")
			}
			args = append(args, image, "sh", "-c", "sleep 120")
			if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
				t.Fatalf("start fixture: %s: %v", out, err)
			}
			defer exec.Command("docker", "rm", "-f", name).Run()
			run := func(argv ...string) ([]byte, error) {
				return exec.CommandContext(ctx, "docker", append([]string{"exec", name}, argv...)...).CombinedOutput()
			}
			before, err := run("cat", "/etc/passwd")
			if err != nil {
				t.Fatal(err)
			}
			probe := `const os = require("os"); const info = os.userInfo(); if (info.uid !== process.getuid() || info.gid !== process.getgid()) throw Error("identity changed");`
			if _, err := run("node", "-e", probe); (err != nil) != tc.missing {
				t.Fatalf("fixture account differs: %v", err)
			}
			sandbox := &dockerSandbox{name: name, started: true}
			for range 2 {
				_, err = ensureConversationCLI(ctx, sandbox, "codebuddy", "/stable/native/home")
				if tc.readOnly && tc.missing {
					if err == nil || !strings.Contains(err.Error(), "has no passwd entry") {
						t.Fatalf("missing clear read-only error: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if out, err := run("node", "-e", probe); err != nil {
				t.Fatalf("native userInfo: %s: %v", out, err)
			}
			after, err := run("cat", "/etc/passwd")
			if err != nil {
				t.Fatal(err)
			}
			if tc.missing {
				if strings.Count(string(after), ":58585:58585:") != 1 || !strings.Contains(string(after), ":/stable/native/home:/bin/false") {
					t.Fatalf("missing or duplicated non-login identity: %s", after)
				}
			} else if string(before) != string(after) {
				t.Fatal("existing image identity changed")
			}
			if out, err := run("sh", "-ec", `grep -q '^CapEff:[[:space:]]*0000000000000000$' /proc/self/status; grep -q '^NoNewPrivs:[[:space:]]*1$' /proc/self/status`); err != nil {
				t.Fatalf("container protections changed: %s: %v", out, err)
			}
		})
	}
}
