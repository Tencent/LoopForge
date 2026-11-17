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
	if node == nil {
		return apperr.Newf(apperr.Conflict, "通知对应的节点 %q 已不存在，请查看最新消息", want.NodeID)
	}
	if node.Status != want.Status || node.Attempt != want.Attempt || node.ReworkCount != want.ReworkCount {
		return apperr.Newf(apperr.Conflict, "通知对应的节点状态已变化：期望 status=%s attempt=%d rework=%d，实际 status=%s attempt=%d rework=%d",
			want.Status, want.Attempt, want.ReworkCount, node.Status, node.Attempt, node.ReworkCount)
	}
	name, sha := strings.TrimSpace(want.ArtifactName), strings.TrimSpace(want.ArtifactSHA256)
	if name == "" && sha == "" {
		return nil
	}
	if name == "" || sha == "" {
		// Half a binding is a caller bug, not an unbound approval: accepting it
		// would let a client believe its approval is artifact-bound when it is not.
		return apperr.New(apperr.Invalid, "expected.artifactName 与 expected.artifactSha256 必须同时提供")
	}
	artifact := latestRunArtifact(run, name)
	if artifact == nil {
		return apperr.Newf(apperr.Conflict, "批准绑定的产物 %q 不在当前 run 中，请查看最新消息", name).
			With("expectedArtifact", name).
			With("expectedSha256", sha)
	}
	if !strings.EqualFold(artifact.SHA256, sha) {
		return apperr.Newf(apperr.Conflict, "批准绑定的产物 %q 内容已变化：期望 sha256 %s，实际 %s", name, sha, artifact.SHA256).
			With("expectedArtifact", name).
			With("expectedSha256", sha).
			With("actualSha256", artifact.SHA256)
	}
	return nil
}

// latestRunArtifact returns the newest artifact with this logical name. Old
// attempts stay in the run for audit, and a decision must bind to the newest
// one — binding an older attempt is exactly the stale approval this check
// exists to reject. Newest means creation time, not attempt number: attempts
// are per node, so comparing them across nodes is meaningless.
func latestRunArtifact(run domain.Run, name string) *domain.Artifact {
	var best *domain.Artifact
	for i := range run.Artifacts {
		artifact := &run.Artifacts[i]
		if artifact.Name != name {
			continue
		}
		if best == nil || artifact.CreatedAt.After(best.CreatedAt) ||
			artifact.CreatedAt.Equal(best.CreatedAt) && artifact.Attempt > best.Attempt {
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
