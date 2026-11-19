package deploy

import (
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestBindCLIInstallsDockerRunnerHelper(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is required for the runner installer")
	}
	script, err := filepath.Abs("bind-cli.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, hostOS, arch, unameArch    string
		upgrade, launchd                 bool
		helperArtifact, source, existing string
		preserveHelper                   bool
		templatePath                     bool
		helperAliasesRunner              bool
		universal, previousUniversal     bool
		intelHardware, sharedTemplateDir bool
		arm64OnlyArtifacts               bool
		templateMissingArch              bool
		trimSavedTemplate                bool
		relativeServiceTemplate          bool
		missingRequiredGate              bool
		missingReplacementHelper         bool
	}{
		{name: "macOS_install", hostOS: "darwin", arch: "arm64", unameArch: "arm64"},
		{name: "macOS_uses_template_in_manual_start_and_upgrade", hostOS: "darwin", arch: "arm64", unameArch: "arm64", templatePath: true, source: "from"},
		{name: "macOS_rejects_template_aliasing_the_runner", hostOS: "darwin", arch: "arm64", unameArch: "arm64", templatePath: true, helperAliasesRunner: true},
		{name: "macOS_upgrade", hostOS: "darwin", arch: "amd64", unameArch: "x86_64", upgrade: true},
		{name: "Linux_install", hostOS: "linux", arch: "arm64", unameArch: "aarch64"},
		{name: "macOS_preserves_helper_on_failed_download", hostOS: "darwin", arch: "arm64", unameArch: "arm64", helperArtifact: "unavailable", preserveHelper: true},
		{name: "macOS_preserves_helper_when_server_serves_macOS_binary_for_Linux", hostOS: "darwin", arch: "arm64", unameArch: "arm64", upgrade: true, helperArtifact: "mach-o", preserveHelper: true},
		{name: "macOS_preserves_helper_when_server_serves_wrong_architecture", hostOS: "darwin", arch: "amd64", unameArch: "x86_64", upgrade: true, helperArtifact: "wrong-arch", preserveHelper: true},
		{name: "macOS_preserves_helper_when_only_ELF_header_is_served", hostOS: "darwin", arch: "arm64", unameArch: "arm64", upgrade: true, helperArtifact: "truncated-header", preserveHelper: true},
		{name: "macOS_preserves_helper_when_ELF_segment_is_truncated", hostOS: "darwin", arch: "arm64", unameArch: "arm64", upgrade: true, helperArtifact: "truncated-segment", preserveHelper: true},
		{name: "macOS_preserves_helper_with_local_runner", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", preserveHelper: true},
		{name: "macOS_preserves_helper_with_implicit_source_build", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "checkout", preserveHelper: true},
		{name: "macOS_upgrades_launchd_with_custom_runner", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", launchd: true, upgrade: true, preserveHelper: true},
		{name: "macOS_upgrades_launchd_on_template_path", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", launchd: true, upgrade: true, templatePath: true},
		{name: "macOS_installs_companion_for_Intel_runner_on_arm64", hostOS: "darwin", arch: "amd64", unameArch: "arm64", source: "from"},
		{name: "macOS_under_Rosetta_installs_both_companions_for_universal_Runner", hostOS: "darwin", arch: "arm64", unameArch: "x86_64", source: "from", universal: true},
		{name: "macOS_Intel_installs_universal_Runner_from_amd64_only_platform", hostOS: "darwin", arch: "amd64", unameArch: "x86_64", source: "from", universal: true, intelHardware: true},
		{name: "macOS_Apple_Silicon_installs_universal_Runner_from_arm64_only_platform", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", universal: true, arm64OnlyArtifacts: true},
		{name: "macOS_rejects_universal_companions_with_a_shared_template_directory", hostOS: "darwin", arch: "arm64", unameArch: "x86_64", source: "from", universal: true, templatePath: true, sharedTemplateDir: true},
		{name: "macOS_rejected_universal_upgrade_preserves_installed_runner", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", universal: true, upgrade: true, launchd: true, templatePath: true, templateMissingArch: true},
		{name: "macOS_preserves_installed_runner_if_required_gate_is_unavailable", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", universal: true, upgrade: true, missingRequiredGate: true},
		{name: "macOS_architecture_upgrade_keeps_working_runner_when_new_helper_is_unavailable", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", universal: true, upgrade: true, launchd: true, missingReplacementHelper: true},
		{name: "macOS_keeps_working_Rosetta_slice_when_native_helper_is_unavailable", hostOS: "darwin", arch: "arm64", unameArch: "x86_64", source: "from", previousUniversal: true, upgrade: true, launchd: true, missingReplacementHelper: true},
		{name: "macOS_preserves_working_helper_with_whitespace_in_saved_template", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", upgrade: true, templatePath: true, trimSavedTemplate: true, missingReplacementHelper: true},
		{name: "macOS_preserves_readable_old_helper_without_execute_permission", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", existing: "non-executable", upgrade: true, launchd: true, missingReplacementHelper: true},
		{name: "macOS_preserves_helper_relative_to_launchd_working_directory", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", upgrade: true, launchd: true, relativeServiceTemplate: true, missingReplacementHelper: true},
		{name: "macOS_repairs_incompatible_local_helper", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", existing: "mach-o", upgrade: true},
		{name: "macOS_preserves_custom_helper_script", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "from", existing: "script", preserveHelper: true},
		{name: "macOS_preserves_helper_with_host_only_URL", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "host-url", preserveHelper: true},
		{name: "macOS_updates_helper_with_explicit_Linux_URL", hostOS: "darwin", arch: "arm64", unameArch: "arm64", source: "template"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.launchd && (!tc.missingReplacementHelper || tc.relativeServiceTemplate) && runtime.GOOS != "darwin" {
				t.Skip("requires macOS plutil")
			}
			fixtureHome := t.TempDir()
			bundle := filepath.Join(fixtureHome, "artifact bundle")
			bin := filepath.Join(fixtureHome, "bin")
			if tc.helperAliasesRunner {
				bin = filepath.Join(bundle, "linux", tc.arch)
			}
			if err := os.MkdirAll(bin, 0o755); err != nil {
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
				cpu := "\x0c\x00\x00\x01"
				if tc.arch == "amd64" {
					cpu = "\x07\x00\x00\x01"
				}
				hostRunner = "\xcf\xfa\xed\xfe" + cpu + " macOS runner"
			}
			if tc.source == "from" {
				hostRunner += " custom"
			}
			oldHost := "\xcf\xfa\xed\xfe\x07\x00\x00\x01 old Intel Runner"
			if tc.universal || tc.previousUniversal {
				const x8664, arm64 = 0x01000007, 0x0100000c
				header := make([]byte, 8+2*20)
				binary.BigEndian.PutUint32(header[:], 0xcafebabe)
				binary.BigEndian.PutUint32(header[4:], 2)
				binary.BigEndian.PutUint32(header[8:], x8664)
				binary.BigEndian.PutUint32(header[28:], arm64)
				if tc.previousUniversal {
					oldHost = string(header) + "old universal runner"
				}
				if tc.universal {
					hostRunner = string(header) + "universal runner"
				}
				arm64Hardware := 1
				if tc.intelHardware {
					arm64Hardware = 0
				}
				sysctl := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %d\n", arm64Hardware)
				if err := os.WriteFile(filepath.Join(bin, "sysctl"), []byte(sysctl), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			oldHelperMode := os.FileMode(0o755)
			switch tc.existing {
			case "mach-o":
				oldHelper = "\xcf\xfa\xed\xfe wrong OS"
			case "script":
				oldHelper = "#!/bin/sh\nexec /opt/runner \"$@\"\n"
			case "non-executable":
				oldHelperMode = 0o644
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				targetOS, arch := req.URL.Query().Get("os"), req.URL.Query().Get("arch")
				if (arch != tc.arch && arch != tc.unameArch && !(tc.universal && arch == "amd64" && !tc.arm64OnlyArtifacts)) ||
					(targetOS != tc.hostOS && targetOS != "linux") {
					http.NotFound(w, req)
					return
				}
				switch req.URL.Path {
				case "/runners/agentflow-runner":
					if targetOS == tc.hostOS {
						if tc.source == "checkout" {
							http.NotFound(w, req)
							return
						}
						fmt.Fprint(w, hostRunner)
						return
					}
					if tc.missingReplacementHelper && arch == "arm64" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					switch tc.helperArtifact {
					case "unavailable":
						http.NotFound(w, req)
					case "mach-o":
						fmt.Fprint(w, hostRunner)
					case "wrong-arch":
						fmt.Fprint(w, bindFixtureLinuxRunner("arm64", "new helper"))
					case "truncated-header":
						fmt.Fprint(w, linuxRunner[:64])
					case "truncated-segment":
						fmt.Fprint(w, linuxRunner[:len(linuxRunner)-1])
					default:
						fmt.Fprint(w, bindFixtureLinuxRunner(arch, "new helper"))
					}
				case "/runners/agentflow-gate-mcp":
					if tc.missingRequiredGate && targetOS == "darwin" && arch == "arm64" {
						http.NotFound(w, req)
						return
					}
					fmt.Fprintf(w, "gate %s/%s", targetOS, arch)
				default:
					http.NotFound(w, req)
				}
			}))
			defer server.Close()
			args := []string{script, "--bin-dir", bin, "--no-start"}
			helper := filepath.Join(bin, "agentflow-runner-linux-"+tc.arch)
			patternEnv := ""
			if tc.templatePath {
				patternEnv = filepath.Join(bundle, "{os}", "{arch}", "agentflow-runner")
				helper = filepath.Join(bundle, "linux", tc.arch, "agentflow-runner")
			}
			if tc.templateMissingArch {
				patternEnv = filepath.Join(bundle, "{os}", "agentflow-runner")
				helper = filepath.Join(bundle, "linux", "agentflow-runner")
				oldHelper = bindFixtureLinuxRunner("amd64", "original Intel helper")
			}
			if tc.missingReplacementHelper {
				oldHelper = bindFixtureLinuxRunner("amd64", "existing Intel helper")
				helper = filepath.Join(bin, "agentflow-runner-linux-amd64")
				if tc.templatePath {
					helper = filepath.Join(bundle, "linux", "amd64", "agentflow-runner")
				}
			}
			workingDir := filepath.Join(fixtureHome, "service directory")
			if tc.relativeServiceTemplate {
				helper = filepath.Join(workingDir, "relative helpers", "linux", "amd64", "agentflow-runner")
			}
			if tc.sharedTemplateDir {
				amd64 := filepath.Join(bundle, "linux", "amd64")
				if err := os.MkdirAll(amd64, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(amd64, filepath.Join(bundle, "linux", "arm64")); err != nil {
					t.Fatal(err)
				}
			}
			statePath := filepath.Join(fixtureHome, ".agentflow-runner", "state.json")
			const state = `{"runnerId":"fixture"}`
			envPath := filepath.Join(filepath.Dir(statePath), "env")
			initialEnv := "AGENTFLOW_SERVER_URL=" + server.URL + "\n"
			if tc.upgrade {
				if err := os.Mkdir(filepath.Dir(statePath), 0o700); err != nil {
					t.Fatal(err)
				}
				if tc.templateMissingArch {
					initialEnv += "AGENTFLOW_RUNNER_BINARY=" + patternEnv + "\n"
					patternEnv = "" // Use the saved configuration, with no process override.
				}
				if tc.trimSavedTemplate {
					initialEnv += "AGENTFLOW_RUNNER_BINARY=  " + patternEnv + "  \n"
					patternEnv = ""
				}
				for path, data := range map[string]string{
					statePath:                              state,
					envPath:                                initialEnv,
					filepath.Join(bin, "agentflow-runner"): oldHost,
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
			plist := filepath.Join(fixtureHome, "Library", "LaunchAgents", "com.agentflow.runner.plist")
			oldServiceArgs := []string{filepath.Join(bin, "agentflow-runner"), "-labels", "fixture", "-auto-update=true"}
			if tc.missingReplacementHelper {
				oldServiceArgs[len(oldServiceArgs)-1] = "-auto-update=false"
			}
			initialPlist := ""
			if tc.launchd {
				if err := os.MkdirAll(filepath.Dir(plist), 0o700); err != nil {
					t.Fatal(err)
				}
				serviceConfig := ""
				if tc.relativeServiceTemplate {
					serviceConfig = fmt.Sprintf("<key>WorkingDirectory</key><string>%s</string><key>EnvironmentVariables</key><dict><key>AGENTFLOW_RUNNER_BINARY</key><string>relative helpers/{os}/{arch}/agentflow-runner</string></dict>", workingDir)
				}
				initialPlist = fmt.Sprintf("<plist version=\"1.0\"><dict><key>KeepAlive</key><true/><key>ProgramArguments</key><array><string>%s</string></array>%s</dict></plist>", strings.Join(oldServiceArgs, "</string><string>"), serviceConfig)
				if err := os.WriteFile(plist, []byte(initialPlist), 0o600); err != nil {
					t.Fatal(err)
				}
				launchctl := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$HOME/launchctl.log\"\nif [ \"$1\" = load ]; then cp \"$2\" \"$HOME/reloaded.plist\"; fi\n"
				if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte(launchctl), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			oldGate := filepath.Join(bin, "agentflow-gate-mcp-linux-amd64")
			if tc.missingReplacementHelper {
				if err := os.WriteFile(oldGate, []byte("original Intel Gate"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if (tc.upgrade && !tc.templatePath) || tc.source == "template" || tc.preserveHelper || tc.templateMissingArch || tc.missingReplacementHelper {
				if err := os.MkdirAll(filepath.Dir(helper), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(helper, []byte(oldHelper), oldHelperMode); err != nil {
					t.Fatal(err)
				}
			}
			if tc.source == "checkout" {
				if err := os.MkdirAll(filepath.Join(fixtureHome, "cmd", "agentflow-runner"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixtureHome, "source-host"), []byte(hostRunner), 0o755); err != nil {
					t.Fatal(err)
				}
				goBuild := "#!/bin/sh\n[ \"$GOOS\" = darwin ] && cp \"$HOME/source-host\" \"$3\"\n"
				if err := os.WriteFile(filepath.Join(bin, "go"), []byte(goBuild), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			install := func() string {
				t.Helper()
				cmd := exec.Command("/bin/bash", args...)
				cmd.Dir = fixtureHome
				cmd.Env = []string{"HOME=" + fixtureHome, "PATH=" + bin + ":" + filepath.Dir(curl) + ":/usr/bin:/bin", "AGENTFLOW_BROWSER=off"}
				if patternEnv != "" {
					cmd.Env = append(cmd.Env, "AGENTFLOW_RUNNER_BINARY="+patternEnv)
				}
				out, err := cmd.CombinedOutput()
				if tc.helperAliasesRunner {
					if err == nil || !strings.Contains(string(out), "helper 路径与 macOS Runner 相同") {
						t.Fatalf("installer did not reject the conflicting path: %v\n%s", err, out)
					}
				} else if tc.sharedTemplateDir {
					if err == nil || !strings.Contains(string(out), "两种架构的 Linux Git/MCP helper 路径相同") {
						t.Fatalf("installer did not reject the shared template path: %v\n%s", err, out)
					}
				} else if tc.templateMissingArch {
					if err == nil || !strings.Contains(string(out), "请在 AGENTFLOW_RUNNER_BINARY 中添加 {arch}") {
						t.Fatalf("installer did not reject the old single-architecture template: %v\n%s", err, out)
					}
				} else if tc.missingRequiredGate {
					if err == nil || !strings.Contains(string(out), "平台未提供 Gate MCP darwin/arm64") {
						t.Fatalf("installer did not report the missing required Gate helper: %v\n%s", err, out)
					}
				} else if tc.missingReplacementHelper {
					if err == nil || !strings.Contains(string(out), "缺少升级所需的 Linux/arm64 Git/MCP helper") {
						t.Fatalf("installer did not preserve the working runner: %v\n%s", err, out)
					}
				} else if err != nil {
					t.Fatalf("install: %v\n%s", err, out)
				}
				return string(out)
			}
			output := install()
			if tc.templateMissingArch || tc.missingRequiredGate || tc.missingReplacementHelper {
				preserved := map[string]string{
					filepath.Join(bin, "agentflow-runner"): oldHost,
					helper:                                 oldHelper,
					envPath:                                initialEnv,
					statePath:                              state,
				}
				if tc.launchd {
					preserved[plist] = initialPlist
				}
				if tc.missingReplacementHelper {
					preserved[oldGate] = "original Intel Gate"
				}
				for path, want := range preserved {
					if got, err := os.ReadFile(path); err != nil || string(got) != want {
						t.Fatalf("rejected upgrade changed %s: %q, %v", filepath.Base(path), got, err)
					}
				}
				if _, err := os.Stat(filepath.Join(fixtureHome, "launchctl.log")); !os.IsNotExist(err) {
					t.Fatalf("rejected upgrade restarted the service: %v", err)
				}
				return
			}
			if tc.universal && !tc.intelHardware && !tc.sharedTemplateDir &&
				!strings.Contains(output, "/usr/bin/arch -arm64 ") {
				t.Fatalf("the manual launch command can inherit Rosetta: %s", output)
			}
			if tc.sharedTemplateDir {
				if _, err := os.Stat(helper); !os.IsNotExist(err) {
					t.Fatalf("the installer wrote a helper before rejecting the conflicting template: %v", err)
				}
				return
			}
			if tc.helperAliasesRunner {
				if _, err := os.Stat(helper); !os.IsNotExist(err) {
					t.Fatalf("installer published the runner before rejecting its conflicting helper path: %v", err)
				}
				return
			}
			if tc.source == "checkout" && !strings.Contains(output, "-auto-update=false") {
				t.Fatalf("local source fallback did not disable automatic updates: %s", output)
			}
			if tc.templatePath && !tc.upgrade {
				var start string
				for line := range strings.SplitSeq(output, "\n") {
					if command, ok := strings.CutPrefix(line, "[bind-cli]   "); ok && strings.Contains(command, " -state ") {
						start = command
					}
				}
				if start == "" {
					t.Fatalf("manual launch command missing: %s", output)
				}
				if err := os.WriteFile(filepath.Join(bin, "agentflow-runner"), []byte("#!/bin/sh\nprintf '%s' \"$AGENTFLOW_RUNNER_BINARY\"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("/bin/bash", "-c", start)
				cmd.Env = []string{"HOME=" + fixtureHome, "PATH=/usr/bin:/bin"}
				if out, err := cmd.CombinedOutput(); err != nil || string(out) != patternEnv {
					t.Fatalf("manual launch got helper template %q, %v, want %q", out, err, patternEnv)
				}
				if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(helper); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--upgrade")
				patternEnv = ""
				install()
			}
			if tc.launchd {
				if tc.templatePath {
					// Use the saved service path during another upgrade without a shell override.
					if err := os.Remove(helper); err != nil {
						t.Fatal(err)
					}
					patternEnv = ""
				}
				install() // The same upgrade must keep only one disabling argument.
				out, err := exec.Command("/usr/bin/plutil", "-extract", "ProgramArguments", "json", "-o", "-", filepath.Join(fixtureHome, "reloaded.plist")).CombinedOutput()
				if err != nil {
					t.Fatalf("read reloaded launchd arguments: %v %s", err, out)
				}
				var got []string
				if err := json.Unmarshal(out, &got); err != nil || !slices.Equal(got, append(oldServiceArgs, "-auto-update=false")) {
					t.Fatalf("launchd started with %q: %v", got, err)
				}
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
			if tc.universal && !tc.intelHardware && !tc.arm64OnlyArtifacts {
				assertArtifact(filepath.Join(bin, "agentflow-runner-linux-amd64"), bindFixtureLinuxRunner("amd64", "new helper"))
				assertArtifact(filepath.Join(bin, "agentflow-gate-mcp-darwin-amd64"), "gate darwin/amd64")
				assertArtifact(filepath.Join(bin, "agentflow-gate-mcp-darwin-arm64"), "gate darwin/arm64")
			}
			if tc.intelHardware {
				if _, err := os.Stat(filepath.Join(bin, "agentflow-runner-linux-arm64")); !os.IsNotExist(err) {
					t.Fatalf("Intel install wrote an arm64 helper: %v", err)
				}
			}
			if tc.arm64OnlyArtifacts {
				if _, err := os.Stat(filepath.Join(bin, "agentflow-gate-mcp-linux-amd64")); !os.IsNotExist(err) {
					t.Fatalf("missing optional artifact changed the amd64 Gate helper: %v", err)
				}
			}
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
			if tc.templatePath {
				if _, err := os.Stat(filepath.Join(bin, "agentflow-runner-linux-"+tc.arch)); !os.IsNotExist(err) {
					t.Fatalf("the installer also wrote the fallback helper path: %v", err)
				}
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
	const elfHeaderSize, programHeaderSize = 64, 56
	header := make([]byte, elfHeaderSize+programHeaderSize)
	copy(header, "\x7fELF\x02\x01\x01\x00")
	machine := elf.EM_X86_64
	if arch == "arm64" {
		machine = elf.EM_AARCH64
	}
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(header[18:], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint64(header[32:], elfHeaderSize)
	binary.LittleEndian.PutUint16(header[52:], elfHeaderSize)
	binary.LittleEndian.PutUint16(header[54:], programHeaderSize)
	binary.LittleEndian.PutUint16(header[56:], 1)
	program := header[elfHeaderSize:]
	binary.LittleEndian.PutUint32(program, uint32(elf.PT_LOAD))
	binary.LittleEndian.PutUint32(program[4:], uint32(elf.PF_R|elf.PF_X))
	size := uint64(len(header) + len(payload))
	binary.LittleEndian.PutUint64(program[32:], size)
	binary.LittleEndian.PutUint64(program[40:], size)
	return string(header) + payload
}
