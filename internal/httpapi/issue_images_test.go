package httpapi

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestIssueImagesCreateReadAndDelete(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	f := cvBNewHTTPFixture(t)
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	description := "# Checkout\n\n- Keep **existing** orders"
	create := func(files ...[]byte) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		payload, err := json.Marshal(domain.Issue{Title: "Checkout", ProjectID: "platform", Description: description})
		if err != nil {
			t.Fatal(err)
		}
		if err := form.WriteField("issue", string(payload)); err != nil {
			t.Fatal(err)
		}
		for _, data := range files {
			part, err := form.CreateFormFile("images", "checkout.png")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/issues", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, authedRequest(t, req, f.user))
		return rec
	}
	before := len(f.s.Issues())
	if rec := create(pngData.Bytes(), []byte("<svg></svg>")); rec.Code != http.StatusBadRequest || len(f.s.Issues()) != before {
		t.Fatalf("invalid images must reject the whole issue: %d %s", rec.Code, rec.Body.String())
	}
	rec := create(pngData.Bytes())
	var saved domain.Issue
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &saved) != nil || saved.ID == "" || saved.Description != description {
		t.Fatalf("create issue: %d %s", rec.Code, rec.Body.String())
	}
	path := "/api/issues/" + saved.ID
	rec = cvBRequest(t, f, http.MethodGet, path, nil, true)
	var detail struct {
		Images []repo.IssueAttachment `json:"images"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &detail) != nil || len(detail.Images) != 1 || detail.Images[0].Name != "checkout.png" {
		t.Fatalf("issue detail: %d %s", rec.Code, rec.Body.String())
	}
	imageID := detail.Images[0].ID
	imagePath := path + "/images/" + imageID
	if rec := cvBRequest(t, f, http.MethodGet, imagePath, nil, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated image: %d", rec.Code)
	}
	rec = cvBRequest(t, f, http.MethodGet, imagePath, nil, true)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), pngData.Bytes()) {
		t.Fatalf("image response: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	trackerIssue := saved
	trackerIssue.ID = "tapd-test-image"
	if err := f.s.PutIssueWithCopiedAttachments(trackerIssue, saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeleteIssue(saved.ID); err != nil {
		t.Fatal(err)
	}
	if images, err := f.s.IssueAttachments(saved.ID); err != nil || len(images) != 0 {
		t.Fatalf("deleted issue left images: %v %v", images, err)
	}
	trackerImagePath := "/api/issues/" + trackerIssue.ID + "/images/" + imageID
	if rec := cvBRequest(t, f, http.MethodGet, trackerImagePath, nil, true); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), pngData.Bytes()) {
		t.Fatalf("copied image: %d", rec.Code)
	}
}

func TestIssueAttachmentsDownloadAndConversationInputs(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	f := cvBNewHTTPFixture(t)
	project, _ := f.s.Project("platform")
	project.OwnerID = f.user.ID
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	imageData := conversationTestPNG(t)
	document := []byte("# Checkout\nContinue from 2 active orders.\n")
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("issue", `{"title":"Attached requirements","projectId":"platform","sopMode":"none"}`); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		name string
		data []byte
	}{{"spec.md", document}, {"checkout.png", imageData}} {
		part, err := form.CreateFormFile("attachments", input.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(input.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/issues", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, authedRequest(t, req, f.user))
	var issue domain.Issue
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &issue) != nil || issue.ID == "" {
		t.Fatalf("create with files only: %d %s", rec.Code, rec.Body)
	}
	path := "/api/issues/" + issue.ID
	rec = cvBRequest(t, f, http.MethodGet, path, nil, true)
	var detail struct {
		Attachments []repo.IssueAttachment
		Images      []repo.IssueAttachment
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &detail) != nil || len(detail.Attachments) != 2 || len(detail.Images) != 1 {
		t.Fatalf("read attachment metadata: %d %s", rec.Code, rec.Body)
	}
	docID := detail.Attachments[0].ID
	rec = cvBRequest(t, f, http.MethodGet, path+"/attachments/"+docID, nil, true)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), document) ||
		rec.Header().Get("Content-Disposition") != "attachment; filename=spec.md" ||
		rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("download: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	if rec := cvBRequest(t, f, http.MethodGet, path+"/images/"+docID, nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("document available on the inline image route: %d", rec.Code)
	}
	other := domain.User{ID: 442, LoginName: "outside"}
	if err := f.s.PutUser(other); err != nil {
		t.Fatal(err)
	}
	// A signed-in non-member can read the requirement, so it can read the
	// files that belong to it as well; only writes are limited to members.
	foreign := httptest.NewRecorder()
	f.h.ServeHTTP(foreign, authedRequest(t, httptest.NewRequest(http.MethodGet, path, nil), other))
	var foreignDetail struct{ Attachments, Images []repo.IssueAttachment }
	if foreign.Code != http.StatusOK || json.Unmarshal(foreign.Body.Bytes(), &foreignDetail) != nil ||
		len(foreignDetail.Attachments) != 2 || len(foreignDetail.Images) != 1 {
		t.Fatalf("detail outside project: %d %s", foreign.Code, foreign.Body)
	}
	for _, id := range []string{"/attachments/" + docID, "/images/" + foreignDetail.Images[0].ID} {
		foreign = httptest.NewRecorder()
		f.h.ServeHTTP(foreign, authedRequest(t, httptest.NewRequest(http.MethodGet, path+id, nil), other))
		if foreign.Code != http.StatusOK || len(foreign.Body.Bytes()) == 0 {
			t.Fatalf("read %s outside project: %d", id, foreign.Code)
		}
	}
	anonymous := httptest.NewRecorder()
	f.h.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, path+"/attachments/"+docID, nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous download: %d", anonymous.Code)
	}

	files, err := readConversationIssueAttachments(f.s.IssuesRepo, []domain.Issue{issue})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := writeConversationIssueAttachments(root, f.s.IssuesRepo, files); err != nil {
		t.Fatal(err)
	}
	turn := assistantRequest{issueAttachments: files}
	note := conversationAttachmentNote(root, turn, assistantEngineCodeBuddy)
	paths := conversationTurnImagePaths(root, turn)
	if !strings.Contains(note, "spec.md") || !strings.Contains(note, "checkout.png") || !conversationHasImageAttachments(turn) || len(paths) != 1 {
		t.Fatalf("conversation inputs: %v, %s", paths, note)
	}
	input, err := assistantStreamImageInput("Describe the screenshot", paths)
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Message struct {
			Content []struct {
				Source struct {
					Data []byte
				}
			}
		}
	}
	if err := json.Unmarshal(input.Bytes(), &frame); err != nil || len(frame.Message.Content) != 2 ||
		!bytes.Equal(frame.Message.Content[1].Source.Data, imageData) {
		t.Fatalf("native image input did not contain the original PNG: %v", err)
	}
}
