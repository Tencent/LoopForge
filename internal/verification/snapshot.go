// Package verification ports AegisOS's deterministic verification chain:
// snapshot tree hashes, build/toolchain fingerprints, container Maven runs and
// surefire parsing. The Go sidecar (internal/verifysidecar) exposes it over
// HTTP. Ported from AegisOS worker (SnapshotHandler, FingerprintHandler,
// VerificationRunner, SurefireReports); AegisOS itself stays frozen.
package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// WorkTree hashes the current working tree as a git tree object without
// touching the repository's real index. AegisOS does the same in
// SnapshotHandler (temporary index, `git add -A`, `git write-tree`) so a run
// can be verified without committing.
func WorkTree(ctx context.Context, repo string) (string, error) {
	index, err := os.CreateTemp("", "loopforge-snapshot-index-*")
	if err != nil {
		return "", err
	}
	indexPath := index.Name()
	_ = index.Close()
	_ = os.Remove(indexPath)
	defer os.Remove(indexPath)

	if _, err := gitWithIndex(ctx, repo, indexPath, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := gitWithIndex(ctx, repo, indexPath, "write-tree")
	if err != nil {
		return "", err
	}
	tree = strings.TrimSpace(tree)
	if !isGitTreeHash(tree) {
		return "", fmt.Errorf("write-tree returned %q, not a git tree hash", tree)
	}
	return tree, nil
}

// DiffHash returns the sha256 of the binary patch between base and the current
// working tree. The patch itself is returned so callers can include it in a
// critique request without recomputing it.
func DiffHash(ctx context.Context, repo, base string) (string, []byte, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "HEAD"
	}
	patchText, err := git(ctx, repo, "diff", "--binary", base, "--")
	if err != nil {
		return "", nil, err
	}
	patch := []byte(patchText)
	sum := sha256.Sum256(patch)
	return hex.EncodeToString(sum[:]), patch, nil
}

// BuildFingerprint hashes every build file path and blob in sorted order,
// mirroring AegisOS FingerprintHandler.BUILD_FILE_NAMES.
func BuildFingerprint(ctx context.Context, repo, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "HEAD"
	}
	output, err := git(ctx, repo, "ls-tree", "-r", "-z", ref)
	if err != nil {
		return "", fmt.Errorf("list tree %s: %w", ref, err)
	}
	var files []fileEntry
	for _, record := range strings.Split(output, "\x00") {
		meta, path, ok := strings.Cut(record, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		if !buildFileNames[filepath.Base(strings.TrimPrefix(path, "./"))] {
			continue
		}
		files = append(files, fileEntry{path: strings.TrimPrefix(path, "./"), hash: fields[2]})
	}
	sortFileEntries(files)
	var canonical strings.Builder
	for _, file := range files {
		canonical.WriteString(file.path)
		canonical.WriteByte(' ')
		canonical.WriteString(file.hash)
		canonical.WriteByte('\n')
	}
	return sha256Hex([]byte(canonical.String())), nil
}

var buildFileNames = map[string]bool{
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true,
	"settings.gradle": true, "settings.gradle.kts": true,
	"gradle.properties": true, "maven-wrapper.properties": true,
	"toolchains.xml": true,
}

// ToolchainFingerprint names the toolchain that will grade the tree. With a
// Maven image it is the image reference (the image pins the JDK); without one
// it falls back to the local Go toolchain, matching the pre-port stub.
func ToolchainFingerprint(mavenImage string) string {
	if image := strings.TrimSpace(mavenImage); image != "" {
		return sha256Hex([]byte("maven-image=" + image))
	}
	return sha256Hex([]byte(strings.Join([]string{
		"sidecar=agentflow-verify-sidecar",
		"go=" + goVersion(),
		"os=" + goOS(),
		"arch=" + goArch(),
	}, ";")))
}

type gitTimeoutKey struct{}

// WithGitTimeout bounds each git invocation created by this package.
func WithGitTimeout(ctx context.Context, limit time.Duration) context.Context {
	if limit <= 0 {
		limit = 30 * time.Second
	}
	return context.WithValue(ctx, gitTimeoutKey{}, limit)
}

func git(ctx context.Context, repo string, args ...string) (string, error) {
	return gitEnv(ctx, repo, nil, args...)
}

func gitWithIndex(ctx context.Context, repo, indexPath string, args ...string) (string, error) {
	return gitEnv(ctx, repo, []string{"GIT_INDEX_FILE=" + indexPath}, args...)
}

func gitEnv(ctx context.Context, repo string, extraEnv []string, args ...string) (string, error) {
	limit := 30 * time.Second
	if set, ok := ctx.Value(gitTimeoutKey{}).(time.Duration); ok {
		limit = set
	}
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(callCtx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, extraEnv...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(output), nil
}
