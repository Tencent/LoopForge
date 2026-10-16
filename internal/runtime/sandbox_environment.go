package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
)

const (
	workspaceEnvironmentDir   = "agentflow-environment"
	managedEnvironmentDir     = "environment"
	environmentIDVar          = "AGENTFLOW_ENVIRONMENT"
	environmentRootVar        = "AGENTFLOW_ENVIRONMENT_ROOT"
	environmentPrefixVar      = "PREFIX"
	workspaceEnvironmentID    = "agentflow-environment"
	workspaceEnvironmentAlias = "workspace"
	installScriptName         = "install.sh"
	installStampName          = ".install-stamp"
	environmentZdotDir        = "zdot"
	environmentBashEnvName    = "bashrc"
)

// SandboxResources are catalog assets resolved before a sandbox is opened.
// Docker builds from Dockerfile; process sandboxes activate Environment.
type SandboxResources struct {
	Dockerfile  domain.DockerfileDefinition
	Environment domain.EnvironmentDefinition
}

type activatedEnvironment struct {
	Overlay       map[string]string
	Root          string
	InstallRel    string
	InstallScript string
}

func prepareSandboxEnvironment(workspace string, profile domain.SandboxProfile, catalog domain.EnvironmentDefinition) (activatedEnvironment, error) {
	driver := strings.TrimSpace(profile.Driver)
	switch driver {
	case "docker":
		return activatedEnvironment{}, nil
	case "", "local":
		if strings.TrimSpace(profile.EnvironmentID) == "" && strings.TrimSpace(catalog.ID) == "" {
			return activatedEnvironment{}, nil
		}
	}
	envID := strings.TrimSpace(profile.EnvironmentID)
	if envID == "" && strings.TrimSpace(catalog.ID) != "" {
		envID = strings.TrimSpace(catalog.ID)
	}
	root := ""
	installRel := ""
	if strings.TrimSpace(catalog.ID) != "" {
		prepared, rel, err := writeEnvironmentInstall(workspace, catalog)
		if err != nil {
			return activatedEnvironment{}, err
		}
		root = prepared
		installRel = rel
		if envID == "" {
			envID = strings.TrimSpace(catalog.ID)
		}
	}
	if root == "" {
		discovered, ok := discoverEnvironmentRoot(workspace, envID)
		if !ok {
			if envID != "" && strings.TrimSpace(catalog.ID) == "" {
				return activatedEnvironment{}, fmt.Errorf("environment %q not found in catalog or workspace", envID)
			}
			if envID == "" {
				return activatedEnvironment{}, nil
			}
			return activatedEnvironment{}, fmt.Errorf("environment %q was not prepared", envID)
		}
		root = discovered
	}
	id := envID
	if id == "" {
		id = workspaceEnvironmentID
	}
	overlay := map[string]string{
		environmentIDVar:     id,
		environmentRootVar:   root,
		environmentPrefixVar: root,
	}
	if strings.TrimSpace(catalog.ID) != "" {
		for key, value := range catalog.Variables {
			overlay[key] = expandEnvironmentPlaceholders(value, root)
		}
	}
	pathDirs := environmentPathDirs(root, catalog)
	basePath := ""
	if profile.Environment != nil {
		basePath = profile.Environment["PATH"]
	}
	if basePath == "" {
		basePath = os.Getenv("PATH")
	}
	if len(pathDirs) > 0 {
		overlay["PATH"] = strings.Join(append(append([]string{}, pathDirs...), basePath), string(os.PathListSeparator))
		zdot, bashEnv, err := writeEnvironmentShellHooks(root, pathDirs)
		if err != nil {
			return activatedEnvironment{}, err
		}
		// Codex/Claude tool calls often use `zsh -lc` / `bash -lc`. Login shells
		// rebuild PATH (macOS path_helper, ~/.zshrc) and would otherwise hide
		// $PREFIX/bin behind Homebrew. ZDOTDIR/BASH_ENV re-prepend after that.
		// `.zshenv` covers `zsh -c` without `-l`; `.zprofile` runs after path_helper.
		overlay["ZDOTDIR"] = zdot
		overlay["BASH_ENV"] = bashEnv
		overlay["ENV"] = bashEnv
	}
	return activatedEnvironment{Overlay: overlay, Root: root, InstallRel: installRel, InstallScript: catalog.InstallScript}, nil
}

func catalogDirName(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return "", fmt.Errorf("invalid catalog id %q", id)
	}
	return id, nil
}

func writeEnvironmentInstall(workspace string, env domain.EnvironmentDefinition) (root, installRel string, err error) {
	envID, err := catalogDirName(env.ID)
	if err != nil {
		return "", "", err
	}
	root, err = EnsureControlDirectory(workspace, managedEnvironmentDir, envID)
	if err != nil {
		return "", "", err
	}
	script := strings.TrimSpace(env.InstallScript)
	if script == "" {
		return root, "", nil
	}
	if !strings.HasPrefix(script, "#!") {
		script = "#!/bin/sh\n" + script
	}
	target := filepath.Join(root, installScriptName)
	if err := writeManagedFile(target, []byte(script+"\n"), 0o700); err != nil {
		return "", "", err
	}
	rel := filepath.ToSlash(filepath.Join(".agentflow", managedEnvironmentDir, envID, installScriptName))
	return root, rel, nil
}

func runEnvironmentInstall(ctx context.Context, sandbox Sandbox, activated activatedEnvironment) error {
	if strings.TrimSpace(activated.InstallRel) == "" || strings.TrimSpace(activated.InstallScript) == "" {
		return nil
	}
	stamp := filepath.Join(activated.Root, installStampName)
	sum := sha256.Sum256([]byte(activated.InstallScript))
	want := hex.EncodeToString(sum[:])
	if current, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(current)) == want {
		return nil
	}
	if _, err := sandbox.Exec(ctx, CommandSpec{Argv: []string{"sh", activated.InstallRel}}, nil); err != nil {
		return fmt.Errorf("environment install.sh: %w", err)
	}
	return writeManagedFile(stamp, []byte(want+"\n"), 0o600)
}

func discoverEnvironmentRoot(workspace, envID string) (string, bool) {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", false
	}
	if name, err := catalogDirName(envID); err == nil {
		named := filepath.Join(workspace, ".agentflow", managedEnvironmentDir, name)
		if isRealDirectory(named) {
			return named, true
		}
	}
	dropin := filepath.Join(workspace, workspaceEnvironmentDir)
	if isRealDirectory(dropin) && (envID == "" || envID == workspaceEnvironmentID || envID == workspaceEnvironmentAlias) {
		return dropin, true
	}
	return "", false
}

func writeEnvironmentShellHooks(root string, pathDirs []string) (zdot, bashEnv string, err error) {
	if root == "" || len(pathDirs) == 0 {
		return "", "", nil
	}
	zdot = filepath.Join(root, environmentZdotDir)
	if err := ensureDirectoryNoSymlink(zdot, 0o700); err != nil {
		return "", "", err
	}
	prepend := strings.Join(pathDirs, string(os.PathListSeparator))
	snippet := environmentPathHook(prepend)
	for _, name := range []string{".zshenv", ".zprofile", ".zshrc"} {
		if err := writeManagedFile(filepath.Join(zdot, name), []byte(snippet), 0o600); err != nil {
			return "", "", err
		}
	}
	bashEnv = filepath.Join(zdot, environmentBashEnvName)
	if err := writeManagedFile(bashEnv, []byte(snippet), 0o600); err != nil {
		return "", "", err
	}
	return zdot, bashEnv, nil
}

func environmentPathHook(prepend string) string {
	return "# AgentFlow: keep isolated bins first after login PATH reset.\n" +
		"PATH=" + shellSingleQuote(prepend) + ":${PATH}\n" +
		"export PATH\n"
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func environmentPathDirs(root string, catalog domain.EnvironmentDefinition) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(path string) {
		path = filepath.Clean(path)
		if path == "" || path == "." || seen[path] {
			return
		}
		seen[path] = true
		dirs = append(dirs, path)
	}
	add(filepath.Join(root, "bin"))
	for _, entry := range catalog.PathEntries {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if filepath.IsAbs(entry) {
			continue
		}
		add(filepath.Join(root, filepath.FromSlash(entry)))
	}
	return dirs
}

func isRealDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir()
}

func expandEnvironmentPlaceholders(value, prefix string) string {
	if prefix == "" || value == "" {
		return value
	}
	return strings.NewReplacer(
		"${PREFIX}", prefix,
		"${AGENTFLOW_ENVIRONMENT_ROOT}", prefix,
		"$PREFIX", prefix,
		"$AGENTFLOW_ENVIRONMENT_ROOT", prefix,
	).Replace(value)
}

func mergeEnvironmentMaps(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, layer := range layers {
		for key, value := range layer {
			out[key] = value
		}
	}
	return out
}
