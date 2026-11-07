package httpapi

import (
	"archive/zip"
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"
)

func TestSkillFileUploadValidation(t *testing.T) {
	s := newSkillFileServer(t)
	if err := s.PutSkill(domain.SkillDefinition{ID: "cvb-round2-skill", Name: "Round2", ProjectID: "platform"}); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, s)

	req := httptest.NewRequest(http.MethodPost, "/api/skills/cvb-round2-skill/files", bytes.NewReader([]byte("not multipart")))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed multipart = %d", rec.Code)
	}
	rec = requestMultipart(t, h, http.MethodPost, "/api/skills/cvb-round2-skill/files", func(w *multipart.Writer) {})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty upload = %d", rec.Code)
	}
	rec = requestMultipart(t, h, http.MethodPost, "/api/skills/cvb-round2-skill/files", func(w *multipart.Writer) {
		_ = w.WriteField("paths", "one")
		_ = w.WriteField("paths", "two")
		addFilePart(w, "files", "one", []byte("x"))
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("path mismatch = %d", rec.Code)
	}
	rec = requestMultipart(t, h, http.MethodPost, "/api/skills/cvb-round2-skill/files", func(w *multipart.Writer) {
		addFilePartWithPath(w, "../bad", []byte("x"))
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid path = %d", rec.Code)
	}
	rec = requestMultipart(t, h, http.MethodPost, "/api/skills/cvb-round2-skill/files", func(w *multipart.Writer) {
		addFilePartWithPath(w, "folder/same.txt", []byte("one"))
		addFilePartWithPath(w, "folder/same.txt", []byte("two"))
	})
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate path = %d: %s", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/skills/missing/files/a", http.StatusNotFound},
		{http.MethodGet, "/api/skills/cvb-round2-skill/files/../bad", http.StatusBadRequest},
		{http.MethodGet, "/api/skills/cvb-round2-skill/files/missing", http.StatusNotFound},
		{http.MethodPatch, "/api/skills/cvb-round2-skill/files/missing", http.StatusMethodNotAllowed},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestSkillZipEmptyInvalidAndDuplicate(t *testing.T) {
	s := newSkillFileServer(t)
	if err := s.PutSkill(domain.SkillDefinition{ID: "cvb-round2-zip", Name: "Zip", ProjectID: "platform"}); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, s)
	for _, archive := range [][]byte{nil, []byte("invalid zip"), cvBEmptyZipRound2(t)} {
		rec := requestMultipart(t, h, http.MethodPost, "/api/skills/cvb-round2-zip/files", func(w *multipart.Writer) {
			addFilePart(w, "files", "bundle.zip", archive)
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("invalid zip = %d: %s", rec.Code, rec.Body.String())
		}
	}
	archive := zipBytes(t, map[string]string{"a.txt": "a"}, nil)
	rec := requestMultipart(t, h, http.MethodPost, "/api/skills/cvb-round2-zip/files", func(w *multipart.Writer) {
		addFilePart(w, "files", "one.zip", archive)
		addFilePart(w, "files", "two.zip", archive)
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("duplicate zip = %d: %s", rec.Code, rec.Body.String())
	}
	if clean, err := cleanUploadPath(`.\folder\file.txt`); err != nil || clean != "folder/file.txt" {
		t.Fatalf("clean path = %q %v", clean, err)
	}
	if top("one/two") != "one" || top("one") != "" {
		t.Fatal("top helper")
	}
	if _, nested := sharedTopDir(nil); nested {
		t.Fatal("empty shared top dir")
	}
	if _, nested := sharedTopDir([]string{"one", "two"}); nested {
		t.Fatal("flat shared top dir")
	}
	if _, nested := sharedTopDir([]string{"a/one", "b/two"}); nested {
		t.Fatal("mixed shared top dir")
	}
	_ = pathEscapeRFC5987("a b中.txt")
}

func cvBEmptyZipRound2(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
