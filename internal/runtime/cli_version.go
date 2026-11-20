package runtime

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/managedcli"
)

const CLIVersionEvent = "cli.version"

const (
	cliVersionOutputLimit = 8 << 10
	cliVersionTimeout     = 5 * time.Second
	cliVersionNumber      = `[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?`
)

var (
	cliVersionValue = regexp.MustCompile("^" + cliVersionNumber + "$")
	cliVersionLine  = regexp.MustCompile(`(?i)^(?:([a-z][a-z0-9 -]*?)[ /:]+)?(v?` + cliVersionNumber + `)(?:\s+\([^()]+\))?$`)
)

// NormalizeCLIVersion accepts only a version number, without its display prefix.
func NormalizeCLIVersion(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if len(value) <= 80 && cliVersionValue.MatchString(value) {
		return value
	}
	return ""
}

// ObserveCLIVersion reports installation metadata immediately when available,
// then falls back to an isolated probe while the actual invocation is active.
// Invoke the returned cancellation function when the invocation exits.
func ObserveCLIVersion(ctx context.Context, sandbox Sandbox, provider, binary string, report func(version, source string)) context.CancelFunc {
	if !filepath.IsAbs(binary) || ctx.Err() != nil {
		return func() {}
	}
	if _, known := onDemandCLICatalog()[provider]; !known && !IsAgentCLI(provider) {
		return func() {}
	}
	sandbox = unwrapSandbox(sandbox)
	if _, docker := sandbox.(*dockerSandbox); !docker {
		if version, source := cliInstallationVersion(provider, binary); version != "" {
			report(version, source)
			return func() {}
		}
	}
	// Native workspace requirements can be reassigned after its lease ends.
	if native, ok := sandbox.(*processSandbox); ok {
		snapshot := *native
		sandbox = &snapshot
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		var version string
		switch sandbox := sandbox.(type) {
		case *dockerSandbox:
			version = sandbox.probeCLIVersion(ctx, provider, binary)
		case *processSandbox, nil:
			native, _ := sandbox.(*processSandbox)
			version = probeHostCLIVersion(ctx, native, provider, binary)
		}
		if version != "" && ctx.Err() == nil {
			report(version, "version_probe")
		}
	}()
	return cancel
}

func cliInstallationVersion(provider, binary string) (version, source string) {
	if root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT")); root != "" {
		if inventory, err := managedcli.ReadInventory(root); err == nil {
			if install, ok := inventory[provider]; ok && install.Binary == binary {
				if version := NormalizeCLIVersion(install.Version); version != "" {
					return version, "managed_inventory"
				}
			}
		}
	}
	pkgName := onDemandCLICatalog()[provider].Package
	if pkgName == "" {
		return "", ""
	}
	if pin := strings.LastIndex(pkgName, "@"); pin > 0 {
		pkgName = pkgName[:pin]
	}
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return "", ""
	}
	for dir, depth := filepath.Dir(resolved), 0; depth < 5; dir, depth = filepath.Dir(dir), depth+1 {
		path := filepath.Join(dir, "package.json")
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
			continue
		}
		raw, err := os.ReadFile(path)
		var pkg struct{ Name, Version string }
		if err == nil && json.Unmarshal(raw, &pkg) == nil && pkg.Name == pkgName {
			if version := NormalizeCLIVersion(pkg.Version); version != "" {
				return version, "package_metadata"
			}
		}
	}
	return "", ""
}

func cliVersionFromOutput(provider, output string) string {
	binary := conversationBinary(provider)
	label := strings.ToLower(onDemandCLILabel(provider))
	for line := range strings.SplitSeq(output, "\n") {
		match := cliVersionLine.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 3 {
			continue
		}
		prefix := strings.TrimSuffix(strings.ToLower(match[1]), " version")
		switch prefix {
		case "", "version", provider, binary, binary + "-cli", label, label + " cli":
			return NormalizeCLIVersion(match[2])
		}
	}
	return ""
}

func probeHostCLIVersion(ctx context.Context, sandbox *processSandbox, provider, binary string) string {
	ctx, cancel := context.WithTimeout(ctx, cliVersionTimeout)
	defer cancel()
	home, err := os.MkdirTemp("", "loopforge-cli-version-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(home)
	command := CommandSpec{Argv: []string{binary, "--version"}, WorkingDir: home, OwnProcessGroup: true}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + home}
	var cmd *exec.Cmd
	if sandbox == nil {
		cmd, err = (&localSandbox{workspace: home}).command(ctx, command, env)
	} else {
		// bwrap mounts its own /tmp. Bind only the disposable probe directory.
		command.privateBinds = []string{home}
		cmd, err = sandbox.command(ctx, command, env)
	}
	if err != nil {
		return ""
	}
	// The sandbox profile and normal command can supply credentials and
	// config paths. The version process only needs PATH and its scratch HOME.
	cmd.Env = upsertEnv(env, "PATH", lookupEnvSlice(cmd.Env, "PATH"))
	return cliVersionFromOutput(provider, runCLIVersionProbe(cmd))
}

type cliVersionOutput struct{ buffer bytes.Buffer }

func (b *cliVersionOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > cliVersionOutputLimit {
		return 0, errors.New("CLI version output too large")
	}
	return b.buffer.Write(p)
}

func runCLIVersionProbe(cmd *exec.Cmd) string {
	var output cliVersionOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.WaitDelay = 200 * time.Millisecond
	err := cmd.Run()
	if cmd.Process != nil && cmd.Cancel != nil {
		_ = cmd.Cancel()
	}
	if err != nil {
		return ""
	}
	return output.buffer.String()
}

func (s *dockerSandbox) probeCLIVersion(ctx context.Context, provider, binary string) string {
	ctx, cancel := context.WithTimeout(ctx, cliVersionTimeout+2*time.Second)
	defer cancel()
	// Bind the probe to this container even if a later turn recreates the
	// sandbox under the same name. The probe never holds its execution gate.
	rawID, err := exec.CommandContext(ctx, "docker", "inspect", "--type", "container", "--format", "{{.Id}}", s.name).Output()
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(rawID))
	if _, err := hex.DecodeString(id); len(id) != 64 || err != nil {
		return ""
	}
	args := []string{"exec", "--workdir", "/tmp"}
	if path, ok := s.profile.Environment["PATH"]; ok {
		args = append(args, "-e", "PATH="+path)
	}
	args = append(args, s.toolchainExecEnv()...)
	args = append(args, id, "sh", "-c", dockerCLIVersionLaunch, "loopforge-cli-version",
		dockerConversationProcessSupervisor, dockerCLIVersionProbe, binary,
		strconv.Itoa(int(cliVersionTimeout/time.Second)), dockerCLIVersionCommand)
	return cliVersionFromOutput(provider, runCLIVersionProbe(exec.CommandContext(ctx, "docker", args...)))
}

// The parent owns the scratch directory. A timeout is enforced inside the
// container even if the Go client cancels its Docker connection. The existing
// supervisor reaps detached descendants before the parent removes the directory.
const dockerCLIVersionLaunch = toolchainPathSetup + dockerCLISharedPathSetup + `set -eu
dir=$(mktemp -d /tmp/loopforge-cli-version.XXXXXXXX)
trap 'rm -rf -- "$dir"' EXIT
cd "$dir"
python3 -I -c "$1" "$dir/probe.pid" python3 -I -c "$2" "$3" "$4" "$5"
`

// Login profiles can set the interpreter's PATH. Redirect only the executable's
// output into the capture file, so a profile banner cannot become a version.
const dockerCLIVersionCommand = toolchainPathSetup + dockerCLISharedPathSetup + `exec env -i "PATH=$PATH" "HOME=$2" "TMPDIR=$2" "$1" --version > "$3" 2>&1
`

const dockerCLIVersionProbe = `import os,resource,subprocess,sys,tempfile
home=os.getcwd()
env={"PATH": os.environ["PATH"], "HOME": home, "TMPDIR": home}
if "AGENTFLOW_TOOLCHAIN" in os.environ:
    env["AGENTFLOW_TOOLCHAIN"]=os.environ["AGENTFLOW_TOOLCHAIN"]
with tempfile.TemporaryFile(dir=home) as output:
    resource.setrlimit(resource.RLIMIT_FSIZE, (8192, 8192))
    try:
        output_path="/proc/self/fd/"+str(output.fileno())
        result=subprocess.run(["sh", "-lc", sys.argv[3], "loopforge-cli-version", sys.argv[1], home, output_path],
                              env=env, pass_fds=(output.fileno(),), timeout=int(sys.argv[2]),
                              stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except subprocess.TimeoutExpired:
        sys.exit(1)
    if result.returncode == 0:
        output.seek(0)
        sys.stdout.buffer.write(output.read())
`
