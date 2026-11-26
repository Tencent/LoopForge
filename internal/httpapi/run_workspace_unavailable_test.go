package httpapi

import (
	"testing"

	"agentflow/internal/domain"
)

// 面板靠这四个字段判断「工作区不可用」并展示只读提示；它们之前由 5 处
// 内联 gin.H 各自复制，抽成 helper 后必须保持完全一致的取值。
func TestWorkspaceUnavailablePayloadKeepsShape(t *testing.T) {
	run := domain.Run{
		ID:     "run-1",
		Status: "succeeded",
		Repositories: []domain.RepositoryContext{{
			Name:       "loopforge",
			CloneURL:   "https://user:secret-token@example.com/loopforge.git",
			BaseBranch: "main",
			WorkBranch: "feat/x",
		}},
	}
	payload := workspaceUnavailablePayload(run, "工作区尚未创建或已被清理")

	if payload["available"] != false {
		t.Fatalf("available = %v, want false", payload["available"])
	}
	if payload["readOnly"] != true {
		t.Fatalf("readOnly = %v, want true", payload["readOnly"])
	}
	if payload["reason"] != "工作区尚未创建或已被清理" {
		t.Fatalf("reason = %v", payload["reason"])
	}
	if payload["runStatus"] != "succeeded" {
		t.Fatalf("runStatus = %v, want succeeded", payload["runStatus"])
	}
	details, ok := payload["repositoryDetails"].([]workspaceRepositoryDetail)
	if !ok || len(details) != 1 {
		t.Fatalf("repositoryDetails = %#v", payload["repositoryDetails"])
	}
	if details[0].Name != "loopforge" || details[0].WorkBranch != "feat/x" || details[0].BaseBranch != "main" {
		t.Fatalf("repository detail = %#v", details[0])
	}
	// 与 workspaceRepositoryDetails 一致：凭据不能出现在面板响应里。
	if details[0].CloneURL != "https://example.com/loopforge.git" {
		t.Fatalf("cloneUrl leaked credentials: %q", details[0].CloneURL)
	}
	if len(payload) != 5 {
		t.Fatalf("unexpected field count %d: %#v", len(payload), payload)
	}
}
