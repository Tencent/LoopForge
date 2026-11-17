package engine

import (
	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"fmt"
	"strings"
)

// NodeExpectation binds chat-card actions to the exact attempt displayed. The
// existing engine state lock and persistence conflict checks still apply.
// ArtifactName/ArtifactSHA256 additionally bind a human gate decision to the
// artifact the operator reviewed: the run must still carry that artifact with
// that content hash, or the decision is rejected as stale.
type NodeExpectation struct {
	NodeID, Status       string
	Attempt, ReworkCount int
	ArtifactName         string
	ArtifactSHA256       string
}

func checkNodeExpectation(run domain.Run, expected []NodeExpectation) error {
	if len(expected) == 0 {
		return nil
	}
	want := expected[0]
	node := run.Nodes[want.NodeID]
	if node == nil || node.Status != want.Status || node.Attempt != want.Attempt || node.ReworkCount != want.ReworkCount {
		return apperr.New(apperr.Conflict, "通知对应的节点状态已变化，请查看最新消息")
	}
	if strings.TrimSpace(want.ArtifactName) == "" {
		return nil
	}
	artifact := latestRunArtifact(run, want.ArtifactName)
	if artifact == nil {
		return apperr.Newf(apperr.Conflict, "批准绑定的产物 %q 不在当前 run 中，请查看最新消息", want.ArtifactName).
			With("expectedArtifact", want.ArtifactName).
			With("expectedSha256", want.ArtifactSHA256)
	}
	if want.ArtifactSHA256 != "" && !strings.EqualFold(artifact.SHA256, want.ArtifactSHA256) {
		return apperr.Newf(apperr.Conflict, "批准绑定的产物 %q 内容已变化：期望 sha256 %s，实际 %s", want.ArtifactName, want.ArtifactSHA256, artifact.SHA256).
			With("expectedArtifact", want.ArtifactName).
			With("expectedSha256", want.ArtifactSHA256).
			With("actualSha256", artifact.SHA256)
	}
	return nil
}

// latestRunArtifact returns the newest artifact with this logical name. Old
// attempts stay in the run for audit, and a decision must bind to the newest
// one — binding an older attempt is exactly the stale approval this check
// exists to reject.
func latestRunArtifact(run domain.Run, name string) *domain.Artifact {
	var best *domain.Artifact
	for i := range run.Artifacts {
		artifact := &run.Artifacts[i]
		if artifact.Name != name {
			continue
		}
		if best == nil || artifact.Attempt > best.Attempt || artifact.Attempt == best.Attempt && artifact.CreatedAt.After(best.CreatedAt) {
			best = artifact
		}
	}
	return best
}

// auditActor names the operator in control events; an anonymous request is
// still recorded, so the audit trail never reads as if nobody made the call.
func auditActor(actor string) string {
	if trimmed := strings.TrimSpace(actor); trimmed != "" {
		return trimmed
	}
	return "未记录操作者"
}

func describeExpectation(expected []NodeExpectation) string {
	if len(expected) == 0 {
		return ""
	}
	want := expected[0]
	parts := []string{fmt.Sprintf("status=%s attempt=%d rework=%d", want.Status, want.Attempt, want.ReworkCount)}
	if want.ArtifactName != "" {
		parts = append(parts, fmt.Sprintf("artifact=%s sha256=%s", want.ArtifactName, want.ArtifactSHA256))
	}
	return strings.Join(parts, " ")
}
