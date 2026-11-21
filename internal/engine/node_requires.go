package engine

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// verdictArtifactNames are the artifact names a verdict requirement reads. The
// verification sidecar publishes its JSON judgement under one of these.
var verdictArtifactNames = []string{"verdict", "verify.json"}

type nodeRequirement struct {
	kind     string // artifact, verdict
	name     string // artifact name (kind=artifact)
	want     string // expected sha256 (kind=artifact) or verdict (kind=verdict)
	diffHash string // expected diff hash (kind=verdict)
}

// parseNodeRequirement parses the two supported precondition forms. Workflow
// validation (run start, and RefreshRunSOP) rejects malformed values; the save
// path does not call this validator, so a typo is reported when a run starts.
func parseNodeRequirement(raw string) (nodeRequirement, error) {
	value := strings.TrimSpace(raw)
	kind, rest, ok := strings.Cut(value, ":")
	if !ok {
		return nodeRequirement{}, fmt.Errorf("expected artifact:<name>@<sha256> or verdict:<PASS|FAIL>@<diffHash>")
	}
	target, hash, ok := strings.Cut(rest, "@")
	target, hash = strings.TrimSpace(target), strings.TrimSpace(hash)
	switch kind {
	case "artifact":
		if target == "" || hash == "" {
			return nodeRequirement{}, fmt.Errorf("artifact requires a name and a sha256")
		}
		return nodeRequirement{kind: kind, name: target, want: hash}, nil
	case "verdict":
		verdict := strings.ToUpper(target)
		if verdict != "PASS" && verdict != "FAIL" || hash == "" {
			return nodeRequirement{}, fmt.Errorf("verdict requires PASS or FAIL and a diff hash")
		}
		return nodeRequirement{kind: kind, want: verdict, diffHash: hash}, nil
	default:
		return nodeRequirement{}, fmt.Errorf("unsupported requirement kind %q", kind)
	}
}

// checkNodeRequires reports the first unmet precondition with the expected and
// actual values, so the failed node explains itself without a log hunt.
func checkNodeRequires(run domain.Run, node domain.WorkflowNode) error {
	for _, raw := range node.Requires {
		requirement, err := parseNodeRequirement(raw)
		if err != nil {
			return fmt.Errorf("节点 %s 的前置条件 %q 无法解析：%w", node.ID, raw, err)
		}
		if err := requirement.unmet(run); err != nil {
			return fmt.Errorf("节点 %s 的前置条件 %q 不满足：%w", node.ID, raw, err)
		}
	}
	return nil
}

func (requirement nodeRequirement) unmet(run domain.Run) error {
	switch requirement.kind {
	case "artifact":
		artifact := latestRunArtifact(run, requirement.name)
		if artifact == nil {
			return fmt.Errorf("run 中没有产物 %q（期望 sha256 %s）", requirement.name, requirement.want)
		}
		if !strings.EqualFold(artifact.SHA256, requirement.want) {
			return fmt.Errorf("产物 %q 的 sha256 为 %s，期望 %s", requirement.name, artifact.SHA256, requirement.want)
		}
		return nil
	case "verdict":
		artifact := latestVerdictArtifact(run)
		if artifact == nil {
			return fmt.Errorf("run 中没有判定产物（%s），期望 verdict=%s diffHash=%s", strings.Join(verdictArtifactNames, "/"), requirement.want, requirement.diffHash)
		}
		var document struct {
			Verdict  string `json:"verdict"`
			DiffHash string `json:"diffHash"`
		}
		if err := json.Unmarshal([]byte(artifact.Content), &document); err != nil {
			return fmt.Errorf("判定产物 %q 不是合法 JSON：%v", artifact.Name, err)
		}
		if !strings.EqualFold(strings.TrimSpace(document.Verdict), requirement.want) {
			return fmt.Errorf("判定产物 %q 的 verdict 为 %q，期望 %q", artifact.Name, document.Verdict, requirement.want)
		}
		if !strings.EqualFold(strings.TrimSpace(document.DiffHash), requirement.diffHash) {
			return fmt.Errorf("判定产物 %q 的 diffHash 为 %q，期望 %q", artifact.Name, document.DiffHash, requirement.diffHash)
		}
		return nil
	default:
		return fmt.Errorf("unsupported requirement kind %q", requirement.kind)
	}
}

func latestVerdictArtifact(run domain.Run) *domain.Artifact {
	var best *domain.Artifact
	for i := range run.Artifacts {
		artifact := &run.Artifacts[i]
		matches := false
		for _, name := range verdictArtifactNames {
			if artifact.Name == name || filepath.Base(artifact.Path) == name {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		if best == nil || artifact.CreatedAt.After(best.CreatedAt) ||
			artifact.CreatedAt.Equal(best.CreatedAt) && artifact.Attempt > best.Attempt {
			best = artifact
		}
	}
	return best
}

// nodeRequirementFailed parks the node as failed without consuming an attempt:
// a deterministic precondition will not change on retry.
func (e *Engine) nodeRequirementFailed(runID string, node domain.WorkflowNode, generation int, err error) {
	e.updateRun(runID, func(r *domain.Run) {
		if r.Generation != generation {
			return
		}
		nr := r.Nodes[node.ID]
		if nr == nil || nr.Status != "pending" {
			return
		}
		nr.Status = "failed"
		nr.FinishedAt = time.Now().UTC()
		nr.Error = err.Error()
		e.event(r, node.ID, "node.failed", err.Error())
	})
}
