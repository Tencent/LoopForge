package managedcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// CommandRunner is replaceable in tests: tests must never install real CLIs or
// consume model credentials. Production always executes an argv, never a shell.
type CommandRunner func(context.Context, string, []string, string, []string) ([]byte, error)

func (m *Manager) command(ctx context.Context, name string, args []string, dir string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	env := m.commandEnvironment()
	out, err := m.RunCommand(ctx, name, args, dir, env)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("CLI command timeout/cancellation: %w", ctx.Err())
	}
	return out, err
}

func (m *Manager) commandEnvironment() []string {
	// Explicit allowlist: no platform bootstrap tokens, provider API keys,
	// task credentials, NODE_OPTIONS or shell-injected npm configuration.
	env := []string{}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"} {
		if value, ok := os.LookupEnv(key); ok && key != "PATH" {
			env = append(env, key+"="+value)
		}
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	if m.nodeBinary != "" {
		path = filepath.Dir(m.nodeBinary) + string(os.PathListSeparator) + path
	}
	home := filepath.Join(m.Root, "updater-home")
	return append(env,
		"PATH="+path, "HOME="+home, "TMPDIR="+filepath.Join(m.Root, "tmp"),
		"CODEX_HOME="+filepath.Join(home, ".codex"), "CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude"), "CODEBUDDY_CONFIG_DIR="+filepath.Join(home, ".codebuddy"),
		"DISABLE_AUTOUPDATER=1", "DISABLE_UPDATES=1", "CI=1", "NO_COLOR=1",
		"NPM_CONFIG_USERCONFIG="+filepath.Join(home, "npmrc-user"), "NPM_CONFIG_GLOBALCONFIG="+filepath.Join(home, "npmrc-global"),
		"NPM_CONFIG_CACHE="+filepath.Join(m.Root, "npm-cache"),
	)
}

func (m *Manager) prepareTools(ctx context.Context) error {
	for _, rel := range []string{"updater-home", "tmp", "npm-cache", "providers"} {
		if err := prepareDirectory(m.Root, rel); err != nil {
			return err
		}
	}
	for _, name := range []string{"npmrc-user", "npmrc-global"} {
		if err := emptyNPMConfig(filepath.Join(m.Root, "updater-home", name)); err != nil {
			return err
		}
	}
	for name, target := range map[string]*string{"node": &m.nodeBinary, "npm": &m.npmBinary} {
		if *target == "" {
			path, err := exec.LookPath(name)
			if err != nil {
				return fmt.Errorf("managed CLI updater requires %s: %w", name, err)
			}
			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			*target = absolute
		}
	}
	out, err := m.command(ctx, m.nodeBinary, []string{"--version"}, m.Root)
	if err != nil {
		return err
	}
	version := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	if !releaseVersion.MatchString(version) {
		return errors.New("cannot identify Node.js version")
	}
	major, _ := strconv.Atoi(strings.Split(version, ".")[0])
	if major < 22 {
		return errors.New("managed CLI updater requires Node.js 22 or newer")
	}
	return nil
}

func (m *Manager) install(ctx context.Context, provider string, candidate release) (Installation, error) {
	spec := providerSpecs[provider]
	if err := prepareDirectory(m.Root, filepath.Join("providers", provider)); err != nil {
		return Installation{}, err
	}
	base := filepath.Join(m.Root, "providers", provider)
	stage, err := os.MkdirTemp(base, ".staging-")
	if err != nil {
		return Installation{}, err
	}
	// Only a freshly-created staging directory may be removed. Activated
	// versions are never overwritten/deleted, even if no local task uses them.
	defer func() {
		if within(base, stage) && filepath.Dir(stage) == base && strings.HasPrefix(filepath.Base(stage), ".staging-") {
			if info, err := os.Lstat(stage); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				_ = os.RemoveAll(stage)
			}
		}
	}()
	if err := os.WriteFile(filepath.Join(stage, "package.json"), []byte("{\"private\":true}\n"), 0o600); err != nil {
		return Installation{}, err
	}
	args := []string{"install", "--prefix", stage, "--registry", npmRegistry, "--no-audit", "--no-fund", "--engine-strict", "--ignore-scripts", "--include=optional", "--save-exact", spec.Package + "@" + candidate.Version}
	if _, err := m.command(ctx, m.npmBinary, args, stage); err != nil {
		return Installation{}, fmt.Errorf("install %s: %w", provider, err)
	}
	if err := verifyPackageLock(stage, spec.Package, candidate); err != nil {
		return Installation{}, err
	}
	// Some native optional dependencies need lifecycle hooks. Defer them until
	// npm has fetched the exact candidate and its SRI has been checked. Commands
	// use the updater's isolated home and credential-free environment; they run
	// with the worker's OS permissions, not in a separate security sandbox.
	rebuild := []string{"rebuild", "--prefix", stage, "--registry", npmRegistry, "--no-audit", "--no-fund"}
	if _, err := m.command(ctx, m.npmBinary, rebuild, stage); err != nil {
		return Installation{}, fmt.Errorf("build verified CLI dependencies: %w", err)
	}
	if err := verifyPackageLock(stage, spec.Package, candidate); err != nil {
		return Installation{}, err
	}
	staged := Installation{Version: candidate.Version, Binary: filepath.Join(stage, "node_modules", ".bin", spec.Binary)}
	if err := validateInventory(m.Root, Inventory{provider: staged}); err != nil {
		return Installation{}, err
	}
	// Rename a unique complete directory, never an existing installation.
	// npm's .bin links are relative and remain valid after this rename.
	final := filepath.Join(base, candidate.Version+"-"+strings.TrimPrefix(filepath.Base(stage), ".staging-"))
	if _, err := os.Lstat(final); err == nil || !os.IsNotExist(err) {
		return Installation{}, errors.New("candidate installation target already exists or is inaccessible")
	}
	if err := os.Rename(stage, final); err != nil {
		return Installation{}, err
	}
	installed := Installation{Version: candidate.Version, Binary: filepath.Join(final, "node_modules", ".bin", spec.Binary), Integrity: candidate.Dist.Integrity, InstalledAt: m.Now().UTC()}
	if err := validateInventory(m.Root, Inventory{provider: installed}); err != nil {
		return Installation{}, err
	}
	return installed, nil
}

func verifyPackageLock(dir, packageName string, candidate release) error {
	var lock struct {
		Packages map[string]struct {
			Version   string `json:"version"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	// Lockfiles can exceed the small control-manifest limit.
	path := filepath.Join(dir, "package-lock.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return errors.New("missing or invalid installed npm lockfile")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return err
	}
	actual, ok := lock.Packages["node_modules/"+packageName]
	if !ok || actual.Version != candidate.Version || actual.Integrity != candidate.Dist.Integrity {
		return errors.New("installed npm package does not match the verified version/integrity")
	}
	return nil
}

var outputVersion = regexp.MustCompile(`(?:^|[^0-9.])([0-9]+\.[0-9]+\.[0-9]+)(?:$|[^0-9.A-Za-z+-])`)

func (m *Manager) validate(ctx context.Context, provider string, installed Installation) error {
	if err := validateInventory(m.Root, Inventory{provider: installed}); err != nil {
		return err
	}
	out, err := m.command(ctx, installed.Binary, []string{"--version"}, m.Root)
	if err != nil {
		return fmt.Errorf("version probe: %w", err)
	}
	match := outputVersion.FindStringSubmatch(strings.TrimSpace(string(out)))
	if len(match) != 2 || match[1] != installed.Version {
		return errors.New("candidate executable reports an unexpected version")
	}
	spec := providerSpecs[provider]
	out, err = m.command(ctx, installed.Binary, spec.Help, m.Root)
	if err != nil {
		return fmt.Errorf("argument compatibility probe: %w", err)
	}
	for _, flag := range spec.Flags {
		pattern := regexp.MustCompile(regexp.QuoteMeta(flag) + `(?:[\s,=<\[\]]|$)`)
		if !pattern.Match(out) {
			if provider == "claude-code" && flag == "--system-prompt-file" {
				if err := m.validateClaudeHiddenFileArgument(ctx, installed); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("candidate CLI does not advertise required argument %s", flag)
		}
	}
	return nil
}
