package gitwork

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
)

type BrowserRepository struct {
	Path       string
	Label      string
	discovered bool
}

type browserRepositoriesKey struct{}

const browserCheckoutScanDepth = 8

var ErrBrowserGitUnavailable = errors.New("工作区沙箱暂不可用，请打开 IDE 或启动工作区后重试")

// DiscoverBrowserRepositories scans an unbound conversation workspace without
// using Git or trusting any checkout configuration on the host.
func DiscoverBrowserRepositories(ctx context.Context, root string) []BrowserRepository {
	// Root checkouts and configured repositories keep their existing layout.
	if _, err := os.Lstat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		return nil
	}
	var repositories []BrowserRepository
	for _, dir := range checkoutDirs(ctx, root, browserCheckoutScanDepth, false) {
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == "." || !filepath.IsLocal(rel) {
			continue
		}
		if head, err := os.Lstat(filepath.Join(dir, ".git", "HEAD")); err != nil || !head.Mode().IsRegular() {
			continue
		}
		repositories = append(repositories, BrowserRepository{Path: dir, Label: filepath.ToSlash(rel), discovered: true})
	}
	return repositories
}

func WithBrowserRepositories(ctx context.Context, repositories []BrowserRepository) context.Context {
	return context.WithValue(ctx, browserRepositoriesKey{}, repositories)
}

func BrowserRepositoryRoots(ctx context.Context, root string, bindings []domain.RepositoryContext) []BrowserRepository {
	if len(bindings) == 0 {
		if discovered, _ := ctx.Value(browserRepositoriesKey{}).([]BrowserRepository); len(discovered) != 0 {
			return discovered
		}
		return []BrowserRepository{{Path: root}}
	}
	repositories := make([]BrowserRepository, 0, len(bindings))
	for _, binding := range bindings {
		dir := filepath.Clean(filepath.FromSlash(binding.WorkspaceDir))
		if !filepath.IsLocal(dir) {
			continue
		}
		if dir == "." {
			dir = ""
		}
		repositories = append(repositories, BrowserRepository{Path: filepath.Join(root, dir), Label: filepath.ToSlash(dir)})
	}
	return repositories
}

func FindBrowserRepository(ctx context.Context, root string, bindings []domain.RepositoryContext, name string) (BrowserRepository, bool) {
	name = strings.TrimSpace(name)
	roots := BrowserRepositoryRoots(ctx, root, bindings)
	if name == "" || name == "." || name == "workspace" {
		for _, repository := range roots {
			if repository.Label == "" {
				return repository, true
			}
		}
	}
	for _, repository := range roots {
		if repository.Label == name {
			return repository, true
		}
	}
	if (name == "" || name == "." || name == "workspace") && len(roots) == 1 {
		return roots[0], true
	}
	return BrowserRepository{}, false
}

func BrowserGitOutput(ctx context.Context, repository BrowserRepository, args ...string) ([]byte, error) {
	options := []string{"--no-optional-locks", "-c", "core.quotePath=false"}
	if !repository.discovered {
		return exec.CommandContext(ctx, "git", append(append(options, "-C", repository.Path), args...)...).Output()
	}
	run := workspaceExecutor(ctx)
	if run == nil {
		return nil, ErrBrowserGitUnavailable
	}
	// A conversation can modify the entire checkout. Run its Git commands
	// in the same execution sandbox, with no Git credentials or host fallback.
	options = append(options, "--git-dir=.git", "--work-tree=.")
	output, err := run(ctx, repository.Path, WorkspaceCommand{Args: append(options, args...), StdoutOnly: true, Offline: true})
	return []byte(output), err
}

// BrowserDiffArgs keeps paths distinct when different repositories contain a
// file with the same name. A single path comes from the workspace path check.
func BrowserDiffArgs(repository BrowserRepository, path string) []string {
	prefix := repository.Label
	if prefix != "" {
		prefix += "/"
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--src-prefix=a/" + prefix, "--dst-prefix=b/" + prefix, "HEAD", "--"}
	if path != "" {
		return append(args, ":(literal)"+filepath.ToSlash(path))
	}
	if repository.discovered {
		for _, name := range []string{".git", ".agentflow", ".ssh", ".gnupg", ".netrc", ".npmrc", "id_rsa", "id_ed25519", ".env", ".env.*"} {
			args = append(args, ":(exclude,icase,glob)**/"+name, ":(exclude,icase,glob)**/"+name+"/**")
		}
	}
	return args
}

func BrowserRepositoryRelativePath(repository BrowserRepository, path string) (string, bool) {
	if repository.Label == "" {
		return path, true
	}
	return strings.CutPrefix(path, repository.Label+"/")
}
