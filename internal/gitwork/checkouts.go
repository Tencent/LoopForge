package gitwork

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Checkout is a git checkout found in a workspace: its remote URLs and the
// commits its local branches point at.
type Checkout struct {
	Dir     string   `json:"dir"`
	Remotes []string `json:"remotes"`
	Heads   []string `json:"heads"`
}

const (
	checkoutScanDepth = 2
	checkoutScanLimit = 50
)

// Checkouts lists the git checkouts at most two directory levels below root
// (root itself included). Agents clone wherever they like, so the bound
// repository list says nothing about them. The files may belong to a sandbox
// user, hence safe.directory.
func Checkouts(ctx context.Context, root string) []Checkout {
	dirs := []string{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if len(dirs) >= checkoutScanLimit {
			return
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			dirs = append(dirs, dir)
			return
		}
		if depth == checkoutScanDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() || strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				continue
			}
			walk(filepath.Join(dir, name), depth+1)
		}
	}
	walk(root, 0)
	out := []Checkout{}
	for _, dir := range dirs {
		remotes, _ := Run(ctx, dir, "-c", "safe.directory=*", "config", "--get-regexp", `^remote\..*\.url$`)
		heads, err := Run(ctx, dir, "-c", "safe.directory=*", "for-each-ref", "--format=%(objectname)", "refs/heads")
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, dir)
		checkout := Checkout{Dir: filepath.ToSlash(rel), Heads: uniqueLines(heads)}
		for _, line := range strings.Split(remotes, "\n") {
			if _, url, ok := strings.Cut(strings.TrimSpace(line), " "); ok && strings.TrimSpace(url) != "" {
				checkout.Remotes = append(checkout.Remotes, strings.TrimSpace(url))
			}
		}
		out = append(out, checkout)
	}
	return out
}

func uniqueLines(text string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" && !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}
