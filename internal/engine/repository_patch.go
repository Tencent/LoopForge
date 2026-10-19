package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// collectRepositoryPatch is the filename inventory of workspace changes
// handed to downstream nodes; see gitwork.PatchInventory.
func collectRepositoryPatch(workspace string) ([]byte, error) {
	return gitwork.PatchInventory(workspace)
}

type patchFileSummary = gitwork.PatchFileSummary

func repositoryPatchApplyBody(artifact domain.Artifact) string {
	if looksLikeGitDiff(artifact.Content) {
		return artifact.Content
	}
	return ""
}

func repositoryPatchSummary(artifact domain.Artifact) string {
	if body := strings.TrimSpace(artifact.Content); body != "" && !looksLikeGitDiff(body) {
		return body
	}
	if raw := repositoryPatchApplyBody(artifact); raw != "" {
		return summarizeRepositoryPatch([]byte(raw))
	}
	return strings.TrimSpace(artifact.Content)
}

func looksLikeGitDiff(patch string) bool {
	s := strings.TrimLeft(patch, "\ufeff \t\r\n")
	return strings.HasPrefix(s, "diff --git ") || strings.HasPrefix(s, "--- ")
}

func summarizeRepositoryPatch(diff []byte) string {
	return gitwork.RenderPatchInventory(parseUnifiedDiffFiles(diff))
}

func parseUnifiedDiffFiles(diff []byte) []patchFileSummary {
	var files []patchFileSummary
	var cur *patchFileSummary
	flush := func() {
		if cur == nil || cur.Path == "" {
			cur = nil
			return
		}
		if cur.Status == "" {
			cur.Status = "M"
		}
		files = append(files, *cur)
		cur = nil
	}
	for _, line := range strings.Split(string(diff), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			src, dst := splitGitDiffHeader(line)
			cur = &patchFileSummary{Path: dst, Status: "M"}
			if dst == "" {
				cur.Path = src
			}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "new file mode"):
			cur.Status = "A"
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "D"
		case strings.HasPrefix(line, "rename from "):
			cur.Status = "R"
			cur.RenameFrom = strings.TrimSpace(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			cur.Status = "R"
			cur.Path = strings.TrimSpace(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "GIT binary patch") || strings.HasPrefix(line, "Binary files "):
			cur.Binary = true
			cur.Status = "B"
		case strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- "):
			continue
		case !cur.Binary && strings.HasPrefix(line, "+"):
			cur.Added++
		case !cur.Binary && strings.HasPrefix(line, "-"):
			cur.Deleted++
		}
	}
	flush()
	return files
}

func splitGitDiffHeader(line string) (src, dst string) {
	rest := strings.TrimPrefix(strings.TrimSpace(line), "diff --git ")
	a := strings.Index(rest, "a/")
	b := strings.LastIndex(rest, " b/")
	if a < 0 || b <= a {
		return "", ""
	}
	src = strings.Trim(rest[a+2:b], `"`)
	dst = strings.Trim(strings.TrimSpace(rest[b+3:]), `"`)
	return src, dst
}

func applyRepositoryPatch(workspace, patch string) error {
	if strings.TrimSpace(patch) == "" || !looksLikeGitDiff(patch) {
		return nil
	}
	tmp, err := os.CreateTemp(workspace, "agentflow-patch-*.diff")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(patch); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", workspace, "apply", "--whitespace=nowarn", tmp.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
