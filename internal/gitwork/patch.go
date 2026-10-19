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
	path := BaselinePath(workspace)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	var sha []byte
	var err error
	if ref := strings.TrimSpace(baseBranch); ref != "" {
		sha, err = exec.Command("git", "-C", workspace, "rev-parse", "origin/"+ref).Output()
	}
	if err != nil || len(bytes.TrimSpace(sha)) == 0 {
		sha, err = exec.Command("git", "-C", workspace, "rev-parse", "origin/master").Output()
	}
	if err != nil {
		sha, err = exec.Command("git", "-C", workspace, "rev-parse", "origin/main").Output()
	}
	if err != nil {
		sha, err = exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
	}
	if err != nil {
		return fmt.Errorf("resolve repository baseline: %w", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(string(sha))), 0o600); err != nil {
		return fmt.Errorf("record repository baseline: %w", err)
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

func ParseNameStatusNumstat(nameStatus, numstat []byte) []PatchFileSummary {
	type rec struct {
		file  PatchFileSummary
		order int
	}
	best := map[string]rec{}
	next := 0
	put := func(path string, update func(*PatchFileSummary)) {
		path = filepath.ToSlash(UnquoteLsFilesPath(path))
		if path == "" {
			return
		}
		item, ok := best[path]
		if !ok {
			item = rec{file: PatchFileSummary{Path: path, Status: "M"}, order: next}
			next++
		}
		update(&item.file)
		item.file.Path = path
		if item.file.Status == "" {
			item.file.Status = "M"
		}
		best[path] = item
	}
	for _, line := range strings.Split(string(nameStatus), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 || parts[0] == "" {
			continue
		}
		letter := string(parts[0][0])
		path := parts[len(parts)-1]
		from := ""
		if (letter == "R" || letter == "C") && len(parts) >= 3 {
			from = filepath.ToSlash(UnquoteLsFilesPath(parts[1]))
			path = parts[2]
		}
		put(path, func(f *PatchFileSummary) {
			f.Status = letter
			f.RenameFrom = from
		})
	}
	for _, line := range strings.Split(string(numstat), "\n") {
		line = strings.TrimRight(line, "\r")
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		path := parts[len(parts)-1]
		put(path, func(f *PatchFileSummary) {
			if parts[0] == "-" || parts[1] == "-" {
				f.Binary = true
				if f.Status == "M" || f.Status == "" {
					f.Status = "B"
				}
				return
			}
			f.Added, _ = strconv.Atoi(parts[0])
			f.Deleted, _ = strconv.Atoi(parts[1])
		})
	}
	out := make([]PatchFileSummary, 0, len(best))
	for _, item := range best {
		out = append(out, item.file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
