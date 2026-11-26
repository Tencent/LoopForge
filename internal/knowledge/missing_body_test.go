package knowledge

import (
	"context"
	"strings"
	"testing"
)

// A review body that disappeared from the content plane must surface as an
// error and must never be published as an empty (title-only) hosted document.
func TestMissingReviewBodyBlocksReadEditAndPublish(t *testing.T) {
	svc, store := newManagedTestService(t)
	item, err := svc.Submit(SubmissionInput{
		ProjectID: "proj-missing-body",
		Source:    "src-missing-body",
		Path:      "docs/guide.md",
		Title:     "指南",
		Content:   "# 指南\n\n这是不能丢失的原文。",
	}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Exec("DELETE FROM knowledge_submission_contents WHERE submission_id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}

	got, err := svc.Submission(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ContentMissing {
		t.Fatalf("missing body was not marked: %+v", got)
	}
	if _, err := svc.SubmissionForRead(item.ID); err == nil || !strings.Contains(err.Error(), "缺失") {
		t.Fatalf("detail read must fail on missing body: %v", err)
	}
	if _, err := svc.Edit(item.ID, SubmissionEdit{Title: "新标题"}); err == nil || !strings.Contains(err.Error(), "缺失") {
		t.Fatalf("edit must not save an empty body: %v", err)
	}
	if _, err := svc.Publish(context.Background(), item.ID, "tester"); err == nil || !strings.Contains(err.Error(), "缺失") {
		t.Fatalf("publish must not write an empty body: %v", err)
	}

	// A caller that supplies a fresh body can explicitly recover the item.
	recovered, err := svc.Edit(item.ID, SubmissionEdit{Title: "新标题", Content: "# 新指南\n\n恢复后的正文。"})
	if err != nil || recovered.ContentMissing || !strings.Contains(recovered.Content, "恢复后的正文") {
		t.Fatalf("explicit replacement should recover: %+v %v", recovered, err)
	}
}
