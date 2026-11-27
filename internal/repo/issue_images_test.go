package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestCreateIssueWithImagesRollsBackOnImageWriteFailure(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	issue := domain.Issue{ID: "issue-images-rollback", ProjectID: "platform", Title: "With images"}
	image := IssueAttachment{ID: "duplicate", Name: "image.png", MediaType: "image/png", Data: []byte{1}}
	if err := store.CreateIssueWithAttachments(issue, []IssueAttachment{image, image}); err == nil {
		t.Fatal("duplicate image ID should fail the transaction")
	}
	if _, ok := store.Issue(issue.ID); ok {
		t.Fatal("issue persisted after a failed image write")
	}
	if images, err := store.IssueAttachments(issue.ID); err != nil || len(images) != 0 {
		t.Fatalf("image persisted after rollback: %v %v", images, err)
	}
}

func TestIssueAttachmentInputPathHasNoComma(t *testing.T) {
	// Codex splits --image values on commas; the staged copy must stay one path
	// while the original name is kept for display and downloads.
	file := IssueAttachment{ID: "a1", Name: "截图,第2版.png"}
	if got, want := file.InputPath(), ".agentflow/inputs/requirement-attachments/a1/截图_第2版.png"; got != want {
		t.Fatalf("InputPath() = %q, want %q", got, want)
	}
	if got := IssueAttachmentName(file.Name); got != "截图,第2版.png" {
		t.Fatalf("display name changed: %q", got)
	}
}
