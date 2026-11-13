package gitwork

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// MaxPatchBytes bounds one repository-patch inventory; it matches the
// artifact size limit enforced by the control plane.
const MaxPatchBytes = 1 << 20

// BaselinePath is the frozen checkout SHA kept inside .git so it can never
// leak into a worker patch.
func BaselinePath(workspace string) string {
	return filepath.Join(workspace, ".git", "agentflow-baseline")
}

// RecordBaseline freezes the checkout a run's patches are measured against.
// Nodes may create real commits; artifact handoff is always calculated
// against this frozen checkout rather than the moving HEAD. An existing
// record is kept.
func RecordBaseline(workspace, baseBranch string) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	const path = ".git/agentflow-baseline"
	if info, err := root.Lstat(".git"); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("repository .git is not a real directory")
	}
	if info, err := root.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("repository baseline is not a regular file")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	var sha []byte
	// The ref travels on stdin so a branch name never reaches git's argument list.
	resolve := func(ref string) ([]byte, error) {
		if strings.ContainsAny(ref, "\r\n") {
			return nil, fmt.Errorf("invalid ref %q", ref)
		}
		cmd := exec.Command("git", "cat-file", "--batch-check=%(objectname)")
		cmd.Dir = workspace
		cmd.Stdin = strings.NewReader(ref + "^{commit}\n")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		sha := strings.TrimSpace(string(out))
		if !isObjectID(sha) {
			return nil, fmt.Errorf("resolve %s: %s", ref, sha)
		}
		return []byte(sha), nil
	}
	if ref := strings.TrimSpace(baseBranch); ref != "" {
		sha, err = resolve("origin/" + ref)
	}
	if err != nil || len(bytes.TrimSpace(sha)) == 0 {
		sha, err = resolve("origin/master")
	}
	if err != nil {
		sha, err = resolve("origin/main")
	}
	if err != nil {
		sha, err = resolve("HEAD")
	}
	if err != nil {
		return fmt.Errorf("resolve repository baseline: %w", err)
	}
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		if info, statErr := root.Lstat(path); statErr == nil && info.Mode().IsRegular() {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("record repository baseline: %w", err)
	}
	_, err = file.WriteString(strings.TrimSpace(string(sha)))
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("record repository baseline: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

// PatchInventory lists workspace changes against the nearest current
// baseline (usually origin/integrate or origin/<base>). A run shares one git
// workspace across nodes, so the next node reads the files there; the
// inventory is only a filename list for the prompt. Executor-local secrets
// and SOP deliverable markdown are excluded. Untracked source files are
// intent-added so they appear in the list. No recorded baseline yields nil.
func PatchInventory(workspace string) ([]byte, error) {
	// Parallel branches finishing together inventory the same checkout;
	// StageUntracked's git add -N would race on .git/index.lock.
	unlock := lockPatchWorkspace(workspace)
	defer unlock()
	baseline, err := os.ReadFile(BaselinePath(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sha := strings.TrimSpace(string(baseline))
	if sha == "" {
		return nil, nil
	}
	sha = nearestPatchBaseline(workspace, sha)
	if err := StageUntracked(workspace); err != nil {
		return nil, err
	}
	nameStatus, err := diffAgainstBaseline(workspace, sha, "--name-status")
	if err != nil {
		return nil, err
	}
	numstat, err := diffAgainstBaseline(workspace, sha, "--numstat")
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(nameStatus)) == 0 && len(bytes.TrimSpace(numstat)) == 0 {
		return nil, nil
	}
	summary := []byte(RenderPatchInventory(ParseNameStatusNumstat(nameStatus, numstat)))
	if len(bytes.TrimSpace(summary)) == 0 {
		return nil, nil
	}
	if len(summary) > MaxPatchBytes {
		return nil, fmt.Errorf("repository-patch exceeds %d bytes", MaxPatchBytes)
	}
	return summary, nil
}

func diffAgainstBaseline(workspace, sha string, extra ...string) ([]byte, error) {
	args := append([]string{"-C", workspace, "diff"}, extra...)
	args = append(args, sha, "--", ".")
	args = append(args, ExcludePathspecs...)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() > 1 {
			return nil, fmt.Errorf("git diff baseline: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return out, nil
}

// nearestPatchBaseline picks the closest origin ref that is an ancestor of
// HEAD, so a later merge of integrate/main does not turn the handoff patch
// into "everything since SOP checkout". The recorded checkout SHA is the
// fallback when no nearer upstream exists. The current branch tip is skipped
// so a pushed work branch does not collapse the patch to empty.
func nearestPatchBaseline(workspace, recorded string) string {
	headOut, err := exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
	if err != nil {
		return recorded
	}
	head := strings.TrimSpace(string(headOut))
	best := strings.TrimSpace(recorded)
	bestCount, bestOK := revListCount(workspace, best, head)
	if !bestOK {
		bestCount = int(^uint(0) >> 1)
	}
	refs, err := exec.Command("git", "-C", workspace, "for-each-ref", "--format=%(objectname) %(refname:short)", "refs/remotes/origin").Output()
	if err != nil {
		return best
	}
	for _, line := range strings.Split(string(refs), "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || sha == "" || sha == head {
			continue
		}
		if strings.EqualFold(name, "origin/HEAD") {
			continue
		}
		if exec.Command("git", "-C", workspace, "merge-base", "--is-ancestor", sha, head).Run() != nil {
			continue
		}
		n, ok := revListCount(workspace, sha, head)
		if !ok {
			continue
		}
		if n < bestCount {
			best, bestCount = sha, n
		}
	}
	if best == "" {
		return recorded
	}
	return best
}

func revListCount(workspace, from, to string) (int, bool) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" {
		return 0, false
	}
	out, err := exec.Command("git", "-C", workspace, "rev-list", "--count", from+".."+to).Output()
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	return n, err == nil
}

// PatchFileSummary is one file line of a repository-patch inventory.
type PatchFileSummary struct {
	Path       string
	Status     string
	Added      int
	Deleted    int
	Binary     bool
	RenameFrom string
}

func RenderPatchInventory(files []PatchFileSummary) string {
	var added, deleted int
	for _, f := range files {
		added += f.Added
		deleted += f.Deleted
	}
	var b strings.Builder
	b.WriteString("repository-patch summary\n")
	b.WriteString("full unified diff is not stored; read the workspace with git status / git diff\n")
	fmt.Fprintf(&b, "files %d  +%d/-%d\n", len(files), added, deleted)
	if len(files) == 0 {
		return strings.TrimSpace(b.String())
	}
	b.WriteByte('\n')
	const maxFiles = 400
	for i, f := range files {
		if i == maxFiles {
			fmt.Fprintf(&b, "… and %d more files\n", len(files)-maxFiles)
			break
		}
		path := f.Path
		if f.RenameFrom != "" {
			path = f.RenameFrom + " -> " + f.Path
		}
		if f.Binary {
			fmt.Fprintf(&b, "%s\t%s\tbinary\n", f.Status, path)
			continue
		}
		fmt.Fprintf(&b, "%s\t%s\t+%d/-%d\n", f.Status, path, f.Added, f.Deleted)
	}
	return strings.TrimSpace(b.String())
}

// ParseNameStatusNumstat merges `git diff --name-status` and
// `git diff --numstat` output into one summary per path, sorted by path.
func ParseNameStatusNumstat(nameStatus, numstat []byte) []PatchFileSummary {
	files := map[string]*PatchFileSummary{}
	for _, line := range strings.Split(string(nameStatus), "\n") {
		path, status, from, ok := parseNameStatusLine(line)
		if !ok {
			continue
		}
		if f := patchSummaryFor(files, path); f != nil {
			f.Status = status
			f.RenameFrom = from
		}
	}
	for _, line := range strings.Split(string(numstat), "\n") {
		path, added, deleted, binary, ok := parseNumstatLine(line)
		if !ok {
			continue
		}
		f := patchSummaryFor(files, path)
		if f == nil {
			continue
		}
		if binary {
			f.Binary = true
			if f.Status == "M" {
				f.Status = "B"
			}
			continue
		}
		f.Added, f.Deleted = added, deleted
	}
	out := make([]PatchFileSummary, 0, len(files))
	for _, f := range files {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// patchSummaryFor returns the summary for the normalized path, creating a
// modified ("M") entry on first sight; it returns nil for an empty path.
func patchSummaryFor(files map[string]*PatchFileSummary, rawPath string) *PatchFileSummary {
	path := filepath.ToSlash(UnquoteLsFilesPath(rawPath))
	if path == "" {
		return nil
	}
	f, ok := files[path]
	if !ok {
		f = &PatchFileSummary{Path: path, Status: "M"}
		files[path] = f
	}
	return f
}

// parseNameStatusLine parses one `--name-status` line into its raw path, the
// single-letter status and, for renames/copies, the normalized source path.
func parseNameStatusLine(line string) (path, status, from string, ok bool) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return "", "", "", false
	}
	parts := strings.Split(line, "\t")
	if len(parts) < 2 || parts[0] == "" {
		return "", "", "", false
	}
	status = string(parts[0][0])
	path = parts[len(parts)-1]
	if (status == "R" || status == "C") && len(parts) >= 3 {
		from = filepath.ToSlash(UnquoteLsFilesPath(parts[1]))
		path = parts[2]
	}
	return path, status, from, true
}

// parseNumstatLine parses one `--numstat` line; binary files report "-" for
// both counts. Unparseable counts read as zero.
func parseNumstatLine(line string) (path string, added, deleted int, binary, ok bool) {
	parts := strings.Split(strings.TrimRight(line, "\r"), "\t")
	if len(parts) < 3 {
		return "", 0, 0, false, false
	}
	path = parts[len(parts)-1]
	if parts[0] == "-" || parts[1] == "-" {
		return path, 0, 0, true, true
	}
	added, _ = strconv.Atoi(parts[0])
	deleted, _ = strconv.Atoi(parts[1])
	return path, added, deleted, false, true
}

var patchWorkspaceLocks sync.Map // cleaned absolute path -> *sync.Mutex

func lockPatchWorkspace(workspace string) func() {
	key := filepath.Clean(workspace)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	value, _ := patchWorkspaceLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
