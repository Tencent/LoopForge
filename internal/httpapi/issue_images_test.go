package httpapi

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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
		Images []repo.IssueImage `json:"images"`
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
	if err := f.s.PutIssueWithCopiedImages(trackerIssue, saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeleteIssue(saved.ID); err != nil {
		t.Fatal(err)
	}
	if images, err := f.s.IssueImages(saved.ID); err != nil || len(images) != 0 {
		t.Fatalf("deleted issue left images: %v %v", images, err)
	}
	trackerImagePath := "/api/issues/" + trackerIssue.ID + "/images/" + imageID
	if rec := cvBRequest(t, f, http.MethodGet, trackerImagePath, nil, true); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), pngData.Bytes()) {
		t.Fatalf("copied image: %d", rec.Code)
	}
}
