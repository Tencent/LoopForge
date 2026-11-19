package deploy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBindCLIInstallsDockerRunnerHelper(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is required for the runner installer")
	}
	for _, tc := range []struct {
		name, hostOS, arch, unameArch string
		upgrade, missingArtifact      bool
	}{
		{name: "macOS_install", hostOS: "darwin", arch: "arm64", unameArch: "arm64"},
		{name: "macOS_upgrade", hostOS: "darwin", arch: "amd64", unameArch: "x86_64", upgrade: true},
		{name: "Linux_install", hostOS: "linux", arch: "arm64", unameArch: "aarch64"},
		{name: "macOS_preserves_existing_helper_on_failed_download", hostOS: "darwin", arch: "arm64", unameArch: "arm64", missingArtifact: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixtureHome := t.TempDir()
			bin := filepath.Join(fixtureHome, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			uname := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in -s) echo %s ;; -m) echo %s ;; esac\n", tc.hostOS, tc.unameArch)
			if err := os.WriteFile(filepath.Join(bin, "uname"), []byte(uname), 0o755); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				targetOS, arch := req.URL.Query().Get("os"), req.URL.Query().Get("arch")
				if arch != tc.arch || (targetOS != tc.hostOS && targetOS != "linux") {
					http.NotFound(w, req)
					return
				}
				switch req.URL.Path {
				case "/runners/agentflow-runner":
					if tc.missingArtifact && targetOS == "linux" {
						http.NotFound(w, req)
						return
					}
					fmt.Fprintf(w, "runner %s/%s", targetOS, arch)
				case "/runners/agentflow-gate-mcp":
					fmt.Fprintf(w, "gate %s/%s", targetOS, arch)
				default:
					http.NotFound(w, req)
				}
			}))
			defer server.Close()
			args := []string{"bind-cli.sh", "--bin-dir", bin, "--no-start"}
			helper := filepath.Join(bin, "agentflow-runner-linux-"+tc.arch)
			statePath := filepath.Join(fixtureHome, ".agentflow-runner", "state.json")
			const state = `{"runnerId":"fixture"}`
			if tc.upgrade {
				if err := os.Mkdir(filepath.Dir(statePath), 0o700); err != nil {
					t.Fatal(err)
				}
				for path, data := range map[string]string{
					statePath: state,
					filepath.Join(filepath.Dir(statePath), "env"): "AGENTFLOW_SERVER_URL=" + server.URL + "\n",
					filepath.Join(bin, "agentflow-runner"):        "old host runner",
				} {
					if err := os.WriteFile(path, []byte(data), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				args = append(args, "--upgrade")
			} else {
				args = append(args, "--server", server.URL, "--token", "fixture")
			}
			if tc.missingArtifact {
				if err := os.WriteFile(helper, []byte("old Linux helper"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("/bin/bash", args...)
			cmd.Env = []string{"HOME=" + fixtureHome, "PATH=" + bin + ":" + filepath.Dir(curl) + ":/usr/bin:/bin", "AGENTFLOW_BROWSER=off"}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}
			assertArtifact := func(path, want string) {
				t.Helper()
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("%s = %q, %v; want %q", filepath.Base(path), got, err, want)
				}
				if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o100 == 0 {
					t.Fatalf("%s is not executable: %v", path, err)
				}
			}
			assertArtifact(filepath.Join(bin, "agentflow-runner"), "runner "+tc.hostOS+"/"+tc.arch)
			switch {
			case tc.hostOS == "linux":
				if _, err := os.Stat(helper); !os.IsNotExist(err) {
					t.Fatalf("Linux install wrote a duplicate runner: %v", err)
				}
			case tc.missingArtifact:
				assertArtifact(helper, "old Linux helper")
			default:
				assertArtifact(helper, "runner linux/"+tc.arch)
				assertArtifact(filepath.Join(bin, "agentflow-gate-mcp-linux-"+tc.arch), "gate linux/"+tc.arch)
			}
			if tc.upgrade {
				if got, err := os.ReadFile(statePath); err != nil || string(got) != state {
					t.Fatalf("upgrade changed runner state: %q, %v", got, err)
				}
			}
		})
	}
}
