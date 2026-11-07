package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func openFeedbackStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestCreatePageFeedbackStoresSubmitterAndImages(t *testing.T) {
	s := openFeedbackStore(t)
	fb := domain.PageFeedback{
		Title: "按钮错位", Description: "按钮错位\n第二行", URL: "https://x.test/#/runs", Route: "#/runs", View: "运行记录",
		Context:   domain.PageFeedbackContext{Viewport: domain.PageFeedbackSize{Width: 1280, Height: 800}, Theme: "dark"},
		Element:   &domain.PageFeedbackElement{Selector: "#retry", Tag: "button", Text: "重试", InFrame: true},
		Submitter: domain.PageFeedbackUser{UserID: 7, LoginName: "alice", ChineseName: "爱丽丝"},
	}
	stored, err := s.CreatePageFeedback(fb, []domain.PageFeedbackImage{
		{FileName: "a.png", MimeType: "image/png", Size: 3, SHA256: "aa", Data: []byte{1, 2, 3}},
		{FileName: "b.gif", MimeType: "image/gif", Size: 2, SHA256: "bb", Data: []byte{4, 5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID == "" || len(stored.Images) != 2 || stored.Images[0].Data != nil {
		t.Fatalf("stored = %+v", stored)
	}

	got, found, err := s.PageFeedback(stored.ID)
	if err != nil || !found {
		t.Fatalf("PageFeedback found=%v err=%v", found, err)
	}
	if got.Submitter != fb.Submitter {
		t.Fatalf("submitter = %+v", got.Submitter)
	}
	if got.Element == nil || got.Element.Selector != "#retry" || !got.Element.InFrame || got.Context.Theme != "dark" {
		t.Fatalf("context/element = %+v %+v", got.Context, got.Element)
	}
	if len(got.Images) != 2 || got.Images[0].FileName != "a.png" || got.Images[1].Position != 1 || got.Images[0].Data != nil {
		t.Fatalf("images = %+v", got.Images)
	}

	img, found, err := s.PageFeedbackImage(stored.ID, stored.Images[1].ID)
	if err != nil || !found || string(img.Data) != string([]byte{4, 5}) || img.MimeType != "image/gif" {
		t.Fatalf("image = %+v found=%v err=%v", img, found, err)
	}
	if _, found, _ := s.PageFeedbackImage("fb-other", stored.Images[1].ID); found {
		t.Fatal("an image must only be readable through its own feedback")
	}
	if _, found, _ := s.PageFeedback("fb-missing"); found {
		t.Fatal("missing feedback should not be found")
	}
}

func TestCreatePageFeedbackRollsBackWhenAnImageFails(t *testing.T) {
	s := openFeedbackStore(t)
	_, err := s.CreatePageFeedback(domain.PageFeedback{Title: "t", Submitter: domain.PageFeedbackUser{UserID: 1, LoginName: "a", ChineseName: "a"}},
		[]domain.PageFeedbackImage{
			{ID: "fbi-dup", MimeType: "image/png", Data: []byte{1}},
			{ID: "fbi-dup", MimeType: "image/png", Data: []byte{2}},
		})
	if err == nil {
		t.Fatal("duplicate image id should fail the insert")
	}
	items, total, err := s.PageFeedbacks(20, 0)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("feedback rows survived a failed transaction: total=%d items=%d err=%v", total, len(items), err)
	}
	var images int64
	s.DB.Model(&pageFeedbackImageRow{}).Count(&images)
	if images != 0 {
		t.Fatalf("image rows survived a failed transaction: %d", images)
	}
}

func TestPageFeedbacksPagesNewestFirstWithoutImageData(t *testing.T) {
	s := openFeedbackStore(t)
	base := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for i, title := range []string{"first", "second", "third"} {
		_, err := s.CreatePageFeedback(domain.PageFeedback{
			Title: title, CreatedAt: base.Add(time.Duration(i) * time.Minute),
			Submitter: domain.PageFeedbackUser{UserID: int64(i + 1), LoginName: "u", ChineseName: "u"},
		}, []domain.PageFeedbackImage{{MimeType: "image/png", Size: 1, Data: []byte{9}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, total, err := s.PageFeedbacks(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(page) != 2 || page[0].Title != "third" || page[1].Title != "second" {
		t.Fatalf("page 1 = %+v total=%d", page, total)
	}
	for _, item := range page {
		if len(item.Images) != 1 || item.Images[0].Data != nil || item.Images[0].Size != 1 {
			t.Fatalf("listing must carry image metadata only: %+v", item.Images)
		}
	}
	rest, total, err := s.PageFeedbacks(2, 2)
	if err != nil || total != 3 || len(rest) != 1 || rest[0].Title != "first" {
		t.Fatalf("page 2 = %+v total=%d err=%v", rest, total, err)
	}
}
