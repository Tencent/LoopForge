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
	image := IssueImage{ID: "duplicate", Name: "image.png", MediaType: "image/png", Data: []byte{1}}
	if err := store.CreateIssueWithImages(issue, []IssueImage{image, image}); err == nil {
		t.Fatal("duplicate image ID should fail the transaction")
	}
	if _, ok := store.Issue(issue.ID); ok {
		t.Fatal("issue persisted after a failed image write")
	}
	if images, err := store.IssueImages(issue.ID); err != nil || len(images) != 0 {
		t.Fatalf("image persisted after rollback: %v %v", images, err)
	}
}
