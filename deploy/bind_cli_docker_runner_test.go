package deploy

import (
	"debug/elf"
	"encoding/binary"
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
		upgrade                       bool
		helperArtifact, source        string
		preserveHelper                bool
	}{
		{name: "macOS_install", hostOS: "darwin", arch: "arm64", unameArch: "arm64"},
		{name: "macOS_upgrade", hostOS: "darwin", arch: "amd64", unameArch: "x86_64", upgrade: true},
		{name: "Linux_install", hostOS: "linux", arch: "arm64", unameArch: "aarch64"},
		{name: "macOS_preserves_helper_on_failed_download", hostOS: "darwin", arch: "arm64", unameArch: "arm64", helperArtifact: "unavailable", preserveHelper: true},
		{name: "macOS_preserves_helper_when_server_serves_macOS_binary_for_Linux", hostOS: "darwin", arch: "arm64", unameArch: "arm64", upgrade: true, helperArtifact: "mach-o", preserveHelper: true},
		{name: "macOS_preserves_helper_when_server_serves_wrong_architecture", hostOS: "darwin", arch: "amd64", unameArch: "x86_64", upgrade: true, helperArtifact: "wrong-arch", preserveHelper: true},
		{name: "macOS_preserves_helper_with_local_runner", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", preserveHelper: true},
		{name: "macOS_preserves_helper_with_host_only_URL", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "host-url", preserveHelper: true},
		{name: "macOS_updates_helper_with_explicit_Linux_URL", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "template"},
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
			linuxRunner := bindFixtureLinuxRunner(tc.arch, "new helper")
			oldHelper := bindFixtureLinuxRunner(tc.arch, "old helper")
			hostRunner := linuxRunner
			if tc.hostOS == "darwin" {
				hostRunner = "\xcf\xfa\xed\xfe" + tc.arch + " macOS runner"
			}
			if tc.source == "from" {
				hostRunner = "custom " + hostRunner
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				targetOS, arch := req.URL.Query().Get("os"), req.URL.Query().Get("arch")
				if arch != tc.arch || (targetOS != tc.hostOS && targetOS != "linux") {
					http.NotFound(w, req)
					return
				}
				switch req.URL.Path {
				case "/runners/agentflow-runner":
					if targetOS == tc.hostOS {
						fmt.Fprint(w, hostRunner)
						return
					}
					switch tc.helperArtifact {
					case "unavailable":
						http.NotFound(w, req)
					case "mach-o":
						fmt.Fprint(w, hostRunner)
					case "wrong-arch":
						fmt.Fprint(w, bindFixtureLinuxRunner("arm64", "new helper"))
					default:
						fmt.Fprint(w, linuxRunner)
					}
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
			switch tc.source {
			case "from":
				custom := filepath.Join(fixtureHome, "custom-runner")
				if err := os.WriteFile(custom, []byte(hostRunner), 0o755); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--from", custom)
			case "host-url":
				args = append(args, "--download-url", server.URL+"/runners/agentflow-runner?os=darwin&arch={arch}")
			case "template":
				args = append(args, "--download-url", server.URL+"/runners/agentflow-runner?os={os}&arch={arch}")
			}
			if tc.upgrade || tc.source != "" || tc.preserveHelper {
				if err := os.WriteFile(helper, []byte(oldHelper), 0o755); err != nil {
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
			assertArtifact(filepath.Join(bin, "agentflow-runner"), hostRunner)
			switch {
			case tc.hostOS == "linux":
				if _, err := os.Stat(helper); !os.IsNotExist(err) {
					t.Fatalf("Linux install wrote a duplicate runner: %v", err)
				}
			case tc.preserveHelper:
				assertArtifact(helper, oldHelper)
			default:
				assertArtifact(helper, linuxRunner)
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

func bindFixtureLinuxRunner(arch, payload string) string {
	header := make([]byte, 64)
	copy(header, "\x7fELF\x02\x01\x01\x00")
	machine := elf.EM_X86_64
	if arch == "arm64" {
		machine = elf.EM_AARCH64
	}
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(header[18:], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint16(header[52:], uint16(len(header)))
	return string(header) + payload
}
