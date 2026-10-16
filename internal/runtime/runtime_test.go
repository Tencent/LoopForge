package runtime

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

type capturingSandbox struct {
	workspace   string
	driver      string
	argv        []string
	environment map[string]string
}

func TestLocalWorkspaceGitDiscoveryCannotReachParent(t *testing.T) {
	parent := t.TempDir()
	if out, err := exec.Command("git", "-C", parent, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	workspace := filepath.Join(parent, "run")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	sandbox := &localSandbox{workspace: workspace}
	if _, err := sandbox.Exec(context.Background(), CommandSpec{Argv: []string{"git", "rev-parse", "--show-toplevel"}}, nil); err == nil {
		t.Fatal("run discovered platform checkout")
	}
	checkout := filepath.Join(workspace, "source")
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", checkout, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if out, err := sandbox.Exec(context.Background(), CommandSpec{Argv: []string{"git", "-C", checkout, "rev-parse", "--show-toplevel"}}, nil); err != nil || strings.TrimSpace(out) != checkout {
		t.Fatalf("child checkout unavailable: %v %s", err, out)
	}
}

func (s *capturingSandbox) ID() string        { return "capture" }
func (s *capturingSandbox) Workspace() string { return s.workspace }
func (s *capturingSandbox) Driver() string    { return s.driver }
func (s *capturingSandbox) IsolationEnv() map[string]string {
	return s.environment
}
func (s *capturingSandbox) Close(context.Context) error {
	return nil
}
func (s *capturingSandbox) Exec(_ context.Context, command CommandSpec, _ EventSink) (string, error) {
	s.argv = append([]string(nil), command.Argv...)
	return "", nil
}

func TestResolveMapExpandsBearerSecret(t *testing.T) {
	t.Setenv("AGENTFLOW_MCP_TOKEN", "tai_test_token")
	got := resolveMap(map[string]string{
		"Authorization": "Bearer ${secret:AGENTFLOW_MCP_TOKEN}",
		"X-Keep":        "plain",
	})
	if got["Authorization"] != "Bearer tai_test_token" {
		t.Fatalf("Authorization = %q", got["Authorization"])
	}
	if got["X-Keep"] != "plain" {
		t.Fatalf("X-Keep = %q", got["X-Keep"])
	}
}

func TestResolveMapOmitsUnresolvedSecret(t *testing.T) {
	t.Setenv("AGENTFLOW_MCP_TOKEN", "")
	got := resolveMap(map[string]string{
		"Authorization": "Bearer ${secret:AGENTFLOW_MCP_TOKEN}",
		"X-Keep":        "plain",
	})
	if _, ok := got["Authorization"]; ok {
		t.Fatalf("unresolved secret must not be written: %v", got)
	}
	if got["X-Keep"] != "plain" {
		t.Fatalf("X-Keep = %q", got["X-Keep"])
	}
}

func TestMaterializeExpandsMCPBearerSecret(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTFLOW_MCP_TOKEN", "tai_live")
	_, err := materialize(dir, "secret-ok", domain.ExecutorDefinition{Kind: "codebuddy"}, domain.AgentDefinition{ID: "checker", Name: "Checker"}, domain.AgentAssetBundle{
		MCPServers: []domain.MCPServerDefinition{{
			ID: "gongfeng-delivery", Name: "工蜂", Transport: "http",
			URL:     "https://mcpgw.knot.example.com/gongfeng",
			Headers: map[string]string{"Authorization": "Bearer ${secret:AGENTFLOW_MCP_TOKEN}"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".agentflow", "checker", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "${secret:") {
		t.Fatalf("mcp.json still has placeholder: %s", text)
	}
	if !strings.Contains(text, "Bearer tai_live") {
		t.Fatalf("mcp.json missing resolved bearer: %s", text)
	}
}

func TestMaterializeFailsWhenMCPSecretMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTFLOW_MCP_TOKEN", "")
	_, err := materialize(dir, "secret-missing", domain.ExecutorDefinition{Kind: "codebuddy"}, domain.AgentDefinition{ID: "checker", Name: "Checker"}, domain.AgentAssetBundle{
		MCPServers: []domain.MCPServerDefinition{{
			ID: "gongfeng-delivery", Name: "工蜂", Transport: "http",
			URL:     "https://mcpgw.knot.example.com/gongfeng",
			Headers: map[string]string{"Authorization": "Bearer ${secret:AGENTFLOW_MCP_TOKEN}"},
		}},
	})
	if err == nil {
		t.Fatal("expected materialize to fail when MCP secret is empty")
	}
	if !strings.Contains(err.Error(), "AGENTFLOW_MCP_TOKEN") && !strings.Contains(err.Error(), "${secret:") && !strings.Contains(err.Error(), "Headers JSON") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMaterializeUsesPlainMCPHeaders(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTFLOW_MCP_TOKEN", "")
	_, err := materialize(dir, "plain-headers", domain.ExecutorDefinition{Kind: "codebuddy"}, domain.AgentDefinition{ID: "checker", Name: "Checker"}, domain.AgentAssetBundle{
		MCPServers: []domain.MCPServerDefinition{{
			ID: "gongfeng-delivery", Name: "工蜂", Transport: "http",
			URL:     "https://mcpgw.knot.example.com/gongfeng",
			Headers: map[string]string{"Authorization": "Bearer tai_plain_in_mcp"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".agentflow", "checker", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "${secret:") {
		t.Fatalf("mcp.json still has placeholder: %s", text)
	}
	if !strings.Contains(text, "Bearer tai_plain_in_mcp") {
		t.Fatalf("mcp.json missing plaintext bearer: %s", text)
	}
}

func TestUpsertEnvReplacesFirstAndDropsDuplicates(t *testing.T) {
	got := upsertEnv([]string{"PATH=/bin", "PWD=/old", "PWD=/stale", "LANG=C"}, "PWD", "/workspace")
	want := []string{"PATH=/bin", "PWD=/workspace", "LANG=C"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("upsertEnv = %v, want %v", got, want)
	}
}

func TestLocalSandboxAppliesProfileEnvironmentAndRuntimeWins(t *testing.T) {
	t.Setenv("AGENTFLOW_ENV_INHERITED", "host")
	workspace := t.TempDir()
	sandbox := &localSandbox{
		id:          "environment-test",
		workspace:   workspace,
		environment: map[string]string{"AGENTFLOW_ENV_PROFILE": "profile", "AGENTFLOW_ENV_OVERRIDE": "profile"},
	}
	output, err := sandbox.Exec(context.Background(), CommandSpec{
		Argv:        []string{"sh", "-c", `printf '%s|%s|%s' "$AGENTFLOW_ENV_INHERITED" "$AGENTFLOW_ENV_PROFILE" "$AGENTFLOW_ENV_OVERRIDE"`},
		environment: map[string]string{"AGENTFLOW_ENV_OVERRIDE": "runtime"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(output); got != "host|profile|runtime" {
		t.Fatalf("sandbox environment = %q, want host|profile|runtime", got)
	}
}

func TestDockerSandboxEnvironmentUsesRuntimePrecedence(t *testing.T) {
	got := appendEnvironment(nil,
		map[string]string{"PROFILE_ONLY": "profile", "OVERRIDE": "profile"},
		map[string]string{"RUNTIME_ONLY": "runtime", "OVERRIDE": "runtime"},
	)
	want := []string{"OVERRIDE=runtime", "PROFILE_ONLY=profile", "RUNTIME_ONLY=runtime"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Docker environment = %v, want %v", got, want)
	}
}

func TestDockerBindMatchesWorkspace(t *testing.T) {
	dir := t.TempDir()
	if !dockerBindMatchesWorkspace(dir, dir) {
		t.Fatal("same path should match")
	}
	if dockerBindMatchesWorkspace(dir, filepath.Join(dir, "other")) {
		t.Fatal("different path must not match")
	}
	if dockerBindMatchesWorkspace("", dir) {
		t.Fatal("empty bind source must not match")
	}
}

func TestDockerContainerReusableWhenBindMatches(t *testing.T) {
	dir := t.TempDir()
	if !dockerContainerReusable(dir, nil, dir) {
		t.Fatal("matching bind should be reused after worker restart")
	}
	if dockerContainerReusable(dir, fmt.Errorf("inspect failed"), dir) {
		t.Fatal("inspect failure must not reuse")
	}
	if dockerContainerReusable(filepath.Join(dir, "other"), nil, dir) {
		t.Fatal("mismatched bind must not reuse")
	}
}

func TestIsSandboxInterrupt(t *testing.T) {
	if IsSandboxInterrupt(nil) {
		t.Fatal("nil")
	}
	if !IsSandboxInterrupt(context.Canceled) {
		t.Fatal("context canceled")
	}
	if !IsSandboxInterrupt(fmt.Errorf("Error response from daemon: No such exec instance: abc")) {
		t.Fatal("missing exec instance")
	}
	if !IsSandboxInterrupt(fmt.Errorf("signal: killed")) {
		t.Fatal("signal killed")
	}
	if IsSandboxInterrupt(fmt.Errorf("verification lint command failed")) {
		t.Fatal("business failure is not an interrupt")
	}
	if IsSandboxInterrupt(fmt.Errorf("exit status 1")) {
		t.Fatal("bare exit status 1 is a business failure")
	}
	wrapped := annotateDockerExecError(
		"Error response from daemon: No such exec instance: abc\n",
		fmt.Errorf("exit status 1"),
	)
	if !IsSandboxInterrupt(wrapped) {
		t.Fatalf("annotated exec-gone should interrupt, got %v", wrapped)
	}
}

func TestAnnotateDockerExecErrorKeepsDaemonLine(t *testing.T) {
	plain := fmt.Errorf("exit status 1")
	if got := annotateDockerExecError("lint failed\n", plain); got != plain {
		t.Fatalf("business output must not be annotated: %v", got)
	}
	got := annotateDockerExecError("Error response from daemon: No such exec instance: deadbeef\n", plain)
	if got == nil || !strings.Contains(strings.ToLower(got.Error()), "no such exec instance") {
		t.Fatalf("want daemon line on error, got %v", got)
	}
	already := fmt.Errorf("Error response from daemon: No such exec instance: abc")
	if got := annotateDockerExecError("", already); got != already {
		t.Fatalf("already classified error must stay unchanged")
	}
}

func TestAnnotateDockerExecErrorIgnoresAgentOutput(t *testing.T) {
	plain := fmt.Errorf("exit status 1")
	logged := "FAIL TestDeploy: expected \"No such container: web\" but got nil\n"
	if got := annotateDockerExecError(logged, plain); got != plain {
		t.Fatalf("agent log mentioning docker errors must not park the node: %v", got)
	}
	stale := "Error response from daemon: No such container: old\n" + strings.Repeat("build step\n", 20)
	if got := annotateDockerExecError(stale, plain); got != plain {
		t.Fatalf("daemon line far above the failure must not park the node: %v", got)
	}
}

func TestPrepareDockerfileBuildContextWritesRecipeAndFiles(t *testing.T) {
	workspace := t.TempDir()
	dir, err := prepareDockerfileBuildContext(workspace, domain.DockerfileDefinition{
		ID:      "agent-runtime",
		Content: "FROM debian:bookworm-slim\n",
		Files:   map[string]string{"packages.txt": "git\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil || !strings.Contains(string(raw), "FROM debian:bookworm-slim") {
		t.Fatalf("dockerfile not written: %s %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "packages.txt")); err != nil {
		t.Fatal(err)
	}
	if got := dockerBuildArgs("agentflow-tmp-run1", dir); len(got) != 4 || got[0] != "build" || got[2] != "agentflow-tmp-run1" {
		t.Fatalf("docker build args = %v", got)
	}
	if tempDockerImageName("run 1") != "agentflow-tmp-run-1" && !strings.HasPrefix(tempDockerImageName("run1"), "agentflow-tmp-") {
		t.Fatalf("temp image name = %q", tempDockerImageName("run1"))
	}
}

func TestCachedDockerImageNameIgnoresCatalogMetadata(t *testing.T) {
	a := domain.DockerfileDefinition{
		ID:      "dnspod-php-backend",
		Name:    "PHP",
		Content: "FROM node:22-bookworm-slim\n",
		Files:   map[string]string{"conf/php.ini": "memory_limit=1G\n", "conf/www.conf": "user=nobody\n"},
	}
	b := domain.DockerfileDefinition{
		ID:      "other-id",
		Name:    "Other",
		Content: a.Content,
		Files:   map[string]string{"conf/www.conf": "user=nobody\n", "conf/php.ini": "memory_limit=1G\n"},
	}
	got := cachedDockerImageName(a)
	if !strings.HasPrefix(got, dockerCachedImagePrefix) || len(got) != len(dockerCachedImagePrefix)+16 {
		t.Fatalf("cached image name = %q", got)
	}
	if cachedDockerImageName(b) != got {
		t.Fatalf("same recipe must share cache tag: %q vs %q", got, cachedDockerImageName(b))
	}
	if DockerImageCached(context.Background(), domain.DockerfileDefinition{}) {
		t.Fatal("empty dockerfile must not look cached")
	}
	if DockerImageCached(context.Background(), a) {
		t.Fatal("missing cache tag must not look cached")
	}
	b.Files["conf/php.ini"] = "memory_limit=2G\n"
	if cachedDockerImageName(b) == got {
		t.Fatal("file change must bust the cache tag")
	}
	if ephemeralDockerImage(got) {
		t.Fatalf("cached image %q must survive run teardown", got)
	}
	if !ephemeralDockerImage(tempDockerImageName("run-1")) {
		t.Fatal("legacy per-run tags must still be removed")
	}
}

func TestStaleCachedDockerImagesKeepsCatalogAndInUseTags(t *testing.T) {
	keep := map[string]struct{}{"agentflow-img-livehash": {}}
	inUse := map[string]struct{}{"agentflow-img-building": {}}
	listed := []string{"agentflow-img-livehash", "agentflow-img-building", "agentflow-img-oldhash", "debian"}
	got := staleCachedDockerImages(listed, keep, inUse)
	if len(got) != 1 || got[0] != "agentflow-img-oldhash" {
		t.Fatalf("stale = %v, want only oldhash", got)
	}
	parsed := parseCachedDockerImageNames("agentflow-img-abc\nagentflow-img-abc:latest\nnode:22-bookworm-slim\n")
	if len(parsed) != 1 || parsed[0] != "agentflow-img-abc" {
		t.Fatalf("parsed = %v", parsed)
	}
}

func TestCreateDockerRequiresDockerfile(t *testing.T) {
	_, err := createDocker(context.Background(), "id", t.TempDir(), domain.SandboxProfile{Driver: "docker"}, SandboxRequirements{}, domain.DockerfileDefinition{})
	if err == nil || !strings.Contains(err.Error(), "dockerfile") {
		t.Fatalf("empty docker recipe should fail, got %v", err)
	}
}

func TestPrepareEnvironmentWritesInstallScriptAndPrependsPath(t *testing.T) {
	workspace := t.TempDir()
	catalog := domain.EnvironmentDefinition{
		ID: "php7.4.33",
		Variables: map[string]string{
			"PHP_VERSION":   "7.4.33",
			"COMPOSER_HOME": "${PREFIX}/composer",
		},
		PathEntries:   []string{"bin"},
		InstallScript: "mkdir -p \"$PREFIX/bin\"\nprintf '#!/bin/sh\\necho php\\n' > \"$PREFIX/bin/php\"\nchmod +x \"$PREFIX/bin/php\"\n",
	}
	activated, err := prepareSandboxEnvironment(workspace, domain.SandboxProfile{Driver: "seatbelt", EnvironmentID: "php7.4.33"}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(workspace, ".agentflow", "environment", "php7.4.33")
	if activated.Overlay[environmentIDVar] != "php7.4.33" || activated.Overlay[environmentRootVar] != root || activated.Overlay[environmentPrefixVar] != root {
		t.Fatalf("overlay identity = %+v", activated.Overlay)
	}
	if activated.Overlay["PHP_VERSION"] != "7.4.33" {
		t.Fatalf("catalog variables missing: %+v", activated.Overlay)
	}
	if activated.Overlay["COMPOSER_HOME"] != filepath.Join(root, "composer") {
		t.Fatalf("PREFIX placeholder was not expanded: %+v", activated.Overlay)
	}
	if !strings.HasPrefix(activated.Overlay["PATH"], filepath.Join(root, "bin")+string(os.PathListSeparator)) {
		t.Fatalf("PATH was not prepended with env bin: %q", activated.Overlay["PATH"])
	}
	zdot := filepath.Join(root, environmentZdotDir)
	if activated.Overlay["ZDOTDIR"] != zdot || activated.Overlay["BASH_ENV"] == "" || activated.Overlay["ENV"] != activated.Overlay["BASH_ENV"] {
		t.Fatalf("login-shell hooks missing: %+v", activated.Overlay)
	}
	for _, name := range []string{".zshenv", ".zprofile", ".zshrc"} {
		raw, err := os.ReadFile(filepath.Join(zdot, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), filepath.Join(root, "bin")) {
			t.Fatalf("%s did not pin env bin: %s", name, raw)
		}
	}
	if _, err := os.Stat(filepath.Join(root, installScriptName)); err != nil {
		t.Fatal(err)
	}
	if activated, err := prepareSandboxEnvironment(workspace, domain.SandboxProfile{Driver: "docker", EnvironmentID: "php7.4.33"}, catalog); err != nil || activated.Overlay != nil {
		t.Fatalf("docker should ignore process environments: %v %+v", err, activated)
	}
}

func TestActivateEnvironmentUsesWorkspaceDropInForProcessSandboxes(t *testing.T) {
	workspace := t.TempDir()
	dropin := filepath.Join(workspace, "agentflow-environment")
	if err := os.MkdirAll(filepath.Join(dropin, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if activated, err := prepareSandboxEnvironment(workspace, domain.SandboxProfile{Driver: "local"}, domain.EnvironmentDefinition{}); err != nil || activated.Overlay != nil {
		t.Fatalf("local should not auto-activate drop-in: %v %+v", err, activated)
	}
	activated, err := prepareSandboxEnvironment(workspace, domain.SandboxProfile{Driver: "bwrap"}, domain.EnvironmentDefinition{})
	if err != nil {
		t.Fatal(err)
	}
	if activated.Overlay[environmentIDVar] != workspaceEnvironmentID || activated.Overlay[environmentRootVar] != dropin {
		t.Fatalf("bwrap drop-in overlay = %+v", activated.Overlay)
	}
}

func TestLoginZshRePrependsEnvironmentBins(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	workspace := t.TempDir()
	catalog := domain.EnvironmentDefinition{ID: "golang", PathEntries: []string{"bin", "go/bin"}, InstallScript: "mkdir -p \"$PREFIX/bin\""}
	activated, err := prepareSandboxEnvironment(workspace, domain.SandboxProfile{Driver: "seatbelt", EnvironmentID: "golang"}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	envBin := filepath.Join(activated.Root, "bin")
	cmd := exec.Command(zsh, "-lc", `printf '%s' "$PATH"`)
	cmd.Env = []string{
		"PATH=/opt/homebrew/bin:/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"ZDOTDIR=" + activated.Overlay["ZDOTDIR"],
		"PREFIX=" + activated.Root,
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.HasPrefix(got, envBin+string(os.PathListSeparator)) && !strings.HasPrefix(got, envBin+":") {
		t.Fatalf("login zsh PATH should start with env bin, got %q", got)
	}
	if i := strings.Index(got, "/opt/homebrew/bin"); i >= 0 {
		if strings.Index(got, envBin) > i {
			t.Fatalf("homebrew still precedes env bin: %q", got)
		}
	}
}

func TestLoginZshWithoutZDOTDIRLosesToUsrBin(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	if _, err := os.Stat("/usr/bin/java"); err != nil {
		t.Skip("no /usr/bin/java stub")
	}
	workspace := t.TempDir()
	catalog := domain.EnvironmentDefinition{
		ID:            "java8",
		PathEntries:   []string{"bin"},
		InstallScript: "mkdir -p \"$PREFIX/bin\"\nprintf '#!/bin/sh\\necho prefix-java\\n' > \"$PREFIX/bin/java\"\nchmod +x \"$PREFIX/bin/java\"\n",
	}
	activated, err := prepareSandboxEnvironment(workspace, domain.SandboxProfile{Driver: "seatbelt", EnvironmentID: "java8"}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(activated.Root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activated.Root, "bin", "java"), []byte("#!/bin/sh\necho prefix-java\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prefixJava := filepath.Join(activated.Root, "bin", "java")
	home := t.TempDir()
	path := "/usr/bin" + string(os.PathListSeparator) + filepath.Join(activated.Root, "bin")
	without := exec.Command(zsh, "-lc", `command -v java`)
	without.Env = []string{"PATH=" + path, "HOME=" + home, "PREFIX=" + activated.Root}
	out, err := without.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "/usr/bin/java" && !strings.HasPrefix(got, "/usr/bin/") {
		t.Fatalf("login zsh without ZDOTDIR should resolve host java, got %q", got)
	}
	with := exec.Command(zsh, "-lc", `command -v java`)
	with.Env = []string{
		"PATH=" + path,
		"HOME=" + home,
		"PREFIX=" + activated.Root,
		"ZDOTDIR=" + activated.Overlay["ZDOTDIR"],
	}
	out, err = with.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != prefixJava {
		t.Fatalf("login zsh with ZDOTDIR should resolve prefix java, got %q", got)
	}
}

func TestWithCodexIsolationEnvInsertsSetBeforePrompt(t *testing.T) {
	argv := []string{"codex", "exec", "-c", `shell_environment_policy.inherit="core"`, "do work"}
	got := withCodexIsolationEnv(argv, map[string]string{
		"ZDOTDIR":                 "/tmp/zdot",
		"PREFIX":                  "/tmp/prefix",
		"AGENTFLOW_GIT_TOKEN":     "secret",
		"CODEBUDDY_API_KEY":       "ck",
		"GIT_CEILING_DIRECTORIES": "/tmp/workspaces",
	})
	if !containsArgPair(got, "-c", `shell_environment_policy.inherit="core"`) {
		t.Fatalf("inherit=core must stay: %v", got)
	}
	if !containsArgPair(got, "-c", `shell_environment_policy.set.ZDOTDIR="/tmp/zdot"`) {
		t.Fatalf("ZDOTDIR was not injected: %v", got)
	}
	if !containsArgPair(got, "-c", `shell_environment_policy.set.PREFIX="/tmp/prefix"`) {
		t.Fatalf("PREFIX was not injected: %v", got)
	}
	joined := strings.Join(got, " ")
	if !containsArgPair(got, "-c", `shell_environment_policy.set.GIT_CEILING_DIRECTORIES="/tmp/workspaces"`) {
		t.Fatal("Codex tool shells lost the workspace git boundary")
	}
	if strings.Contains(joined, "AGENTFLOW_GIT_TOKEN") || strings.Contains(joined, "CODEBUDDY_API_KEY") {
		t.Fatalf("secrets must not be copied into Codex set: %v", got)
	}
	if got[len(got)-1] != "do work" {
		t.Fatalf("prompt must remain last: %v", got)
	}
}

func TestExecutorRunnerInjectsCodexZDOTDIR(t *testing.T) {
	sb := &capturingSandbox{
		workspace: t.TempDir(),
		driver:    "seatbelt",
		environment: map[string]string{
			"ZDOTDIR": "/tmp/env/zdot",
			"PREFIX":  "/tmp/env",
		},
	}
	ex := domain.ExecutorDefinition{Kind: "codex", Binary: "codex"}
	if _, err := NewExecutorRunner().Run(context.Background(), sb, "invoke-1", ex, domain.AgentDefinition{ID: "probe", Name: "Probe"}, domain.AgentAssetBundle{}, "probe", InvocationOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if !containsArgPair(sb.argv, "-c", `shell_environment_policy.inherit="core"`) {
		t.Fatalf("inherit=core missing: %v", sb.argv)
	}
	if !containsArgPair(sb.argv, "-c", `shell_environment_policy.set.ZDOTDIR="/tmp/env/zdot"`) {
		t.Fatalf("Codex tool shells never received ZDOTDIR: %v", sb.argv)
	}
}

func TestExpandEnvironmentPlaceholders(t *testing.T) {
	prefix := "/tmp/env-root"
	if got := expandEnvironmentPlaceholders("${PREFIX}/jdk", prefix); got != "/tmp/env-root/jdk" {
		t.Fatalf("braced PREFIX = %q", got)
	}
	if got := expandEnvironmentPlaceholders("$PREFIX/maven", prefix); got != "/tmp/env-root/maven" {
		t.Fatalf("plain PREFIX = %q", got)
	}
	if got := expandEnvironmentPlaceholders("7.4.33", prefix); got != "7.4.33" {
		t.Fatalf("literal = %q", got)
	}
}

func phpInstallScript() string {
	return `mkdir -p "$PREFIX/bin"
cat > "$PREFIX/bin/php" <<'EOF'
#!/bin/sh
echo php
EOF
chmod +x "$PREFIX/bin/php"
`
}

func TestLocalSandboxRunsInstallScriptWithoutHostPollution(t *testing.T) {
	t.Setenv("PHP_VERSION", "host-php")
	manager := NewManager(t.TempDir())
	sandbox, err := manager.AcquireRun(context.Background(), "run-php", domain.SandboxProfile{ID: "local", Driver: "local", EnvironmentID: "php8"}, SandboxRequirements{}, RunCredentials{}, SandboxResources{
		Environment: domain.EnvironmentDefinition{
			ID:            "php8",
			Variables:     map[string]string{"PHP_VERSION": "8.3.0"},
			PathEntries:   []string{"bin"},
			InstallScript: phpInstallScript(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := sandbox.Exec(context.Background(), CommandSpec{
		Argv: []string{"sh", "-c", `printf '%s|%s|%s' "$PHP_VERSION" "$AGENTFLOW_ENVIRONMENT" "$(php)"`},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(output); got != "8.3.0|php8|php" {
		t.Fatalf("process environment = %q", got)
	}
	if os.Getenv("PHP_VERSION") != "host-php" {
		t.Fatalf("host PHP_VERSION was mutated: %q", os.Getenv("PHP_VERSION"))
	}
	root := filepath.Join(sandbox.Workspace(), ".agentflow", "environment", "php8")
	if _, err := os.Stat(filepath.Join(root, "bin", "php")); err != nil {
		t.Fatal(err)
	}
	stamp, err := os.ReadFile(filepath.Join(root, installStampName))
	if err != nil || strings.TrimSpace(string(stamp)) == "" {
		t.Fatalf("install stamp missing: %s %v", stamp, err)
	}
	if err := sandbox.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	again, err := manager.AcquireRun(context.Background(), "run-php", domain.SandboxProfile{ID: "local", Driver: "local", EnvironmentID: "php8"}, SandboxRequirements{}, RunCredentials{}, SandboxResources{
		Environment: domain.EnvironmentDefinition{
			ID:            "php8",
			Variables:     map[string]string{"PHP_VERSION": "8.3.0"},
			PathEntries:   []string{"bin"},
			InstallScript: phpInstallScript(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close(context.Background()) })
}

func TestReservedAgentIDIncludesEnvironmentAndDocker(t *testing.T) {
	if !reservedAgentID("environment") || !reservedAgentID("docker") {
		t.Fatal("environment and docker must stay reserved control directories")
	}
}

func TestManagerRejectsUnknownSandboxDriver(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.Create(context.Background(), "run", "node", domain.SandboxProfile{Driver: "typo-driver"})
	if err == nil || !strings.Contains(err.Error(), "unsupported sandbox driver") {
		t.Fatalf("unknown driver should fail closed, got %v", err)
	}
}

func TestSeatbeltProfileDeniesHostWritesAndProjectAgentConfig(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	stateDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	profile, err := seatbeltProfileRequirements(workspace, ProviderRequirements("claude-code"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(profile, stateDir) {
		t.Fatal("host Claude state must not be writable by a user-credential run")
	}
	needles := []string{"(deny file-write*)", "(allow file-write*", "/private/tmp", "/dev/null", filepath.Join(workspace, "AGENTS.md"), filepath.Join(workspace, ".claude")}
	if runtime.GOOS == "darwin" {
		needles = append(needles, "/private/var/folders")
	}
	for _, needle := range needles {
		if !strings.Contains(profile, needle) {
			t.Fatalf("seatbelt profile missing %q:\n%s", needle, profile)
		}
	}
}

func TestBwrapProvidesWritableRuntimeInfrastructure(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	stateDir := filepath.Join(home, ".codebuddy")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	args, err := bwrapArgsRequirements(workspace, ProviderRequirements("codebuddy"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(args, " ")
	for _, needle := range []string{"--die-with-parent", "--tmpfs /tmp", "--dev-bind /dev /dev", "--proc /proc", "--tmpfs /$bunfs"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("bwrap args missing %q: %v", needle, args)
		}
	}
	if strings.Contains(text, stateDir) {
		t.Fatalf("CodeBuddy host state leaked into bwrap: %v", args)
	}
}

func TestDockerMountsOnlySelectedCLIStateDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{".claude", ".codex", ".codebuddy"} {
		if err := os.MkdirAll(filepath.Join(home, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		binary string
		state  string
	}{
		{binary: "claude"},
		{binary: "codex"},
		{binary: "codebuddy"},
	}
	for _, test := range tests {
		t.Run(test.binary, func(t *testing.T) {
			kind := map[string]string{"claude": "claude-code", "codex": "codex", "codebuddy": "codebuddy"}[test.binary]
			args, err := dockerAgentStateMountRequirements(ProviderRequirements(kind))
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Join(args, " ")
			if test.state != "" {
				selected := filepath.Join(home, test.state)
				if !strings.Contains(text, "src="+selected) || !strings.Contains(text, "dst=/root/"+test.state) {
					t.Fatalf("selected state directory missing from Docker mount: %v", args)
				}
			} else if len(args) != 0 {
				t.Fatalf("CodeBuddy must not mount host state: %v", args)
			}
			claudeState := filepath.Join(home, ".claude.json")
			if test.state == ".claude" {
				if !strings.Contains(text, "src="+claudeState) || !strings.Contains(text, "dst=/root/.claude.json") {
					t.Fatalf("Claude sibling state file missing from Docker mount: %v", args)
				}
			} else if strings.Contains(text, claudeState) {
				t.Fatalf("Claude sibling state file leaked into %s mount: %v", test.binary, args)
			}
			for _, other := range []string{".claude", ".codex", ".codebuddy"} {
				if other != test.state && strings.Contains(text, filepath.Join(home, other)) {
					t.Fatalf("unselected state directory %s leaked into Docker mount: %v", other, args)
				}
			}
		})
	}

	args, err := dockerAgentStateMountRequirements(SandboxRequirements{})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Fatalf("command node received Agent CLI state mount: %v", args)
	}
}

func TestProcessSandboxKeepsHostHome(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt is only available on macOS")
	}
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec is unavailable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	stateDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	fakeClaude := filepath.Join(workspace, "claude")
	if err := os.Symlink("/bin/sh", fakeClaude); err != nil {
		t.Fatal(err)
	}
	sb := &processSandbox{driver: "seatbelt", workspace: workspace, binary: "/usr/bin/sandbox-exec", requirements: providerForKind("claude-code").SandboxRequirements()}
	output, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{fakeClaude, "-lc", `printf %s "$HOME"`}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != home {
		t.Fatalf("sandbox HOME = %q, want host HOME %q", output, home)
	}
}

func TestLightweightSandboxSmoke(t *testing.T) {
	if os.Getenv("AGENTFLOW_SANDBOX_SMOKE") != "1" {
		t.Skip("set AGENTFLOW_SANDBOX_SMOKE=1 on a host that permits seatbelt/bwrap")
	}
	workspace := t.TempDir()
	hostHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	stateHome := t.TempDir()
	t.Setenv("HOME", stateHome)
	if err := os.MkdirAll(filepath.Join(stateHome, ".codebuddy"), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeCodeBuddy := filepath.Join(workspace, "codebuddy")
	if err := os.Symlink("/bin/sh", fakeCodeBuddy); err != nil {
		t.Fatal(err)
	}
	profile := domain.SandboxProfile{}
	var sb Sandbox
	var createErr error
	switch runtime.GOOS {
	case "darwin":
		sb, createErr = createSeatbelt("smoke", workspace, profile, providerForKind("codebuddy").SandboxRequirements())
	case "linux":
		sb, createErr = createBwrap("smoke", workspace, profile, providerForKind("codebuddy").SandboxRequirements())
	default:
		t.Skip("lightweight sandbox is only available on macOS/Linux")
	}
	if createErr != nil {
		t.Fatal(createErr)
	}
	allowed := `test "$HOME" != "` + stateHome + `" && test -w "$HOME" && f=$(mktemp) && printf ok > "$f" && printf ok > /dev/null && printf ok > workspace-write.txt`
	command, err := bindCommand(sb, workspace, CommandSpec{Argv: []string{fakeCodeBuddy, "-lc", allowed}, Provider: "codebuddy", PrivateHome: true}, RunCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	if output, err := sb.Exec(context.Background(), command, nil); err != nil {
		t.Fatalf("allowed runtime operations failed: %v: %s", err, output)
	}
	protected, err := os.CreateTemp(hostHome, ".agentflow-sandbox-protected-")
	if err != nil {
		t.Fatal(err)
	}
	protectedPath := protected.Name()
	_ = protected.Close()
	t.Cleanup(func() { _ = os.Remove(protectedPath) })
	if _, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{fakeCodeBuddy, "-lc", fmt.Sprintf("printf blocked >> %q", protectedPath)}}, nil); err == nil {
		t.Fatal("sandbox allowed a write outside workspace/temp/state")
	}
}

func TestBwrapAndDockerMaskRepositoryAgentConfig(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".agents", "skills", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("repo prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "services", "api", ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "services", "api", "CLAUDE.md"), []byte("nested prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	bwrap, err := bwrapAgentConfigMasks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	docker, err := dockerAgentConfigMasks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	bwrapText, dockerText := strings.Join(bwrap, " "), strings.Join(docker, " ")
	for _, target := range []string{filepath.Join(workspace, "AGENTS.md"), filepath.Join(workspace, ".agents"), filepath.Join(workspace, "services", "api", ".claude"), filepath.Join(workspace, "services", "api", "CLAUDE.md")} {
		if !strings.Contains(bwrapText, target) {
			t.Fatalf("bwrap mask missing %s: %v", target, bwrap)
		}
	}
	for _, target := range []string{"dst=/workspace/AGENTS.md", "dst=/workspace/.agents", "dst=/workspace/services/api/.claude", "dst=/workspace/services/api/CLAUDE.md"} {
		if !strings.Contains(dockerText, target) {
			t.Fatalf("docker mask missing %s: %v", target, docker)
		}
	}
	if strings.Contains(dockerText, "src="+workspace) {
		t.Fatalf("docker isolation sources nested inside workspace bind: %v", docker)
	}
}

func TestManagedSandboxGitIndexPreservesMaskedAgentConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	workspace := t.TempDir()
	testRuntimeGit(t, workspace, "init", "-b", "main")
	testRuntimeGit(t, workspace, "config", "user.email", "agentflow@test.local")
	testRuntimeGit(t, workspace, "config", "user.name", "AgentFlow Test")

	originals := map[string]string{
		"AGENTS.md":                          "root instructions\n",
		".codex/config.toml":                 "model = \"host\"\n",
		".agents/skills/repo/SKILL.md":       "repository skill\n",
		"services/api/CLAUDE.md":             "nested instructions\n",
		"services/api/.claude/settings.json": "{\"enabled\":true}\n",
	}
	for path, content := range originals {
		full := filepath.Join(workspace, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	businessPath := filepath.Join(workspace, "src", "app.txt")
	if err := os.MkdirAll(filepath.Dir(businessPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(businessPath, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testRuntimeGit(t, workspace, "add", "-A")
	testRuntimeGit(t, workspace, "commit", "-m", "baseline")
	untrackedAgentConfig := filepath.Join(workspace, "services", "worker", "CODEBUDDY.md")
	if err := os.MkdirAll(filepath.Dir(untrackedAgentConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untrackedAgentConfig, []byte("untracked local instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := ProtectProjectAgentGitIndex(ctx, workspace, "bwrap"); err != nil {
		t.Fatal(err)
	}
	for path := range originals {
		flag := testRuntimeGit(t, workspace, "ls-files", "-v", "--", path)
		if !strings.HasPrefix(flag, "S ") {
			t.Fatalf("protected path %s does not have skip-worktree set: %q", path, flag)
		}
	}
	if err := RestoreProjectAgentGitIndex(ctx, workspace, "bwrap"); err != nil {
		t.Fatal(err)
	}
	if flag := testRuntimeGit(t, workspace, "ls-files", "-v", "--", "AGENTS.md"); strings.HasPrefix(flag, "S ") {
		t.Fatalf("restore left skip-worktree set: %q", flag)
	}
	if err := ProtectProjectAgentGitIndex(ctx, workspace, "docker"); err != nil {
		t.Fatal(err)
	}

	// Emulate the filesystem view produced by the Docker/bwrap masks: files
	// become empty and protected directories contain no repository files.
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "services", "api", "CLAUDE.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".codex", ".agents", "services/api/.claude"} {
		if err := os.RemoveAll(filepath.Join(workspace, filepath.FromSlash(dir))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(untrackedAgentConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(businessPath, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	testRuntimeGit(t, workspace, "add", "-A")
	if got := testRuntimeGit(t, workspace, "diff", "--cached", "--name-only"); got != "src/app.txt" {
		t.Fatalf("masked project configuration leaked into staged diff: %q", got)
	}
	testRuntimeGit(t, workspace, "commit", "-m", "business change")
	for path, original := range originals {
		if got := testRuntimeGit(t, workspace, "show", "HEAD:"+path); got+"\n" != original {
			t.Fatalf("committed content for %s = %q, want %q", path, got, strings.TrimSuffix(original, "\n"))
		}
	}
}

func TestLocalSandboxDoesNotChangeProjectAgentGitIndex(t *testing.T) {
	workspace := t.TempDir()
	testRuntimeGit(t, workspace, "init", "-b", "main")
	testRuntimeGit(t, workspace, "config", "user.email", "agentflow@test.local")
	testRuntimeGit(t, workspace, "config", "user.name", "AgentFlow Test")
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("local instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testRuntimeGit(t, workspace, "add", "AGENTS.md")
	testRuntimeGit(t, workspace, "commit", "-m", "baseline")
	if err := ProtectProjectAgentGitIndex(context.Background(), workspace, "local"); err != nil {
		t.Fatal(err)
	}
	if flag := testRuntimeGit(t, workspace, "ls-files", "-v", "--", "AGENTS.md"); strings.HasPrefix(flag, "S ") {
		t.Fatalf("local sandbox unexpectedly set skip-worktree: %q", flag)
	}
}

func TestProjectAgentGitIndexUpdatesAreIdempotent(t *testing.T) {
	workspace := t.TempDir()
	testRuntimeGit(t, workspace, "init", "-b", "main")
	testRuntimeGit(t, workspace, "config", "user.email", "agentflow@test.local")
	testRuntimeGit(t, workspace, "config", "user.name", "AgentFlow Test")
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testRuntimeGit(t, workspace, "add", "AGENTS.md")
	testRuntimeGit(t, workspace, "commit", "-m", "baseline")

	ctx := context.Background()
	if err := ProtectProjectAgentGitIndex(ctx, workspace, "docker"); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(workspace, ".git", "index.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// A second protect has no paths to update and therefore must not contend
	// with an unrelated live index lock.
	if err := ProtectProjectAgentGitIndex(ctx, workspace, "docker"); err != nil {
		lock.Close()
		t.Fatalf("idempotent protect touched the index: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}

	if err := RestoreProjectAgentGitIndex(ctx, workspace, "docker"); err != nil {
		t.Fatal(err)
	}
	lock, err = os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// Restore is idempotent for the same reason.
	if err := RestoreProjectAgentGitIndex(ctx, workspace, "docker"); err != nil {
		lock.Close()
		t.Fatalf("idempotent restore touched the index: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreProjectAgentGitIndexRemovesStaleIndexLock(t *testing.T) {
	workspace := t.TempDir()
	testRuntimeGit(t, workspace, "init", "-b", "main")
	testRuntimeGit(t, workspace, "config", "user.email", "agentflow@test.local")
	testRuntimeGit(t, workspace, "config", "user.name", "AgentFlow Test")
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testRuntimeGit(t, workspace, "add", "AGENTS.md")
	testRuntimeGit(t, workspace, "commit", "-m", "baseline")
	if err := ProtectProjectAgentGitIndex(context.Background(), workspace, "docker"); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(workspace, ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreProjectAgentGitIndex(context.Background(), workspace, "docker"); err != nil {
		t.Fatalf("restore with stale index lock: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("stale index lock still exists: %v", err)
	}
	if flag := testRuntimeGit(t, workspace, "ls-files", "-v", "--", "AGENTS.md"); strings.HasPrefix(flag, "S ") {
		t.Fatalf("restore left skip-worktree set: %q", flag)
	}
}

func TestRestoreProjectAgentGitIndexKeepsLiveIndexLock(t *testing.T) {
	workspace := t.TempDir()
	testRuntimeGit(t, workspace, "init", "-b", "main")
	testRuntimeGit(t, workspace, "config", "user.email", "agentflow@test.local")
	testRuntimeGit(t, workspace, "config", "user.name", "AgentFlow Test")
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testRuntimeGit(t, workspace, "add", "AGENTS.md")
	testRuntimeGit(t, workspace, "commit", "-m", "baseline")
	if err := ProtectProjectAgentGitIndex(context.Background(), workspace, "docker"); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(workspace, ".git", "index.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	defer os.Remove(lockPath)
	err = RestoreProjectAgentGitIndex(context.Background(), workspace, "docker")
	if err == nil || !strings.Contains(err.Error(), "held by a live process") {
		t.Fatalf("restore error = %v, want live-process protection", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("live index lock was removed: %v", err)
	}
}

func TestAgentFlowGitExcludeSurvivesStashUntracked(t *testing.T) {
	workspace := t.TempDir()
	testRuntimeGit(t, workspace, "init", "-b", "main")
	testRuntimeGit(t, workspace, "config", "user.email", "agentflow@test.local")
	testRuntimeGit(t, workspace, "config", "user.name", "AgentFlow Test")
	if err := EnsureControlRoot(workspace); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(workspace, ".agentflow", "hooks", "codebuddy", "hook.py")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testRuntimeGit(t, workspace, "add", "tracked.txt")
	testRuntimeGit(t, workspace, "commit", "-m", "baseline")
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testRuntimeGit(t, workspace, "stash", "push", "-u", "-m", "agentflow-test")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("hook.py should survive git stash -u when .agentflow/ is excluded: %v", err)
	}
}

func testRuntimeGit(t *testing.T, workspace string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", workspace}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestRunCommandDoesNotWaitForDetachedPipeHolder(t *testing.T) {
	started := time.Now()
	out, err := runCommand(exec.Command("sh", "-c", "(sleep 5) & echo done"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("missing command output: %q", out)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("waited for detached descendant holding output pipe: %s", elapsed)
	}
}

func TestEphemeralSandboxRemovesPreviousAttemptFiles(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	first, err := m.Create(context.Background(), "run-1", "node-1", profile)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(first.Workspace(), "stale.md")
	if err := os.WriteFile(stale, []byte("old attempt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := m.Create(context.Background(), "run-1", "node-1", profile)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale artifact survived clean workspace creation: %v", err)
	}
}

func TestParallelNodeWorkspacesArePhysicallyIsolated(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	left, err := m.Create(context.Background(), "same-run", "left", profile)
	if err != nil {
		t.Fatal(err)
	}
	right, err := m.Create(context.Background(), "same-run", "right", profile)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close(context.Background())
	defer right.Close(context.Background())
	if left.Workspace() == right.Workspace() {
		t.Fatal("parallel nodes received the same workspace")
	}
	if err := os.WriteFile(filepath.Join(left.Workspace(), "result.md"), []byte("left"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(right.Workspace(), "result.md"), []byte("right"), 0o600); err != nil {
		t.Fatal(err)
	}
	leftBody, _ := os.ReadFile(filepath.Join(left.Workspace(), "result.md"))
	rightBody, _ := os.ReadFile(filepath.Join(right.Workspace(), "result.md"))
	if string(leftBody) != "left" || string(rightBody) != "right" {
		t.Fatalf("parallel writes crossed workspaces: left=%q right=%q", leftBody, rightBody)
	}
}

func TestAcquirePersistentKeepsGitCheckout(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	first, err := m.AcquirePersistent(context.Background(), "issue-9", profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", first.Workspace()}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	keep := filepath.Join(first.Workspace(), "keep.txt")
	if err := os.WriteFile(keep, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "keep"}} {
		if output, err := exec.Command("git", append([]string{"-C", first.Workspace()}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := m.AcquirePersistent(context.Background(), "issue-9", profile)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	if first.Workspace() != second.Workspace() {
		t.Fatalf("persistent workspace changed: %s vs %s", first.Workspace(), second.Workspace())
	}
	if _, err := os.Stat(filepath.Join(second.Workspace(), ".git")); err != nil {
		t.Fatalf("persistent checkout lost .git: %v", err)
	}
	body, err := os.ReadFile(keep)
	if err != nil || string(body) != "hello\n" {
		t.Fatalf("persistent work was wiped: %q err=%v", body, err)
	}
}

func TestRunHandlesDoNotHoldExecutionSlotsWhileIdle(t *testing.T) {
	manager := NewManagerWithLimit(t.TempDir(), 1)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	first, err := manager.AcquireRun(context.Background(), "run-first", profile, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second, err := manager.AcquireRun(ctx, "run-second", profile, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err != nil {
		t.Fatalf("idle run consumed execution slot: %v", err)
	}
	defer second.Close(context.Background())
}

func TestDestroyRunRemovesPersistentWorkspace(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	sb, err := m.AcquirePersistent(context.Background(), "run-abc", profile)
	if err != nil {
		t.Fatal(err)
	}
	path := sb.Workspace()
	if err := os.WriteFile(filepath.Join(path, "repo.txt"), []byte("clone"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persistent workspace should survive Close: %v", err)
	}
	if err := m.DestroyRun("run-abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("persistent workspace survived DestroyRun: %v", err)
	}
}

func TestReclaimExceptKeepsLiveRunWorkspaces(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	live, err := m.AcquirePersistent(context.Background(), "run-live", profile)
	if err != nil {
		t.Fatal(err)
	}
	dead, err := m.AcquirePersistent(context.Background(), "run-dead", profile)
	if err != nil {
		t.Fatal(err)
	}
	livePath, deadPath := live.Workspace(), dead.Workspace()
	if err := live.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dead.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	removed, err := m.ReclaimExcept([]string{"run-live"})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("reclaimed %d workspaces, want 1", removed)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live workspace was reclaimed: %v", err)
	}
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Fatalf("finished workspace survived reclaim: %v", err)
	}
}

func TestReclaimExceptSkipsLockedWorkspace(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	live, err := m.AcquirePersistent(context.Background(), "run-live", profile)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close(context.Background())
	stale := filepath.Join(root, "persist-run-stale")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	removed, err := m.ReclaimExcept(nil)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("reclaimed %d workspaces, want 1 (stale only)", removed)
	}
	if _, err := os.Stat(live.Workspace()); err != nil {
		t.Fatalf("in-use workspace was reclaimed: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale workspace survived reclaim: %v", err)
	}
}

func TestDestroyRunSkipsLockedWorkspace(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	sb, err := m.AcquirePersistent(context.Background(), "run-abc", profile)
	if err != nil {
		t.Fatal(err)
	}
	path := sb.Workspace()
	if err := os.WriteFile(filepath.Join(path, "repo.txt"), []byte("clone"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRun("run-abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("in-use workspace was destroyed: %v", err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRun("run-abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("workspace survived DestroyRun after Close: %v", err)
	}
}

func TestExecSetsPWDAndRejectsMissingWorkspace(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	sb, err := m.AcquirePersistent(context.Background(), "run-cwd", profile)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close(context.Background())
	out, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{"sh", "-lc", `printf '%s' "$PWD"`}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(sb.Workspace())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != abs {
		t.Fatalf("PWD = %q, want workspace %q", strings.TrimSpace(out), abs)
	}
	if err := os.RemoveAll(sb.Workspace()); err != nil {
		t.Fatal(err)
	}
	_, err = sb.Exec(context.Background(), CommandSpec{Argv: []string{"sh", "-lc", "pwd"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "sandbox workspace missing") {
		t.Fatalf("missing workspace error = %v", err)
	}
}

func TestRunSandboxBindsCodeBuddyCredentialOnlyToTargetProcess(t *testing.T) {
	t.Setenv("CODEBUDDY_API_KEY", "host-key-must-not-leak")
	t.Setenv("CODEBUDDY_INTERNET_ENVIRONMENT", "host-network-must-not-leak")
	root := t.TempDir()
	manager := NewManager(root)
	sandbox, err := manager.AcquireRun(context.Background(), "run-credential", domain.SandboxProfile{ID: "local", Driver: "local"}, ProviderRequirements("codebuddy"), RunCredentials{
		CodeBuddy: CodeBuddyCredential{APIKey: "project-key", InternetEnvironment: "project-network"},
	}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(context.Background())

	codeBuddy := CommandSpec{
		Argv:               []string{"sh", "-lc", `printf '%s|%s|%s' "$CODEBUDDY_API_KEY" "$CODEBUDDY_INTERNET_ENVIRONMENT" "$HOME"; touch "$HOME/used"`},
		Provider:           "codebuddy",
		CredentialBindings: []CredentialBinding{BindCodeBuddy},
		PrivateHome:        true,
	}
	output, err := sandbox.Exec(context.Background(), codeBuddy, nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimSpace(output), "|")
	if len(parts) != 3 || parts[0] != "project-key" || parts[1] != "project-network" {
		t.Fatalf("bound credential output = %q", output)
	}
	if !strings.HasPrefix(parts[2], filepath.Join(sandbox.Workspace(), ".agentflow", "private-home")) {
		t.Fatalf("CodeBuddy HOME is not run-private: %q", parts[2])
	}
	plain, err := sandbox.Exec(context.Background(), CommandSpec{Argv: []string{"sh", "-lc", `printf '%s|%s' "$CODEBUDDY_API_KEY" "$CODEBUDDY_INTERNET_ENVIRONMENT"`}, Provider: "command"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(plain) != "|" {
		t.Fatalf("credential leaked to unrelated child: %q", plain)
	}
	err = filepath.Walk(sandbox.Workspace(), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), "project-key") || strings.Contains(string(raw), "project-network") {
			t.Fatalf("credential persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunSandboxBindsGitAskPassForVerification(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	sandbox, err := manager.AcquireRun(context.Background(), "run-git-askpass", domain.SandboxProfile{ID: "local", Driver: "local"}, SandboxRequirements{}, RunCredentials{
		GitToken: "proj_git_token",
	}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(context.Background())

	output, err := sandbox.Exec(context.Background(), CommandSpec{
		Argv:               []string{"sh", "-lc", `printf '%s\n' "$GIT_ASKPASS"; test -x "$GIT_ASKPASS"; "$GIT_ASKPASS" "Username for 'https://git.example.com':"; "$GIT_ASKPASS" "Password for 'https://git.example.com':"`},
		Provider:           "verification",
		CredentialBindings: []CredentialBinding{BindGit},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 3 {
		t.Fatalf("askpass output = %q", output)
	}
	if !strings.HasSuffix(lines[0], filepath.Join(".agentflow", "runtime", "git-askpass")) {
		t.Fatalf("GIT_ASKPASS = %q", lines[0])
	}
	if lines[1] != "private" || lines[2] != "proj_git_token" {
		t.Fatalf("askpass answers = %q %q", lines[1], lines[2])
	}
	plain, err := sandbox.Exec(context.Background(), CommandSpec{Argv: []string{"sh", "-lc", `printf '%s|%s' "$GIT_ASKPASS" "$AGENTFLOW_GIT_TOKEN"`}, Provider: "verification"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(plain) != "|" {
		t.Fatalf("git credential leaked to unbound child: %q", plain)
	}
}

func TestVerificationGitLSRemoteAskPassAgainstHTTP401(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	var authorized atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
		if err != nil || string(raw) != "private:proj_git_token" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		authorized.Store(true)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n0000"))
	}))
	defer srv.Close()

	root := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = test\n\temail = test@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root)
	sandbox, err := manager.AcquireRun(context.Background(), "run-git-401", domain.SandboxProfile{ID: "local", Driver: "local"}, SandboxRequirements{}, RunCredentials{
		GitToken: "proj_git_token",
	}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(context.Background())
	workspace := sandbox.Workspace()
	for _, args := range [][]string{
		{"git", "init", "-q"},
		{"git", "remote", "add", "origin", srv.URL + "/example/repo.git"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = workspace
		cmd.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	without, withoutErr := sandbox.Exec(context.Background(), CommandSpec{
		Argv:     []string{"sh", "-lc", "git -c credential.helper= ls-remote origin"},
		Provider: "verification",
		environment: map[string]string{
			"HOME":                home,
			"GIT_CONFIG_NOSYSTEM": "1",
			"GIT_TERMINAL_PROMPT": "0",
		},
	}, nil)
	if withoutErr == nil || !strings.Contains(without+" "+withoutErr.Error(), "could not read Username") {
		t.Fatalf("unauthenticated ls-remote should match CVM error, err=%v out=%q", withoutErr, without)
	}

	with, withErr := sandbox.Exec(context.Background(), CommandSpec{
		Argv:               []string{"sh", "-lc", "git -c credential.helper= ls-remote origin"},
		Provider:           "verification",
		CredentialBindings: []CredentialBinding{BindGit},
		environment: map[string]string{
			"HOME":                home,
			"GIT_CONFIG_NOSYSTEM": "1",
		},
	}, nil)
	if !authorized.Load() {
		t.Fatalf("GIT_ASKPASS did not send Basic auth; err=%v out=%q", withErr, with)
	}
}

func TestHostInsteadOfUsernamePromptThenAskPass(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	var authorized atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "private" || p != "proj_git_token" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		authorized.Store(true)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n0000"))
	}))
	defer srv.Close()

	home := t.TempDir()
	cfg := fmt.Sprintf("[url %q]\n\tinsteadOf = git@git.example.com:\n[user]\n\tname = test\n\temail = test@example.com\n", srv.URL+"/")
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", "git@git.example.com:example/repo.git")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote: %v\n%s", err, out)
	}

	fail := exec.Command("git", "-c", "credential.helper=", "ls-remote", "origin")
	fail.Dir = dir
	fail.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	failOut, failErr := fail.CombinedOutput()
	blob := string(failOut) + " " + fmt.Sprint(failErr)
	if failErr == nil || !strings.Contains(blob, "could not read Username") {
		t.Fatalf("host insteadOf without askpass should prompt, err=%v out=%s", failErr, failOut)
	}

	askpass := filepath.Join(dir, "git-askpass")
	if err := os.WriteFile(askpass, []byte(gitAskPassScript), 0o700); err != nil {
		t.Fatal(err)
	}
	okCmd := exec.Command("git", "-c", "credential.helper=", "ls-remote", "origin")
	okCmd.Dir = dir
	okCmd.Env = append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS="+askpass,
		"AGENTFLOW_GIT_USER=private",
		"AGENTFLOW_GIT_TOKEN=proj_git_token",
	)
	okOut, okErr := okCmd.CombinedOutput()
	if !authorized.Load() {
		t.Fatalf("askpass did not authenticate after host insteadOf; err=%v out=%s", okErr, okOut)
	}
}

func TestEphemeralCreateDeletesWorkspaceOnClose(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	sb, err := m.Create(context.Background(), "run-1", "node-1", profile)
	if err != nil {
		t.Fatal(err)
	}
	path := sb.Workspace()
	if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ephemeral workspace survived Close: %v", err)
	}
}

func TestCompactCommandKeepsShortCommandsAndBoundsPrompts(t *testing.T) {
	short := []string{"sh", "-lc", "test -f result.json"}
	if got := compactCommand(short); got != "sh -lc test -f result.json" {
		t.Fatalf("short command changed: %q", got)
	}
	longPrompt := strings.Repeat("需求", 2_000)
	got := compactCommand([]string{"codebuddy", "-p", longPrompt})
	if len([]rune(got)) > 1_100 || !strings.Contains(got, "arguments truncated") {
		t.Fatalf("long command was not bounded: %d runes", len([]rune(got)))
	}
	secret := compactCommand([]string{"docker", "exec", "-e", "CODEBUDDY_API_KEY=ck_secret", "-e", "GIT_CONFIG_KEY_0=url.https://private:token@git.example.com/.insteadof", "sandbox", "codebuddy"})
	if strings.Contains(secret, "ck_secret") || strings.Contains(secret, "private:token") {
		t.Fatalf("credential leaked into compact command: %s", secret)
	}
}

func TestAdaptersEmitVersionedPortableObservations(t *testing.T) {
	var events []domain.TelemetryEvent
	adapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codex", Protocol: "jsonl", Model: "gpt-test"}, func(event domain.TelemetryEvent) { events = append(events, event) })
	adapter.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: `{"type":"turn.completed","thread_id":"thread-1","usage":{"input_tokens":12,"cached_input_tokens":3,"output_tokens":5,"reasoning_tokens":2}}`})
	if len(events) != 2 {
		t.Fatalf("codex events=%d, want usage and model finish", len(events))
	}
	usage := events[0]
	if usage.SpecVersion != domain.ObservabilitySpecVersion || usage.Source.Adapter != "codex-jsonl-v1" || usage.SessionID != "thread-1" {
		t.Fatalf("portable envelope missing: %+v", usage)
	}
	if usage.Usage == nil || usage.Usage.InputTokens != 12 || usage.Usage.CacheReadTokens != 3 || usage.Usage.OutputTokens != 5 || usage.Usage.ReasoningTokens != 2 || len(usage.Metrics) < 4 {
		t.Fatalf("usage was not normalized: %+v", usage)
	}
	spec := ObservabilitySpec()
	if spec.Version != domain.ObservabilitySpecVersion || len(spec.Adapters) < 3 {
		t.Fatalf("adapter capabilities not published: %+v", spec)
	}
}

func TestMaterializeProviderAssets(t *testing.T) {
	assets := domain.AgentAssetBundle{
		Rules:      []domain.RuleDefinition{{ID: "safe", Name: "Safety", Content: "Never leak secrets.", Priority: 100}},
		Skills:     []domain.SkillDefinition{{ID: "review", Name: "Review", Description: "Review code", Content: "# Review\nCheck behavior.", Files: map[string]string{"references/checks.md": "evidence"}}},
		MCPServers: []domain.MCPServerDefinition{{ID: "tools", Name: "Tools", Transport: "stdio", Command: "tool-server", Args: []string{"serve"}, TimeoutSec: 30}},
	}
	agent := domain.AgentDefinition{ID: "reviewer", Name: "Reviewer", Instructions: "Review the change."}
	for _, kind := range []string{"claude-code", "codex", "codebuddy"} {
		dir := filepath.Join(t.TempDir(), kind)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg, err := materialize(dir, "invoke-1", domain.ExecutorDefinition{Kind: kind}, agent, assets)
		if err != nil {
			t.Fatal(err)
		}
		agentRoot := filepath.Join(dir, ".agentflow", "reviewer")
		instruction := filepath.Join(agentRoot, "agent-system.md")
		skill := filepath.Join(agentRoot, "skills", "review", "SKILL.md")
		switch kind {
		case "claude-code":
			if !containsArgPair(cfg.Args, "--system-prompt-file", ".agentflow/reviewer/agent-system.md") || !containsArgPair(cfg.Args, "--setting-sources", "user") {
				t.Fatalf("Claude project settings were not isolated: %v", cfg.Args)
			}
			if containsArg(cfg.Args, "--strict-mcp-config") {
				t.Fatalf("Claude user MCP configuration must remain enabled: %v", cfg.Args)
			}
			if !containsArgPair(cfg.Args, "--plugin-dir", ".agentflow/reviewer") {
				t.Fatalf("Claude selected skills were not injected: %v", cfg.Args)
			}
		case "codebuddy":
			if !containsArgPair(cfg.Args, "--system-prompt-file", ".agentflow/reviewer/agent-system.md") {
				t.Fatalf("CodeBuddy system prompt was not injected: %v", cfg.Args)
			}
			if containsArg(cfg.Args, "--strict-mcp-config") {
				t.Fatalf("CodeBuddy user MCP configuration must remain enabled: %v", cfg.Args)
			}
			if containsArg(cfg.Args, "-y") || containsArg(cfg.Args, "--setting-sources") || containsArg(cfg.Args, "--permission-mode") {
				t.Fatalf("process-only CodeBuddy flags must be added once by Prepare: %v", cfg.Args)
			}
			if !containsArgPair(cfg.Args, "--settings", ".agentflow/reviewer/codebuddy-settings.json") {
				t.Fatalf("CodeBuddy hook settings were not injected: %v", cfg.Args)
			}
			if containsArg(cfg.Args, "--plugin-dir") {
				t.Fatalf("CodeBuddy plugin flag must be added once by Prepare: %v", cfg.Args)
			}
			if _, err := os.Stat(filepath.Join(dir, ".agentflow", "hooks", "codebuddy", "hook.py")); err != nil {
				t.Fatal(err)
			}
			if cfg.HookOutput != ".agentflow/runtime/invoke-1/observability/hooks.ndjson" {
				t.Fatalf("CodeBuddy hook output is not invocation-scoped: %v", cfg.HookOutput)
			}
			for _, name := range []string{"hooks", "codebuddy-settings.json"} {
				if info, err := os.Lstat(filepath.Join(agentRoot, name)); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("CodeBuddy Agent binding %s is not a symlink: %v, %v", name, info, err)
				}
			}
		case "codex":
			if containsArg(cfg.Args, "--ignore-user-config") {
				t.Fatalf("Codex user config must remain enabled for custom model providers: %v", cfg.Args)
			}
			if containsArg(cfg.Args, "--ignore-rules") {
				t.Fatalf("Codex user rules must remain enabled: %v", cfg.Args)
			}
			if !containsArgPair(cfg.Args, "-c", `skills.config=[{ path = ".agentflow/reviewer/skills/review", enabled = true }]`) {
				t.Fatalf("Codex selected skills were not injected: %v", cfg.Args)
			}
			if !containsArgPair(cfg.Args, "-c", `shell_environment_policy.inherit="core"`) {
				t.Fatalf("Codex shell environment policy was not injected: %v", cfg.Args)
			}
		}
		for _, path := range []string{instruction, skill, filepath.Join(agentRoot, "mcp.json")} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
		for _, name := range []string{"skills", "rules", "mcp", "agent-system.md", "mcp.json"} {
			if info, err := os.Lstat(filepath.Join(agentRoot, name)); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("agent binding %s is not a symlink: %v, %v", name, info, err)
			}
		}
		if info, err := os.Lstat(filepath.Join(dir, ".agentflow", "bindings", "reviewer", "skills", "review")); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("selected skill binding is not a symlink: %v, %v", info, err)
		}
		raw, _ := os.ReadFile(instruction)
		if !strings.Contains(string(raw), "Never leak secrets") {
			t.Fatalf("rule was not compiled for %s", kind)
		}
		for _, repoFile := range []string{"AGENTS.md", "CLAUDE.md", "CODEBUDDY.md"} {
			if _, err := os.Lstat(filepath.Join(dir, repoFile)); !os.IsNotExist(err) {
				t.Fatalf("materialize should not create repository file %s", repoFile)
			}
		}
	}
}

func TestMaterializeKeepsAssetsAgentScopedAndRuntimeInvocationScoped(t *testing.T) {
	dir := t.TempDir()
	agent := domain.AgentDefinition{ID: "reviewer", Name: "Reviewer"}
	assets := domain.AgentAssetBundle{Skills: []domain.SkillDefinition{{ID: "review", Description: "Review", Content: "# Review"}}}
	for _, invocationID := range []string{"invoke-1", "invoke-2"} {
		if _, err := materialize(dir, invocationID, domain.ExecutorDefinition{Kind: "codex"}, agent, assets); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".agentflow", "runtime", invocationID, "run-context.json")); err != nil {
			t.Fatalf("missing invocation state for %s: %v", invocationID, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".agentflow", "runtime", invocationID, "skills")); !os.IsNotExist(err) {
			t.Fatalf("invocation %s unexpectedly owns skills: %v", invocationID, err)
		}
	}
	link := filepath.Join(dir, ".agentflow", "reviewer", "skills")
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Agent skill view is not a symlink: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(link, "review", "SKILL.md")); err != nil {
		t.Fatalf("Agent skill link does not resolve: %v", err)
	}
}

func TestMaterializeRejectsRepositoryAgentflowSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, ".agentflow")); err != nil {
		t.Fatal(err)
	}
	_, err := materialize(dir, "invoke-1", domain.ExecutorDefinition{Kind: "codex"}, domain.AgentDefinition{ID: "probe", Name: "Probe"}, domain.AgentAssetBundle{})
	if err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("repository-controlled .agentflow symlink should be rejected, got %v", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("materializer wrote through repository symlink: entries=%v err=%v", entries, readErr)
	}
}

func TestMaterializedCodexArgumentsAreAcceptedByInstalledCLI(t *testing.T) {
	if os.Getenv("AGENTFLOW_CLI_INTEGRATION") != "1" {
		t.Skip("set AGENTFLOW_CLI_INTEGRATION=1 to validate against installed CLIs")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex CLI is not installed")
	}
	dir := t.TempDir()
	cfg, err := materialize(dir, "config-probe", domain.ExecutorDefinition{Kind: "codex"}, domain.AgentDefinition{ID: "probe", Name: "Probe", Instructions: "Stay scoped."}, domain.AgentAssetBundle{
		Skills: []domain.SkillDefinition{{ID: "probe", Description: "Config parsing probe", Content: "# Probe\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"exec"}, cfg.Args...)
	args = append(args, "--help")
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	output, runErr := cmd.CombinedOutput()
	text := string(output)
	if runErr != nil || !strings.Contains(text, "Run Codex non-interactively") {
		t.Fatalf("Codex did not accept generated arguments: %v\n%s", runErr, output)
	}
}

func TestMaterializeCodeBuddyDoesNotOverwriteRepositoryInstructions(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, "AGENTS.md")
	codebuddy := filepath.Join(dir, "CODEBUDDY.md")
	if err := os.WriteFile(agents, []byte("repository policy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", codebuddy); err != nil {
		t.Fatal(err)
	}
	_, err := materialize(dir, "invoke-1", domain.ExecutorDefinition{Kind: "codebuddy"}, domain.AgentDefinition{ID: "fixer", Name: "Fixer", Instructions: "agent policy"}, domain.AgentAssetBundle{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(agents)
	if err != nil || string(raw) != "repository policy\n" {
		t.Fatalf("repository instructions changed: %q, %v", raw, err)
	}
	info, err := os.Lstat(codebuddy)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("CODEBUDDY.md symlink changed: %v, %v", info, err)
	}
}

func containsArg(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestMaterializeIgnoresWorkspaceLocalMCP(t *testing.T) {
	dir := t.TempDir()
	server := filepath.Join(dir, ".codebuddy", "runtime", "replace_batch_mcp")
	if err := os.MkdirAll(server, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(server, "server.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mcp := `{
  "mcpServers": {
    "gongfeng": {"url": "https://example.invalid/gongfeng"},
    "playwright": {"command": "npx", "args": ["-y", "@playwright/mcp@latest"]},
    "replace-batch": {"command": "python3", "args": [".codebuddy/runtime/replace_batch_mcp/server.py"]}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(mcp), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := materialize(dir, "invoke-1", domain.ExecutorDefinition{Kind: "codebuddy"}, domain.AgentDefinition{ID: "fixer", Name: "Fixer"}, domain.AgentAssetBundle{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".agentflow", "fixer", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "replace-batch") || strings.Contains(text, "gongfeng") || strings.Contains(text, "playwright") {
		t.Fatalf("repository MCP leaked into runtime bundle: %s", text)
	}
	if containsArg(cfg.Args, "--strict-mcp-config") || containsArg(cfg.Args, "--setting-sources") {
		t.Fatalf("process isolation flags must be added by Provider.Prepare: %v", cfg.Args)
	}
}

func TestMaterializeInjectsHTTPMCPAliases(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTFLOW_MCP_USERNAME", "example-admin")
	assets := domain.AgentAssetBundle{
		MCPServers: []domain.MCPServerDefinition{
			{ID: "zhiyan-pipeline", Name: "智研", Transport: "http", URL: "https://mcpgw.knot.example.com/zhiyan/qci", Headers: map[string]string{"Authorization": "Bearer test-token"}},
			{ID: "gongfeng-delivery", Name: "工蜂", Transport: "http", URL: "https://mcpgw.knot.example.com/gongfeng", Headers: map[string]string{"Authorization": "Bearer test-token"}},
		},
	}
	_, err := materialize(dir, "invoke-1", domain.ExecutorDefinition{Kind: "codebuddy"}, domain.AgentDefinition{ID: "checker", Name: "Checker"}, assets)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".agentflow", "checker", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, needle := range []string{`"zhiyan-pipeline"`, `"coding-mcp"`, `"gongfeng-delivery"`, `"gongfeng"`, `"X-Username": "example-admin"`, "mcpgw.knot.example.com/zhiyan/qci"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("mcp.json missing %s:\n%s", needle, text)
		}
	}
	sys, err := os.ReadFile(filepath.Join(dir, ".agentflow", "checker", "agent-system.md"))
	if err != nil {
		t.Fatal(err)
	}
	hint := string(sys)
	if !strings.Contains(hint, "totalresult") || !strings.Contains(hint, "oldtotalresult") || !strings.Contains(hint, "QueryErrorLog") || !strings.Contains(hint, "taskId") || !strings.Contains(hint, "get_commit_combined_status") || !strings.Contains(hint, "coding_skill") {
		t.Fatalf("system prompt missing QCI/MR API rules:\n%s", hint)
	}
}

func TestIsolateCodeBuddyFlagsSkipsProjectSettings(t *testing.T) {
	got := isolateCodeBuddyFlags([]string{"codebuddy", "-p", "--setting-sources", "user,project,local", "--permission-mode", "dontAsk", "--output-format", "stream-json"})
	if !containsArg(got, "-y") || !containsArgPair(got, "--setting-sources", "user") {
		t.Fatalf("missing isolation flags: %v", got)
	}
	if !containsArgPair(got, "--permission-mode", "bypassPermissions") {
		t.Fatalf("missing bypassPermissions: %v", got)
	}
	if containsArg(got, "user,project,local") || containsArg(got, "dontAsk") {
		t.Fatalf("old setting sources or permission mode leaked: %v", got)
	}
	again := isolateCodeBuddyFlags(got)
	ys, sources, modes := 0, 0, 0
	for i, a := range again {
		if a == "-y" {
			ys++
		}
		if a == "--setting-sources" && i+1 < len(again) && again[i+1] == "user" {
			sources++
		}
		if a == "--permission-mode" && i+1 < len(again) && again[i+1] == "bypassPermissions" {
			modes++
		}
	}
	if ys != 1 || sources != 1 || modes != 1 {
		t.Fatalf("isolation flags should be idempotent: %v", again)
	}
}

func TestIsolateClaudeFlagsUsesHostUserSettings(t *testing.T) {
	got := isolateClaudeFlags([]string{"claude", "-p", "--setting-sources", "user,project,local", "--system-prompt-file", "/repo/untrusted.md"}, ".agentflow/tester/agent-system.md")
	if !containsArgPair(got, "--setting-sources", "user") {
		t.Fatalf("Claude should use the host user source: %v", got)
	}
	if !containsArgPair(got, "--system-prompt-file", ".agentflow/tester/agent-system.md") {
		t.Fatalf("Claude should use the managed system prompt: %v", got)
	}
	if containsArg(got, "user,project,local") || containsArg(got, "/repo/untrusted.md") || containsArg(got, "none") {
		t.Fatalf("Claude retained unsupported or repository settings: %v", got)
	}
	again := isolateClaudeFlags(got, ".agentflow/tester/agent-system.md")
	sources, prompts := 0, 0
	for i, arg := range again {
		if arg == "--setting-sources" && i+1 < len(again) && again[i+1] == "user" {
			sources++
		}
		if arg == "--system-prompt-file" && i+1 < len(again) && again[i+1] == ".agentflow/tester/agent-system.md" {
			prompts++
		}
	}
	if sources != 1 || prompts != 1 {
		t.Fatalf("Claude isolation flags should be idempotent: %v", again)
	}
}

func TestExecutorRunnerAppliesDriverSpecificUserConfigPolicy(t *testing.T) {
	tests := []struct {
		name   string
		driver string
		kind   string
		check  func(*testing.T, []string)
	}{
		{name: "local claude", driver: "local", kind: "claude-code", check: func(t *testing.T, argv []string) {
			if !containsArgPair(argv, "--setting-sources", "user") || !containsArg(argv, "--strict-mcp-config") {
				t.Fatalf("local Claude compatibility flags missing: %v", argv)
			}
		}},
		{name: "sandboxed claude", driver: "bwrap", kind: "claude-code", check: func(t *testing.T, argv []string) {
			if !containsArgPair(argv, "--setting-sources", "user") || containsArg(argv, "--strict-mcp-config") {
				t.Fatalf("sandboxed Claude should inherit user settings and MCP: %v", argv)
			}
		}},
		{name: "local codebuddy", driver: "local", kind: "codebuddy", check: func(t *testing.T, argv []string) {
			if !containsArgPair(argv, "--setting-sources", "none") || !containsArg(argv, "--strict-mcp-config") {
				t.Fatalf("local CodeBuddy compatibility flags missing: %v", argv)
			}
		}},
		{name: "sandboxed codebuddy", driver: "bwrap", kind: "codebuddy", check: func(t *testing.T, argv []string) {
			if !containsArgPair(argv, "--setting-sources", "none") || !containsArg(argv, "--strict-mcp-config") {
				t.Fatalf("sandboxed CodeBuddy must use AgentFlow settings only: %v", argv)
			}
		}},
		{name: "local codex", driver: "local", kind: "codex", check: func(t *testing.T, argv []string) {
			if !containsArg(argv, "--ignore-user-config") || !containsArg(argv, "--ignore-rules") {
				t.Fatalf("local Codex compatibility flags missing: %v", argv)
			}
		}},
		{name: "sandboxed codex", driver: "bwrap", kind: "codex", check: func(t *testing.T, argv []string) {
			if containsArg(argv, "--ignore-user-config") || containsArg(argv, "--ignore-rules") {
				t.Fatalf("sandboxed Codex should inherit user configuration: %v", argv)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sb := &capturingSandbox{workspace: t.TempDir(), driver: test.driver}
			ex := domain.ExecutorDefinition{Kind: test.kind, Binary: test.kind}
			_, err := NewExecutorRunner().Run(context.Background(), sb, "invoke-1", ex, domain.AgentDefinition{ID: "probe", Name: "Probe"}, domain.AgentAssetBundle{}, "probe", InvocationOptions{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, sb.argv)
		})
	}
}

func TestCodeBuddyDropsToolsAllowlistWhenHTTPMCPPresent(t *testing.T) {
	sb := &capturingSandbox{workspace: t.TempDir(), driver: "local"}
	ex := domain.ExecutorDefinition{
		Kind:   "codebuddy",
		Binary: "codebuddy",
		Args:   []string{"-p", "--tools", "Read,Write,Edit,Bash,Glob,Grep", "--max-turns", "180"},
	}
	assets := domain.AgentAssetBundle{
		MCPServers: []domain.MCPServerDefinition{
			{ID: "gongfeng-delivery", Name: "工蜂", Transport: "http", URL: "https://mcpgw.knot.example.com/gongfeng", Headers: map[string]string{"Authorization": "Bearer test-token"}},
		},
	}
	if _, err := NewExecutorRunner().Run(context.Background(), sb, "invoke-mcp", ex, domain.AgentDefinition{ID: "closer", Name: "Closer"}, assets, "probe", InvocationOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if containsArg(sb.argv, "--tools") || containsArg(sb.argv, "Read,Write,Edit,Bash,Glob,Grep") {
		t.Fatalf("--tools allowlist must not hide MCP servers: %v", sb.argv)
	}
	if !containsArgPair(sb.argv, "--mcp-config", ".agentflow/config/closer/mcp.json") {
		t.Fatalf("mcp-config must point at the real config file, got %v", sb.argv)
	}
	if sb.argv[len(sb.argv)-1] != "probe" {
		t.Fatalf("short prompt must stay on argv, got %v", sb.argv)
	}
}

func TestClipExecutorOutputBoundsRawCLIStreams(t *testing.T) {
	short := clipExecutorOutput("ok")
	if short != "ok" {
		t.Fatalf("short output changed: %q", short)
	}
	raw := strings.Repeat("x", maxExecutorOutputBytes+4096)
	got := clipExecutorOutput(raw)
	if len(got) <= maxExecutorOutputBytes || !strings.Contains(got, "output truncated") {
		t.Fatalf("expected clipped output, got %d bytes", len(got))
	}
}

func TestOversizedPromptMovesOffArgvIntoSystemFile(t *testing.T) {
	sb := &capturingSandbox{workspace: t.TempDir(), driver: "local"}
	ex := domain.ExecutorDefinition{Kind: "codebuddy", Binary: "codebuddy", Args: []string{"-p"}}
	prompt := strings.Repeat("产物正文", maxArgvPromptBytes/12+100)
	if _, err := NewExecutorRunner().Run(context.Background(), sb, "invoke-big", ex, domain.AgentDefinition{ID: "closer", Name: "Closer"}, domain.AgentAssetBundle{}, prompt, InvocationOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	last := sb.argv[len(sb.argv)-1]
	if strings.Contains(last, "产物正文") {
		t.Fatalf("oversized prompt leaked onto argv (%d bytes)", len(last))
	}
	sys, err := os.ReadFile(filepath.Join(sb.workspace, ".agentflow", "closer", "agent-system.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sys), "本次节点任务书") || !strings.Contains(string(sys), "产物正文") {
		t.Fatalf("system prompt missing spilled task book")
	}
}

func TestProviderIsolationKeepsOnlyInvocationPluginDirectory(t *testing.T) {
	for _, kind := range []string{"claude-code", "codebuddy"} {
		argv := []string{kind, "--plugin-dir", "/repo/.claude/plugins/untrusted"}
		runtimeRoot := ".agentflow/invoke-1"
		if kind == "claude-code" {
			argv = isolateClaudeFlags(argv, runtimeRoot+"/agent-system.md")
		} else {
			argv = isolateCodeBuddyFlags(argv)
		}
		argv = append(argv, "--plugin-dir", runtimeRoot)
		plugins := 0
		for i, arg := range argv {
			if arg == "--plugin-dir" && i+1 < len(argv) {
				plugins++
				if argv[i+1] != runtimeRoot {
					t.Fatalf("%s retained non-invocation plugin directory: %v", kind, argv)
				}
			}
		}
		if plugins != 1 {
			t.Fatalf("%s plugin directory count = %d, args: %v", kind, plugins, argv)
		}
	}
}

func TestDockerWorkspaceExecAvoidsRuncWorkdirFlag(t *testing.T) {
	got := dockerWorkspaceExecArgv([]string{"codebuddy", "--print"})
	text := strings.Join(got, " ")
	if strings.Contains(text, "-w") {
		t.Fatalf("docker exec wrapper still sets -w: %v", got)
	}
	if got[0] != "sh" || !strings.Contains(got[2], "cd /workspace") {
		t.Fatalf("wrapper = %v", got)
	}
	if !strings.Contains(got[2], "/workspace/.agentflow/runtime/bin") {
		t.Fatalf("docker launch should prepend workspace agentflow bin after login, got %v", got)
	}
	if got[len(got)-2] != "codebuddy" || got[len(got)-1] != "--print" {
		t.Fatalf("argv not preserved: %v", got)
	}
}

func TestDockerWorkspaceExecDropsInnerLoginShell(t *testing.T) {
	got := dockerWorkspaceExecArgv([]string{"sh", "-lc", "agentflow mcp list"})
	if len(got) < 6 {
		t.Fatalf("argv = %v", got)
	}
	if got[3] != "sh" || got[4] != "-c" || got[5] != "agentflow mcp list" {
		t.Fatalf("inner login shell should become sh -c, got %v", got)
	}
}

func TestSandboxMasksRejectSymlinkedAgentDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, ".claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := bwrapAgentConfigMasks(dir); err == nil {
		t.Fatal("bwrap masks accepted a symlinked Agent CLI directory")
	}
	if _, err := dockerAgentConfigMasks(dir); err == nil {
		t.Fatal("Docker masks accepted a symlinked Agent CLI directory")
	}
}

func TestAdapterRequiresTerminalResult(t *testing.T) {
	adapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, nil)
	if adapter.Finished() || adapter.Failed() {
		t.Fatal("new adapter should be unfinished")
	}
	adapter.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: `{"type":"result","is_error":true,"result":"boom"}`})
	if !adapter.Finished() || !adapter.Failed() || adapter.Result() != "boom" {
		t.Fatalf("error result not captured: finished=%v failed=%v result=%q", adapter.Finished(), adapter.Failed(), adapter.Result())
	}
}

func containsArgPair(values []string, key, wanted string) bool {
	for i := 0; i+1 < len(values); i++ {
		if values[i] == key && values[i+1] == wanted {
			return true
		}
	}
	return false
}

func TestCodeBuddyTelemetryAdapter(t *testing.T) {
	events := []domain.TelemetryEvent{}
	adapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json", Model: "deepseek-v4-pro-ioa"}, func(event domain.TelemetryEvent) {
		events = append(events, event)
	})
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"session-1","model":"deepseek-v4-pro-ioa","permissionMode":"dontAsk"}`,
		`{"type":"stream_event","session_id":"session-1","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool-1","name":"Write","input":{"file_path":"result.txt"}}}}`,
		`{"type":"stream_event","session_id":"session-1","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"checking"}}}`,
		`{"type":"stream_event","session_id":"session-1","event":{"type":"message_stop"}}`,
		`{"type":"result","subtype":"success","session_id":"session-1","result":"done","duration_ms":100,"num_turns":1,"usage":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":10,"cache_creation_input_tokens":20}}`,
	}
	for _, line := range lines {
		adapter.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: line})
	}
	if adapter.Result() != "done" {
		t.Fatalf("unexpected result %q", adapter.Result())
	}
	var initialized, tool, reasoning, usage bool
	for _, event := range events {
		switch event.Name {
		case "agent.initialized":
			initialized = event.Model == "deepseek-v4-pro-ioa" && event.SessionID == "session-1"
		case "tool.call":
			tool = event.ToolCallID == "tool-1" && strings.Contains(event.Message, "Write") && strings.Contains(event.Message, "result.txt")
		case "agent.reasoning":
			reasoning = event.Message == "checking"
		case "usage.final":
			usage = event.Usage != nil && event.Usage.TotalTokens == 150
		}
	}
	if !initialized || !tool || !reasoning || !usage {
		t.Fatalf("missing normalized events: %+v", events)
	}
}

func TestMaterializeNeverWritesProcessEnvironmentToRunContext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEBUDDY_API_KEY", "ck_secret")
	_, err := materialize(dir, "context-probe", domain.ExecutorDefinition{Kind: "codebuddy"}, domain.AgentDefinition{ID: "fixer", Name: "Fixer"}, domain.AgentAssetBundle{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".agentflow", "runtime", "context-probe", "run-context.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ck_secret") {
		t.Fatalf("api key leaked into run-context.json: %s", raw)
	}
	if strings.Contains(string(raw), `"env"`) {
		t.Fatalf("executor environment channel survived in run-context.json: %s", raw)
	}
}

func TestSummarizeToolCallIncludesCommandAndPath(t *testing.T) {
	if got := summarizeToolCall("Bash", map[string]any{"command": "git push origin HEAD"}); !strings.Contains(got, "git push") {
		t.Fatalf("bash summary = %q", got)
	}
	if got := summarizeToolCall("Write", map[string]any{"path": "fix-report.md"}); got != "Write fix-report.md" {
		t.Fatalf("write summary = %q", got)
	}
}

func TestToolCallWaitsForInputJSONDelta(t *testing.T) {
	events := []domain.TelemetryEvent{}
	adapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json", Model: "deepseek-v4-pro-ioa"}, func(event domain.TelemetryEvent) {
		events = append(events, event)
	})
	lines := []string{
		`{"type":"stream_event","session_id":"s1","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"Bash","input":{}}}}`,
		`{"type":"stream_event","session_id":"s1","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"git status\"}"}}}`,
		`{"type":"stream_event","session_id":"s1","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"user","session_id":"s1","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"On branch main\nnothing to commit"}]}]}}`,
	}
	for _, line := range lines {
		adapter.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: line})
	}
	var start, finish string
	starts := 0
	for _, event := range events {
		if event.Name != "tool.call" {
			continue
		}
		if event.Phase == "start" {
			starts++
			start = event.Message
		}
		if event.Phase == "finish" {
			finish = event.Message
		}
	}
	if starts != 1 {
		t.Fatalf("expected one tool start, got %d in %+v", starts, events)
	}
	if start == "Bash" || !strings.Contains(start, "git status") {
		t.Fatalf("tool start should include command, got %q", start)
	}
	if strings.Contains(finish, "tool result") || strings.Contains(finish, "nothing to commit") || strings.Contains(finish, "\n") {
		t.Fatalf("tool finish should be a one-line summary, got %q", finish)
	}
	if finish == "" {
		t.Fatalf("telemetry still needs a tool finish event, got empty message")
	}
}
