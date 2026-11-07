package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func newSkillFileServer(t *testing.T) *repo.Store {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func skillFileHandler(t *testing.T, s *repo.Store) http.Handler {
	t.Helper()
	return New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
}

func requestMultipart(t *testing.T, h http.Handler, method, path string, build func(w *multipart.Writer)) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	build(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func addFilePart(w *multipart.Writer, field, name string, content []byte) {
	part, err := w.CreateFormFile(field, name)
	if err != nil {
		panic(err)
	}
	if _, err := part.Write(content); err != nil {
		panic(err)
	}
}

// addFilePartWithPath mirrors the browser folder-upload protocol: the
// relative path travels in a parallel "paths" value because Go's multipart
// parser keeps only the basename of each file part.
func addFilePartWithPath(w *multipart.Writer, logicalPath string, content []byte) {
	if err := w.WriteField("paths", logicalPath); err != nil {
		panic(err)
	}
	addFilePart(w, "files", "blob", content)
}

func zipBytes(t *testing.T, entries map[string]string, modes map[string]fs.FileMode) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		mode := fs.FileMode(0o644)
		if m, ok := modes[name]; ok {
			mode = m
		}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeSkillResponse(t *testing.T, rec *httptest.ResponseRecorder) domain.SkillDefinition {
	t.Helper()
	var skill domain.SkillDefinition
	if err := json.Unmarshal(rec.Body.Bytes(), &skill); err != nil {
		t.Fatalf("invalid response %s: %v", rec.Body.String(), err)
	}
	return skill
}

func TestSkillFileUploadDownloadDelete(t *testing.T) {
	s := newSkillFileServer(t)
	if err := s.PutSkill(domain.SkillDefinition{ID: "up-skill", Name: "Up", ProjectID: "platform", Content: "# up"}); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, s)

	rec := requestMultipart(t, h, http.MethodPost, "/api/skills/up-skill/files", func(w *multipart.Writer) {
		addFilePartWithPath(w, "assets/logo.png", []byte{0x89, 0x50, 0x4e, 0x47})
		addFilePartWithPath(w, "docs/readme.md", []byte("readme"))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status %d: %s", rec.Code, rec.Body.String())
	}
	skill := decodeSkillResponse(t, rec)
	if len(skill.FileRefs) != 2 {
		t.Fatalf("unexpected manifest: %#v", skill.FileRefs)
	}
	for _, ref := range skill.FileRefs {
		if !strings.Contains(ref.Path, "/") {
			t.Fatalf("directory structure lost: %#v", skill.FileRefs)
		}
	}

	// Download round-trips the exact bytes.
	req := httptest.NewRequest(http.MethodGet, "/api/skills/up-skill/files/assets/logo.png", nil)
	dl := httptest.NewRecorder()
	h.ServeHTTP(dl, req)
	if dl.Code != http.StatusOK || !bytes.Equal(dl.Body.Bytes(), []byte{0x89, 0x50, 0x4e, 0x47}) {
		t.Fatalf("download status %d body %v", dl.Code, dl.Body.Bytes())
	}

	// Delete removes the file and refreshes the manifest.
	del := httptest.NewRequest(http.MethodDelete, "/api/skills/up-skill/files/docs/readme.md", nil)
	dr := httptest.NewRecorder()
	h.ServeHTTP(dr, del)
	if dr.Code != http.StatusOK {
		t.Fatalf("delete status %d: %s", dr.Code, dr.Body.String())
	}
	if skill := decodeSkillResponse(t, dr); len(skill.FileRefs) != 1 || skill.FileRefs[0].Path != "assets/logo.png" {
		t.Fatalf("unexpected manifest after delete: %#v", skill.FileRefs)
	}
}

func TestSkillZipUploadStripsTopDirAndKeepsMode(t *testing.T) {
	s := newSkillFileServer(t)
	if err := s.PutSkill(domain.SkillDefinition{ID: "zip-skill", Name: "Zip", ProjectID: "platform", Content: "# zip"}); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, s)
	archive := zipBytes(t,
		map[string]string{
			"my-skill/scripts/check.sh": "#!/bin/sh\n",
			"my-skill/docs/readme.md":   "docs",
			"__MACOSX/junk":             "noise",
		},
		map[string]fs.FileMode{"my-skill/scripts/check.sh": 0o755},
	)

	rec := requestMultipart(t, h, http.MethodPost, "/api/skills/zip-skill/files", func(w *multipart.Writer) {
		addFilePart(w, "files", "bundle.zip", archive)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status %d: %s", rec.Code, rec.Body.String())
	}
	skill := decodeSkillResponse(t, rec)
	byPath := map[string]domain.SkillFileRef{}
	for _, ref := range skill.FileRefs {
		byPath[ref.Path] = ref
	}
	if len(byPath) != 2 {
		t.Fatalf("expected stripped top dir and no __MACOSX noise: %#v", byPath)
	}
	if ref, ok := byPath["scripts/check.sh"]; !ok || ref.Mode != 0o755 {
		t.Fatalf("exec mode not preserved: %#v", ref)
	}
	if content, ok := s.SkillFileContent("zip-skill", "scripts/check.sh"); !ok || string(content) != "#!/bin/sh\n" {
		t.Fatalf("unexpected stored content: %q", content)
	}
}

func TestSkillZipRejectsTraversalAndSymlinks(t *testing.T) {
	s := newSkillFileServer(t)
	if err := s.PutSkill(domain.SkillDefinition{ID: "evil-skill", Name: "Evil", ProjectID: "platform", Content: "# evil"}); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, s)

	traversal := zipBytes(t, map[string]string{"ok.txt": "fine", "../escape.txt": "evil"}, nil)
	rec := requestMultipart(t, h, http.MethodPost, "/api/skills/evil-skill/files", func(w *multipart.Writer) {
		addFilePart(w, "files", "evil.zip", traversal)
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "无效路径") {
		t.Fatalf("traversal not rejected: %d %s", rec.Code, rec.Body.String())
	}
	if refs := s.SkillFileRefs("evil-skill"); len(refs) != 0 {
		t.Fatalf("rejected zip must not persist anything: %#v", refs)
	}

	symlinkZip := zipBytes(t, map[string]string{"link": "/etc/passwd"}, map[string]fs.FileMode{"link": fs.ModeSymlink | 0o777})
	rec = requestMultipart(t, h, http.MethodPost, "/api/skills/evil-skill/files", func(w *multipart.Writer) {
		addFilePart(w, "files", "link.zip", symlinkZip)
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "非常规文件") {
		t.Fatalf("symlink not rejected: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSkillFileUploadLimits(t *testing.T) {
	s := newSkillFileServer(t)
	if err := s.PutSkill(domain.SkillDefinition{ID: "limit-skill", Name: "Limit", ProjectID: "platform", Content: "# limit"}); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, s)

	rec := requestMultipart(t, h, http.MethodPost, "/api/skills/limit-skill/files", func(w *multipart.Writer) {
		addFilePart(w, "files", "big.bin", bytes.Repeat([]byte("x"), int(domain.SkillFileMaxBytes)+1))
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "1MB") {
		t.Fatalf("per-file limit not enforced: %d %s", rec.Code, rec.Body.String())
	}

	// 501 tiny files exceed the count cap.
	rec = requestMultipart(t, h, http.MethodPost, "/api/skills/limit-skill/files", func(w *multipart.Writer) {
		for i := 0; i < domain.SkillMaxFileCount+1; i++ {
			addFilePart(w, "files", fmt.Sprintf("f%04d.txt", i), []byte("x"))
		}
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "500") {
		t.Fatalf("count limit not enforced: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSkillFileUploadConflictsAndMissingSkill(t *testing.T) {
	s := newSkillFileServer(t)
	if err := s.PutSkill(domain.SkillDefinition{ID: "text-skill", Name: "Text", ProjectID: "platform", Content: "# text", Files: map[string]string{"a.txt": "text"}}); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, s)

	rec := requestMultipart(t, h, http.MethodPost, "/api/skills/text-skill/files", func(w *multipart.Writer) {
		addFilePart(w, "files", "a.txt", []byte("binary"))
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("text collision not rejected: %d %s", rec.Code, rec.Body.String())
	}

	rec = requestMultipart(t, h, http.MethodPost, "/api/skills/ghost/files", func(w *multipart.Writer) {
		addFilePart(w, "files", "a.txt", []byte("x"))
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown skill not rejected: %d", rec.Code)
	}
}

func TestSkillPutPreservesServerFileManifest(t *testing.T) {
	s := newSkillFileServer(t)
	if err := s.PutSkill(domain.SkillDefinition{ID: "keep-skill", Name: "Keep", ProjectID: "platform", Content: "# keep"}); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, s)

	rec := requestMultipart(t, h, http.MethodPost, "/api/skills/keep-skill/files", func(w *multipart.Writer) {
		// A folder pick of "img/" with one file: the shared top-level
		// directory is stripped, so the stored path is logo.png.
		addFilePartWithPath(w, "img/logo.png", []byte("png"))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status %d: %s", rec.Code, rec.Body.String())
	}
	if stored, _ := s.Skill("keep-skill"); len(stored.FileRefs) != 1 || stored.FileRefs[0].Path != "logo.png" {
		t.Fatalf("unexpected stored path: %#v", stored.FileRefs)
	}

	// A plain PUT must not wipe the manifest even though its JSON response
	// is only {"ok": true}; the stored payload keeps the server manifest.
	body := domain.SkillDefinition{ID: "keep-skill", Name: "Keep v2", ProjectID: "platform", Content: "# keep2"}
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/skills", body, http.StatusOK)
	if stored, _ := s.Skill("keep-skill"); len(stored.FileRefs) != 1 || stored.FileRefs[0].Path != "logo.png" {
		t.Fatalf("stored manifest lost: %#v", stored.FileRefs)
	}
}

func TestSkillFileUploadReplacesPreviousBatch(t *testing.T) {
	s := newSkillFileServer(t)
	if err := s.PutSkill(domain.SkillDefinition{ID: "replace-skill", Name: "Replace", ProjectID: "platform", Content: "# replace"}); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, s)

	rec := requestMultipart(t, h, http.MethodPost, "/api/skills/replace-skill/files", func(w *multipart.Writer) {
		addFilePartWithPath(w, "a/keep.txt", []byte("first"))
		addFilePartWithPath(w, "a/lose.txt", []byte("first"))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("first upload status %d: %s", rec.Code, rec.Body.String())
	}

	// Second upload replaces the first batch entirely (shared top-level
	// directory "b/" is stripped, so the stored path is "only.txt").
	rec = requestMultipart(t, h, http.MethodPost, "/api/skills/replace-skill/files", func(w *multipart.Writer) {
		addFilePartWithPath(w, "b/only.txt", []byte("second"))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("second upload status %d: %s", rec.Code, rec.Body.String())
	}
	refs := s.SkillFileRefs("replace-skill")
	if len(refs) != 1 || refs[0].Path != "only.txt" {
		t.Fatalf("replace did not wipe previous files: %#v", refs)
	}
}

func TestSkillMdUploadReplacesEntireSet(t *testing.T) {
	s := newSkillFileServer(t)
	if err := s.PutSkill(domain.SkillDefinition{ID: "md-skill", Name: "Md", ProjectID: "platform", Content: "# md"}); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, s)

	rec := requestMultipart(t, h, http.MethodPost, "/api/skills/md-skill/files", func(w *multipart.Writer) {
		addFilePartWithPath(w, "SKILL.md", []byte("# old\n"))
		addFilePartWithPath(w, "scripts/run.sh", []byte("#!/bin/sh\n"))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("seed upload status %d: %s", rec.Code, rec.Body.String())
	}

	rec = requestMultipart(t, h, http.MethodPost, "/api/skills/md-skill/files", func(w *multipart.Writer) {
		addFilePartWithPath(w, "SKILL.md", []byte("# only\n"))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("skill.md upload status %d: %s", rec.Code, rec.Body.String())
	}
	skill := decodeSkillResponse(t, rec)
	if len(skill.FileRefs) != 1 || skill.FileRefs[0].Path != "SKILL.md" {
		t.Fatalf("SKILL.md upload must leave only SKILL.md: %#v", skill.FileRefs)
	}
	if content, ok := s.SkillFileContent("md-skill", "SKILL.md"); !ok || string(content) != "# only\n" {
		t.Fatalf("SKILL.md not replaced: %q %v", content, ok)
	}
	if _, ok := s.SkillFileContent("md-skill", "scripts/run.sh"); ok {
		t.Fatal("sibling scripts/run.sh should have been wiped")
	}
}
