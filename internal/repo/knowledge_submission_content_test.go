package repo

import (
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// 审核正文属于内容面：控制面的 knowledge_submissions.payload 只能留元数据和
// 指纹。读取单条时再从内容面 staging 表取正文；列表不返回正文。
func TestKnowledgeSubmissionBodyLivesOnContentPlane(t *testing.T) {
	s := builtinStore(t)
	body := "# 审核正文\n\nSTAGED-BODY-SECRET 不能写进控制面的 payload。"
	item := domain.KnowledgeSubmission{
		ID: "kbs-staged", ProjectID: "proj-stage", Source: "src-stage", Path: "docs/staged.md",
		Title: "待审文档", Content: body, Status: domain.KnowledgeSubmissionPending,
		Fingerprint: "4f2a8c6a0a1f4e8c8a7b6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a",
	}
	if err := s.PutKnowledgeSubmission(item); err != nil {
		t.Fatal(err)
	}

	var payload []byte
	if err := s.DB.Raw("SELECT payload FROM knowledge_submissions WHERE id = ?", item.ID).Row().Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "STAGED-BODY-SECRET") {
		t.Fatalf("控制面 payload 不应包含正文：%s", payload)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload 不是 JSON：%v (%s)", err, payload)
	}
	if _, ok := decoded["content"]; ok {
		t.Fatalf("payload 仍带 content 字段：%s", payload)
	}
	if decoded["fingerprint"] != item.Fingerprint {
		t.Fatalf("payload 应保留指纹：%s", payload)
	}

	var staged int64
	if err := s.DB.Model(&knowledgeSubmissionContentRow{}).Where("submission_id = ?", item.ID).Count(&staged).Error; err != nil {
		t.Fatal(err)
	}
	if staged != 1 {
		t.Fatalf("内容面 staging 行数 = %d, want 1", staged)
	}

	got, ok := s.KnowledgeSubmission(item.ID)
	if !ok || got.Content != body {
		t.Fatalf("单条读取应带回内容面正文：%+v ok=%v", got, ok)
	}
	list := s.KnowledgeSubmissions("proj-stage", "", 10)
	if len(list) != 1 || list[0].Content != "" {
		t.Fatalf("列表不应带正文：%+v", list)
	}

	// 审核状态变化时重新保存也必须保留 staging 正文，而不是把空正文覆盖进去。
	got.Status = domain.KnowledgeSubmissionRejected
	if err := s.PutKnowledgeSubmission(got); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := s.KnowledgeSubmission(item.ID)
	if reloaded.Content != body {
		t.Fatalf("重新保存后正文丢失：%q", reloaded.Content)
	}

	if err := s.DeleteKnowledgeSubmission(item.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&knowledgeSubmissionContentRow{}).Where("submission_id = ?", item.ID).Count(&staged).Error; err != nil {
		t.Fatal(err)
	}
	if staged != 0 {
		t.Fatalf("删除审核条目后 staging 仍剩 %d 行", staged)
	}
}

// 内容面读不到正文时不能把空字符串当成正文：审核条目要带缺失标记，发布
// 路径据此拒绝覆盖远端文档。retract 条目本来就没有正文，不算缺失。
func TestKnowledgeSubmissionMarksMissingContent(t *testing.T) {
	s := builtinStore(t)
	item := domain.KnowledgeSubmission{
		ID: "kbs-missing", ProjectID: "proj-missing", Source: "src-missing", Path: "docs/missing.md",
		Title: "待审文档", Content: "# 原文\n\nBODY-MUST-NOT-DISAPPEAR", Status: domain.KnowledgeSubmissionPending,
	}
	if err := s.PutKnowledgeSubmission(item); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Where("submission_id = ?", item.ID).Delete(&knowledgeSubmissionContentRow{}).Error; err != nil {
		t.Fatal(err)
	}
	got, ok := s.KnowledgeSubmission(item.ID)
	if !ok || !got.ContentMissing || got.Content != "" {
		t.Fatalf("missing body must be marked: %+v ok=%v", got, ok)
	}
	if err := s.PutKnowledgeSubmission(domain.KnowledgeSubmission{
		ID: "kbs-retract-missing", ProjectID: "proj-missing", Source: "src-missing", Path: "docs/missing.md",
		Action: domain.KnowledgeSubmissionActionRetract, Status: domain.KnowledgeSubmissionPending,
	}); err != nil {
		t.Fatal(err)
	}
	retract, ok := s.KnowledgeSubmission("kbs-retract-missing")
	if !ok || retract.ContentMissing {
		t.Fatalf("retract item has no body by design: %+v ok=%v", retract, ok)
	}
}
