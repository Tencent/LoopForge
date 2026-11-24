package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const dockerGitToolsMount = "/run/loopforge-git"

var errDockerGitToolsMountMissing = errors.New("此 Docker 容器缺少 Git 凭据只读挂载，请在当前任务和终端结束后重新打开工作区")

// Take a private snapshot even when the installed distribution uses links.
// Prefer the executable from libexec when available, including on macOS where
// the command on PATH may be the developer tools launcher. Docker images
// without Git publish an empty distribution.
const copyGitDistribution = `git="${2:-}"
if [ -z "$git" ]; then git=$(command -v git) || exit 0; fi
execpath="${3:-}"
if [ -z "$execpath" ]; then execpath=$("$git" --exec-path); fi
mkdir "$1/exec"
cp -aL "$execpath/." "$1/exec/"
if [ -x "$execpath/git" ]; then git="$execpath/git"; fi
cp -pL "$git" "$1/git"
cp -pL "$execpath/git-remote-http" "$1/git-remote-http"`

func nativeGitToolsRoot() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate native Git helper cache: %w", err)
	}
	return filepath.Join(cache, "LoopForge", "git-helpers"), nil
}

func ensureRunnerGitDir(dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("Git helper directory must be owned by the Runner: %s", dir)
	}
	return nil
}

func dockerGitToolsDirectory(workspace, name string) (string, error) {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(workspace + "\n" + name))
	return filepath.Join(filepath.Dir(workspace), ".loopforge-git-helpers", fmt.Sprintf("%x", key[:16])), nil
}

func ensureDockerGitToolsDirectory(root string) error {
	if err := ensureRunnerGitDir(filepath.Dir(root)); err != nil {
		return err
	}
	return ensureRunnerGitDir(root)
}

func prepareWorkspaceGitTools(ctx context.Context, sandbox Sandbox, workspace string, plainHTTP bool) (root, httpBin string, err error) {
	var host string
	docker, _ := unwrapSandbox(sandbox).(*dockerSandbox)
	if docker != nil {
		if docker.workspace != "" {
			workspace = docker.workspace
		}
		host, err = dockerGitToolsDirectory(workspace, docker.name)
		if err != nil {
			return "", "", err
		}
		if err := ensureDockerGitToolsDirectory(host); err != nil {
			return "", "", err
		}
		root = dockerGitToolsMount
	} else {
		host, err = nativeGitToolsRoot()
		if err != nil {
			return "", "", err
		}
		if err := os.MkdirAll(filepath.Dir(host), 0o700); err != nil {
			return "", "", err
		}
		if err := ensureRunnerGitDir(host); err != nil {
			return "", "", err
		}
		self, err := os.Executable()
		if err != nil {
			return "", "", err
		}
		info, err := os.Stat(self)
		if err != nil {
			return "", "", err
		}
		key := sha256.Sum256([]byte(fmt.Sprintf("%s\n%d\n%d", self, info.Size(), info.ModTime().UnixNano())))
		host = filepath.Join(host, fmt.Sprintf("runner-%x", key[:16]))
		if err := ensureRunnerGitDir(host); err != nil {
			return "", "", err
		}
		var git string
		if plainHTTP {
			git, err = exec.LookPath("git")
			if err != nil && !errors.Is(err, exec.ErrNotFound) {
				return "", "", err
			}
			plainHTTP = git != ""
		}
		host, err = prepareNativeGitDistribution(ctx, host, git)
		if err != nil {
			return "", "", err
		}
		root = host
	}
	if err := ensureRunnerGitDir(filepath.Join(host, "bin")); err != nil {
		return "", "", err
	}
	if err := installMCPCallHelperForSandbox(ctx, sandbox, host, filepath.Join(host, "bin", "agentflow-helper")); err != nil {
		return "", "", err
	}
	helper := filepath.Join(root, "bin", "agentflow-helper")
	for _, name := range []string{"gh", "glab"} {
		if err := writeWorkspaceGitShim(host, filepath.Join("bin", name), helper, "cli-shim "+name); err != nil {
			return "", "", err
		}
	}
	if plainHTTP {
		if err := ensureRunnerGitDir(filepath.Join(host, workspaceGitHTTPBin)); err != nil {
			return "", "", err
		}
		if err := writeWorkspaceGitShim(host, filepath.Join(workspaceGitHTTPBin, "git"), helper, "git-workspace"); err != nil {
			return "", "", err
		}
		httpBin = filepath.Join(root, workspaceGitHTTPBin)
	}
	return root, httpBin, nil
}

func writeWorkspaceGitShim(root, rel, helper, command string) error {
	script := "#!/bin/sh\nexec " + shellSingleQuote(helper) + " " + command + " \"$@\"\n"
	return writeManagedFile(root, filepath.Join(root, rel), []byte(script), 0o755)
}

func prepareNativeGitDistribution(ctx context.Context, root, git string) (string, error) {
	if git == "" {
		dir := filepath.Join(root, "credential")
		return dir, ensureRunnerGitDir(dir)
	}
	output, err := exec.CommandContext(ctx, git, "--exec-path").Output()
	if err != nil {
		return "", fmt.Errorf("read installed Git helper path: %w", err)
	}
	installed, err := filepath.Abs(strings.TrimSpace(string(output)))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(installed)
	if err != nil {
		return "", err
	}
	gitInfo, err := os.Stat(git)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%d\n%d", git, installed, info.ModTime().UnixNano(), gitInfo.ModTime().UnixNano())))
	dir := filepath.Join(root, fmt.Sprintf("git-%x", key[:16]))
	if err := ensureRunnerGitDir(dir); err != nil {
		return "", err
	}
	distribution := filepath.Join(dir, workspaceGitDistribution)
	if err := publishGitDistribution(distribution, func(stage string) error {
		if out, err := exec.CommandContext(ctx, "sh", "-ec", copyGitDistribution, "loopforge-git-install", stage, git, installed).CombinedOutput(); err != nil {
			return fmt.Errorf("copy installed Git helpers: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return writeWorkspaceGitShim(stage, "exec/git-remote-http", filepath.Join(dir, "bin", "agentflow-helper"), "git-remote-http")
	}); err != nil {
		return "", err
	}
	return dir, nil
}

// Stage the distribution before publishing it so simultaneous turns never
// observe a partially installed helper directory.
func publishGitDistribution(path string, install func(string) error) error {
	if info, err := os.Lstat(path); err == nil {
		if info.IsDir() {
			return nil
		}
		return fmt.Errorf("Git distribution is not a directory: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(path), "stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := install(stage); err != nil {
		return err
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		return err
	}
	if err := os.Rename(stage, path); err != nil {
		if info, statErr := os.Lstat(path); statErr != nil || !info.IsDir() {
			return err
		}
	}
	return nil
}

// Read the Git installation from the image with no workspace, credentials or
// network attached. A live workspace container can have changed its own files.
func prepareDockerGitTools(ctx context.Context, root, container string) error {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--type", "container", "--format", "{{.Image}}", container).Output()
	if err != nil {
		return fmt.Errorf("inspect sandbox Git image: %w", err)
	}
	imageID := strings.TrimSpace(string(out))
	if !strings.HasPrefix(imageID, "sha256:") {
		return fmt.Errorf("could not identify the sandbox Git image")
	}
	return prepareDockerGitDistribution(ctx, root, imageID)
}

func prepareDockerGitDistribution(ctx context.Context, root, imageID string) error {
	key := sha256.Sum256([]byte(imageID))
	name := fmt.Sprintf("image-%x", key[:16])
	if err := publishGitDistribution(filepath.Join(root, name), func(stage string) error {
		args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop=ALL", "--security-opt", "no-new-privileges",
			"--user", fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()), "--mount", "type=bind,src=" + stage + ",dst=/git-install",
			"--entrypoint", "sh", imageID, "-ec", copyGitDistribution, "loopforge-git-install", "/git-install"}
		if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("copy Git helpers from sandbox image: %w: %s", err, strings.TrimSpace(string(out)))
		}
		if _, err := os.Stat(filepath.Join(stage, "git")); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		return writeWorkspaceGitShim(stage, "exec/git-remote-http", dockerGitToolsMount+"/bin/agentflow-helper", "git-remote-http")
	}); err != nil {
		return err
	}
	link := filepath.Join(root, "distribution-"+rand.Text())
	if err := os.Symlink(name, link); err != nil {
		return err
	}
	defer os.Remove(link)
	return os.Rename(link, filepath.Join(root, workspaceGitDistribution))
}
