package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// fakeSkillhub mimics the SkillHub Open API. Downloads and single files
// redirect to a separate "COS" host like the real service.
type fakeSkillhub struct {
	t        *testing.T
	api, cos *httptest.Server

	mu         sync.Mutex
	latest     map[string]string            // ns/slug -> latest version
	packages   map[string][]byte            // ns/slug@version -> zip
	files      map[string]map[string][]byte // ns/slug@version -> path -> content
	failStatus map[string]int               // request path -> forced status
	hits       map[string]int
	queries    map[string]url.Values
	headers    map[string]http.Header
	cosHeaders []http.Header
	noEval     bool
}

func newFakeSkillhub(t *testing.T) *fakeSkillhub {
	f := &fakeSkillhub{
		t: t, latest: map[string]string{}, packages: map[string][]byte{}, files: map[string]map[string][]byte{},
		failStatus: map[string]int{}, hits: map[string]int{}, queries: map[string]url.Values{}, headers: map[string]http.Header{},
	}
	f.cos = httptest.NewServer(http.HandlerFunc(f.serveCOS))
	f.api = httptest.NewServer(http.HandlerFunc(f.serveAPI))
	t.Cleanup(func() { f.api.Close(); f.cos.Close() })
	return f
}

func (f *fakeSkillhub) publish(namespace, slug, version string, entries map[string]string) {
	key := namespace + "/" + slug
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latest[key] = version
	f.packages[key+"@"+version] = zipBytes(f.t, entries, nil)
	files := map[string][]byte{}
	for path, content := range entries {
		files[path] = []byte(content)
	}
	f.files[key+"@"+version] = files
}

func (f *fakeSkillhub) hitCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *fakeSkillhub) serveAPI(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits[r.URL.Path]++
	f.queries[r.URL.Path] = r.URL.Query()
	f.headers[r.URL.Path] = r.Header.Clone()
	status := f.failStatus[r.URL.Path]
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"forced"}`))
		return
	}
	q := r.URL.Query()
	ns := q.Get("namespace")
	if ns == "" {
		ns = "team"
	}
	writeJSON := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/api/skills":
		writeJSON(map[string]any{"code": 0, "data": map[string]any{"total": 1234, "skills": []map[string]any{{
			"slug": "pdf-tools", "name": "PDF 处理", "description": "Read PDFs", "description_zh": "读取 PDF",
			"ownerName": "root", "category": "office-efficiency", "version": "1.3.0", "downloads": 98000, "stars": 412,
			"labels": map[string]any{"requires_api_key": "true"}, "namespace": map[string]any{"handle": "team", "displayName": "PDF Team"},
		}, {
			"slug": "plain", "name": "Plain", "description": "English only", "version": "0.1.0", "labels": map[string]any{"requires_api_key": false},
			"namespace": map[string]any{"handle": "solo"},
		}}}})
	case r.URL.Path == "/api/v1/categories":
		writeJSON(map[string]any{"count": 2, "items": []map[string]any{{"key": "office-efficiency", "name": "办公效率", "active": true}, {"key": "gone", "name": "下线", "active": false}}})
	case r.URL.Path == "/api/v1/download":
		key := ns + "/" + q.Get("slug")
		version := q.Get("version")
		f.mu.Lock()
		if version == "" {
			version = f.latest[key]
		}
		_, ok := f.packages[key+"@"+version]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Version not found"}`))
			return
		}
		http.Redirect(w, r, f.cos.URL+"/zip/"+url.PathEscape(key+"@"+version)+"?q-signature=secret", http.StatusFound)
	case strings.HasPrefix(r.URL.Path, "/api/v1/skills/"):
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/skills/")
		slug, suffix, _ := strings.Cut(rest, "/")
		key := ns + "/" + slug
		f.mu.Lock()
		latest, known := f.latest[key]
		f.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Skill not found"}`))
			return
		}
		version := q.Get("version")
		if version == "" {
			version = latest
		}
		switch suffix {
		case "":
			writeJSON(map[string]any{
				"namespace": map[string]any{"handle": ns, "displayName": ns}, "owner": map[string]any{"displayName": "Owner " + ns, "handle": ns},
				"latestVersion": map[string]any{"version": latest, "changelog": "latest changes", "createdAt": 1700000000000},
				"skill": map[string]any{"slug": slug, "displayName": "Display " + slug, "summary": "summary en", "summary_zh": "中文简介",
					"category": "office-efficiency", "stats": map[string]any{"downloads": 10, "stars": 2, "versions": 2}},
			})
		case "versions":
			writeJSON(map[string]any{"versions": []map[string]any{
				{"version": latest, "changelog": "new", "createdAt": 2, "securityReports": map[string]any{
					"keen":  map[string]any{"status": "benign", "statusText": "安全，无风险", "reportUrl": "https://report.example/1"},
					"other": map[string]any{"status": "suspicious", "statusText": "可疑", "reportUrl": "javascript:alert(1)"},
				}},
				{"version": "1.0.0", "changelog": "Initial", "createdAt": 1},
			}})
		case "files":
			f.mu.Lock()
			entries := f.files[key+"@"+version]
			f.mu.Unlock()
			list := []map[string]any{}
			for path, content := range entries {
				list = append(list, map[string]any{"path": path, "size": len(content), "sha256": "x"})
			}
			writeJSON(map[string]any{"count": len(list), "files": list})
		case "file":
			path := q.Get("path")
			if path == "big.bin" {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_, _ = w.Write([]byte(`{"error":"File exceeds 1MB"}`))
				return
			}
			http.Redirect(w, r, f.cos.URL+"/file/"+url.PathEscape(key+"@"+version)+"/"+url.PathEscape(path), http.StatusFound)
		case "evaluation":
			if f.noEval {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"Evaluation not found"}`))
				return
			}
			writeJSON(map[string]any{"dimensions": map[string]any{"trust": map[string]any{"items": map[string]any{"scan": map[string]any{"score": 5, "userReason": "ok"}}}},
				"summary": "internal", "userSummary": "整体良好", "updatedAt": 1700000000000})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeSkillhub) serveCOS(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.cosHeaders = append(f.cosHeaders, r.Header.Clone())
	f.mu.Unlock()
	parts := strings.SplitN(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/", 3)
	if len(parts) < 2 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	for i := range parts {
		parts[i], _ = url.PathUnescape(parts[i])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch parts[0] {
	case "zip":
		_, _ = w.Write(f.packages[parts[1]])
	case "file":
		if len(parts) == 3 {
			if content, ok := f.files[parts[1]][parts[2]]; ok {
				_, _ = w.Write(content)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}
}

func newSkillhubTestServer(t *testing.T, apiKey string) (*fakeSkillhub, *repo.Store, http.Handler) {
	t.Helper()
	f := newFakeSkillhub(t)
	t.Setenv("SKILLHUB_BASE_URL", f.api.URL)
	t.Setenv("SKILLHUB_API_KEY", apiKey)
	s := newSkillFileServer(t)
	if err := s.PutProject(domain.Project{ID: "other", Key: "OTH", Name: "数据中台", WorkflowID: "requirement-development-production"}); err != nil {
		t.Fatal(err)
	}
	return f, s, skillFileHandler(t, s)
}

func skillhubRequest(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeJSONMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestSkillhubListWhitelistsParamsAndNormalizes(t *testing.T) {
	f, _, h := newSkillhubTestServer(t, "team-secret-key")
	rec := skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills?keyword=pdf&category=office-efficiency&sortBy=bogus&page=2&pageSize=500&evil=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "team-secret-key") {
		t.Fatal("API key leaked into the response")
	}
	q := f.queries["/api/skills"]
	for key, want := range map[string]string{"keyword": "pdf", "category": "office-efficiency", "sortBy": "downloads", "order": "desc", "page": "2", "pageSize": "50"} {
		if q.Get(key) != want {
			t.Fatalf("upstream %s=%q, want %q (all %v)", key, q.Get(key), want, q)
		}
	}
	if q.Has("evil") {
		t.Fatal("unknown parameters must not be forwarded")
	}
	if got := f.headers["/api/skills"].Get("X-API-Key"); got != "team-secret-key" {
		t.Fatalf("X-API-Key = %q", got)
	}
	var body struct {
		Total int64              `json:"total"`
		Page  int                `json:"page"`
		Items []skillhubListItem `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Total != 1234 || body.Page != 2 || len(body.Items) != 2 {
		t.Fatalf("unexpected body %+v", body)
	}
	first, second := body.Items[0], body.Items[1]
	if first.Description != "读取 PDF" || first.Namespace != "team" || first.OwnerName != "PDF Team" || first.Labels["requires_api_key"] != "true" {
		t.Fatalf("first item not normalised: %+v", first)
	}
	if first.Homepage != "https://skillhub.cn/skills/team/pdf-tools" {
		t.Fatalf("homepage = %q", first.Homepage)
	}
	if second.Description != "English only" || second.Labels["requires_api_key"] != "false" {
		t.Fatalf("english fallback / bool label: %+v", second)
	}

	rec = skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills?sortBy=score", nil)
	if rec.Code != http.StatusOK || f.queries["/api/skills"].Get("sortBy") != "score" || f.queries["/api/skills"].Get("pageSize") != "24" {
		t.Fatalf("score sort / default page size: %d %v", rec.Code, f.queries["/api/skills"])
	}
}

func TestSkillhubWithoutAPIKeySendsNoKeyHeader(t *testing.T) {
	f, _, h := newSkillhubTestServer(t, "")
	if rec := skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills", nil); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if _, present := f.headers["/api/skills"]["X-Api-Key"]; present {
		t.Fatal("X-API-Key must be omitted when not configured")
	}
}

func TestSkillhubClientUserKeyAndRedirectHeaderStripping(t *testing.T) {
	f := newFakeSkillhub(t)
	f.publish("team", "pdf-tools", "1.3.0", map[string]string{"SKILL.md": "# pdf"})
	client := newSkillhubClient(f.api.URL, "team-secret-key")
	key := skillhubUserKey(42, "alice")
	if len(key) != 16 || key == skillhubUserKey(43, "alice") || skillhubUserKey(0, "") != "" {
		t.Fatalf("user key %q is not a 16-char pseudonym", key)
	}
	raw, err := client.download(context.Background(), "pdf-tools", "team", "1.3.0", key, 1<<20)
	if err != nil || len(raw) == 0 {
		t.Fatalf("download: %v", err)
	}
	if got := f.headers["/api/v1/download"].Get("X-Client-User-Id"); got != key {
		t.Fatalf("X-Client-User-Id = %q, want %q", got, key)
	}
	if len(f.cosHeaders) != 1 {
		t.Fatalf("expected one COS request, got %d", len(f.cosHeaders))
	}
	for _, name := range []string{"X-Api-Key", "X-Client-User-Id"} {
		if f.cosHeaders[0].Get(name) != "" {
			t.Fatalf("%s must not follow the redirect to the COS host", name)
		}
	}
}

func TestSkillhubErrorMapping(t *testing.T) {
	f, _, h := newSkillhubTestServer(t, "")
	for _, tc := range []struct {
		status, want, hits int
	}{
		{http.StatusNotFound, http.StatusNotFound, 1},
		{http.StatusTooManyRequests, http.StatusTooManyRequests, 2},
		{http.StatusInternalServerError, http.StatusBadGateway, 2},
		{http.StatusServiceUnavailable, http.StatusBadGateway, 2},
	} {
		f.mu.Lock()
		f.failStatus["/api/skills"] = tc.status
		f.hits["/api/skills"] = 0
		f.mu.Unlock()
		rec := skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills", nil)
		if rec.Code != tc.want {
			t.Fatalf("upstream %d answered %d, want %d: %s", tc.status, rec.Code, tc.want, rec.Body.String())
		}
		if msg := decodeJSONMap(t, rec)["error"]; msg == "" || !strings.Contains(fmt.Sprint(msg), "SkillHub") {
			t.Fatalf("missing readable error: %v", msg)
		}
		if got := f.hitCount("/api/skills"); got != tc.hits {
			t.Fatalf("upstream %d hit %d times, want %d", tc.status, got, tc.hits)
		}
	}
}

func TestSkillhubClientTimeoutMapsTo504(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	client := newSkillhubClient(slow.URL, "")
	client.timeout = 50 * time.Millisecond
	_, err := client.categoryList(context.Background(), "")
	se, ok := err.(*skillhubError)
	if !ok || se.Status != http.StatusGatewayTimeout || !strings.Contains(se.Message, "超时") {
		t.Fatalf("timeout error = %#v", err)
	}
}

func TestSkillhubCategoriesCachedAndFiltered(t *testing.T) {
	f, _, h := newSkillhubTestServer(t, "")
	for i := 0; i < 3; i++ {
		rec := skillhubRequest(t, h, http.MethodGet, "/api/skillhub/categories", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "办公效率") || strings.Contains(rec.Body.String(), "下线") {
			t.Fatalf("inactive categories must be dropped: %s", rec.Body.String())
		}
	}
	if got := f.hitCount("/api/v1/categories"); got != 1 {
		t.Fatalf("categories fetched %d times, want 1 (cached)", got)
	}
}

func TestSkillhubDetailVersionsFilesAndPreview(t *testing.T) {
	f, _, h := newSkillhubTestServer(t, "")
	f.publish("team", "pdf-tools", "1.3.0", map[string]string{"SKILL.md": "# PDF\nhello", "bin.dat": "\xff\xfe\x00"})

	rec := skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills/pdf-tools?namespace=team", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail %d: %s", rec.Code, rec.Body.String())
	}
	detail := decodeJSONMap(t, rec)
	skill := detail["skill"].(map[string]any)
	if skill["summary"] != "中文简介" || skill["namespace"] != "team" || skill["homepage"] != "https://skillhub.cn/skills/team/pdf-tools" {
		t.Fatalf("detail not normalised: %v", skill)
	}
	if detail["latestVersion"].(map[string]any)["version"] != "1.3.0" {
		t.Fatalf("latest version: %v", detail["latestVersion"])
	}
	if f.queries["/api/v1/skills/pdf-tools"].Get("namespace") != "team" {
		t.Fatal("namespace must be forwarded")
	}

	rec = skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills/pdf-tools/versions?namespace=team", nil)
	var versions struct {
		Versions []skillhubVersionView `json:"versions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &versions)
	if len(versions.Versions) != 2 {
		t.Fatalf("versions: %s", rec.Body.String())
	}
	reports := versions.Versions[0].SecurityReports
	if !reports["keen"].Safe || reports["keen"].StatusText != "安全，无风险" || reports["keen"].ReportURL != "https://report.example/1" {
		t.Fatalf("keen report: %+v", reports["keen"])
	}
	if reports["other"].Safe || reports["other"].ReportURL != "" {
		t.Fatalf("non-benign report must be unsafe and drop non-http links: %+v", reports["other"])
	}
	if versions.Versions[1].SecurityReports != nil || strings.Contains(rec.Body.String(), `"securityReports":null`) {
		t.Fatalf("missing securityReports must be omitted: %s", rec.Body.String())
	}

	rec = skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills/pdf-tools/files?namespace=team&version=1.3.0", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"SKILL.md"`) {
		t.Fatalf("files %d: %s", rec.Code, rec.Body.String())
	}

	rec = skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills/pdf-tools/file?namespace=team&version=1.3.0&path=SKILL.md", nil)
	preview := decodeJSONMap(t, rec)
	if rec.Code != http.StatusOK || preview["previewable"] != true || preview["content"] != "# PDF\nhello" {
		t.Fatalf("preview %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), f.cos.URL) || strings.Contains(rec.Body.String(), "signature") {
		t.Fatal("signed COS address must not reach the browser")
	}
	rec = skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills/pdf-tools/file?namespace=team&path=big.bin", nil)
	if preview := decodeJSONMap(t, rec); rec.Code != http.StatusOK || preview["previewable"] != false || !strings.Contains(fmt.Sprint(preview["reason"]), "1MB") {
		t.Fatalf("oversized preview %d: %s", rec.Code, rec.Body.String())
	}
	rec = skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills/pdf-tools/file?namespace=team&path=bin.dat", nil)
	if preview := decodeJSONMap(t, rec); preview["previewable"] != false {
		t.Fatalf("binary preview: %s", rec.Body.String())
	}
	for _, bad := range []string{
		"/api/skillhub/skills/pdf-tools/file?path=../etc/passwd",
		"/api/skillhub/skills/pdf-tools?namespace=bad%20ns",
		"/api/skillhub/skills/pdf-tools/files?version=1.0%20rm",
	} {
		if rec := skillhubRequest(t, h, http.MethodGet, bad, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s answered %d, want 400", bad, rec.Code)
		}
	}

	rec = skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills/missing?namespace=team", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing skill answered %d", rec.Code)
	}
}

func TestSkillhubEvaluationAvailability(t *testing.T) {
	f, _, h := newSkillhubTestServer(t, "")
	f.publish("team", "pdf-tools", "1.3.0", map[string]string{"SKILL.md": "# pdf"})
	rec := skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills/pdf-tools/evaluation?namespace=team", nil)
	body := decodeJSONMap(t, rec)
	if rec.Code != http.StatusOK || body["available"] != true || body["userSummary"] != "整体良好" || body["dimensions"] == nil {
		t.Fatalf("evaluation %d: %s", rec.Code, rec.Body.String())
	}
	f.mu.Lock()
	f.noEval = true
	f.mu.Unlock()
	rec = skillhubRequest(t, h, http.MethodGet, "/api/skillhub/skills/pdf-tools/evaluation?namespace=team", nil)
	if body := decodeJSONMap(t, rec); rec.Code != http.StatusOK || body["available"] != false {
		t.Fatalf("missing evaluation %d: %s", rec.Code, rec.Body.String())
	}
}

func installSkillhub(t *testing.T, h http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return skillhubRequest(t, h, http.MethodPost, "/api/skillhub/install", body)
}

func TestSkillhubInstallSnapshotsLatestAndHistoricalVersions(t *testing.T) {
	f, s, h := newSkillhubTestServer(t, "")
	f.publish("team", "pdf-tools", "1.0.0", map[string]string{"pdf-tools/SKILL.md": "# old", "pdf-tools/scripts/a.py": "print(1)"})
	f.publish("team", "pdf-tools", "1.3.0", map[string]string{"SKILL.md": "# new", "scripts/a.py": "print(2)", "references/api.md": "api"})

	rec := installSkillhub(t, h, map[string]any{"slug": "pdf-tools", "namespace": "team", "projectId": "platform"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("install %d: %s", rec.Code, rec.Body.String())
	}
	skill := decodeSkillResponse(t, rec)
	if skill.ID != "pdf-tools" || skill.Name != "Display pdf-tools" || skill.ProjectID != "platform" || skill.Description != "中文简介" {
		t.Fatalf("installed skill: %+v", skill)
	}
	if len(skill.FileRefs) != 3 {
		t.Fatalf("file count = %d, want 3: %+v", len(skill.FileRefs), skill.FileRefs)
	}
	src := skill.Source
	if src == nil || src.Provider != "skillhub" || src.Slug != "pdf-tools" || src.Namespace != "team" || src.Version != "1.3.0" || src.InstalledAt.IsZero() {
		t.Fatalf("source: %+v", src)
	}
	if content, ok := s.SkillFileContent("pdf-tools", "SKILL.md"); !ok || string(content) != "# new" {
		t.Fatalf("SKILL.md = %q", content)
	}

	// A historical version, custom id and name, top-level dir stripped.
	rec = installSkillhub(t, h, map[string]any{"slug": "pdf-tools", "namespace": "team", "version": "1.0.0", "projectId": "other", "id": "pdf-legacy", "name": "旧版 PDF"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("historical install %d: %s", rec.Code, rec.Body.String())
	}
	legacy := decodeSkillResponse(t, rec)
	if legacy.Source.Version != "1.0.0" || legacy.Name != "旧版 PDF" || legacy.ID != "pdf-legacy" {
		t.Fatalf("historical install: %+v", legacy)
	}
	if _, ok := s.SkillFileContent("pdf-legacy", "SKILL.md"); !ok {
		t.Fatal("shared top-level zip directory must be stripped")
	}
}

func TestSkillhubInstallConflicts(t *testing.T) {
	f, s, h := newSkillhubTestServer(t, "")
	f.publish("team", "pdf-tools", "1.3.0", map[string]string{"SKILL.md": "# pdf"})
	if err := s.PutSkill(domain.SkillDefinition{ID: "taken", Name: "Taken", ProjectID: "other"}); err != nil {
		t.Fatal(err)
	}
	if rec := installSkillhub(t, h, map[string]any{"slug": "pdf-tools", "namespace": "team", "projectId": "platform", "id": "taken"}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "数据中台") {
		t.Fatalf("id taken by another project: %d %s", rec.Code, rec.Body.String())
	}
	if rec := installSkillhub(t, h, map[string]any{"slug": "pdf-tools", "namespace": "team", "projectId": "platform"}); rec.Code != http.StatusCreated {
		t.Fatalf("first install %d: %s", rec.Code, rec.Body.String())
	}
	if rec := installSkillhub(t, h, map[string]any{"slug": "pdf-tools", "namespace": "team", "projectId": "platform", "id": "pdf-again"}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "已安装") {
		t.Fatalf("same slug twice in a project: %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []map[string]any{
		{"slug": "pdf-tools", "projectId": ""},
		{"slug": "../x", "projectId": "platform"},
		{"slug": "pdf-tools", "projectId": "platform", "id": "bad id"},
		{"slug": "pdf-tools", "projectId": "platform", "version": "1.0;x"},
	} {
		if rec := installSkillhub(t, h, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%v answered %d, want 400", body, rec.Code)
		}
	}
	if rec := installSkillhub(t, h, map[string]any{"slug": "pdf-tools", "projectId": "nope"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project answered %d", rec.Code)
	}
}

func TestSkillhubInstallFailuresLeaveNoSkill(t *testing.T) {
	f, s, h := newSkillhubTestServer(t, "")
	big := strings.Repeat("x", int(domain.SkillFileMaxBytes)+1)
	f.publish("team", "huge", "1.0.0", map[string]string{"SKILL.md": "# huge", "assets/big.txt": big})
	rec := installSkillhub(t, h, map[string]any{"slug": "huge", "namespace": "team", "projectId": "platform"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "1MB") {
		t.Fatalf("oversized install %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.Skill("huge"); ok || len(s.SkillFileRefs("huge")) != 0 {
		t.Fatal("failed install left a skill behind")
	}

	f.publish("team", "flaky", "1.0.0", map[string]string{"SKILL.md": "# flaky"})
	f.mu.Lock()
	f.failStatus["/api/v1/download"] = http.StatusInternalServerError
	f.mu.Unlock()
	rec = installSkillhub(t, h, map[string]any{"slug": "flaky", "namespace": "team", "projectId": "platform"})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("download failure %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.Skill("flaky"); ok {
		t.Fatal("failed download left a skill behind")
	}
	if rec := installSkillhub(t, h, map[string]any{"slug": "ghost", "namespace": "team", "projectId": "platform"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown slug answered %d", rec.Code)
	}
}

func TestSkillhubConcurrentInstallSucceedsOnce(t *testing.T) {
	f, s, h := newSkillhubTestServer(t, "")
	f.publish("team", "pdf-tools", "1.3.0", map[string]string{"SKILL.md": "# pdf"})
	var created, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := installSkillhub(t, h, map[string]any{"slug": "pdf-tools", "namespace": "team", "projectId": "platform", "id": fmt.Sprintf("pdf-%d", i)})
			switch rec.Code {
			case http.StatusCreated:
				created.Add(1)
			case http.StatusConflict:
				conflicts.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if created.Load() != 1 || conflicts.Load() != 3 {
		t.Fatalf("created=%d conflicts=%d", created.Load(), conflicts.Load())
	}
	count := 0
	for _, skill := range s.Skills() {
		if skill.Source != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d SkillHub skills stored, want 1", count)
	}
}

func TestSkillhubUpdateReplacesFilesAndKeepsMetadata(t *testing.T) {
	f, s, h := newSkillhubTestServer(t, "")
	f.publish("team", "review", "1.0.0", map[string]string{"SKILL.md": "# v1", "old.md": "gone soon"})
	if rec := installSkillhub(t, h, map[string]any{"slug": "review", "namespace": "team", "projectId": "platform", "id": "my-review"}); rec.Code != http.StatusCreated {
		t.Fatalf("install %d: %s", rec.Code, rec.Body.String())
	}
	installed, _ := s.Skill("my-review")
	installed.Name, installed.Description, installed.Shared = "团队评审", "自定义描述", true
	if err := s.PutSkill(installed); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(domain.AgentDefinition{ID: "reviewer", Name: "评审员", ProjectID: "platform", Skills: []string{"my-review"}, ExecutorID: "codebuddy"}); err != nil {
		t.Fatal(err)
	}

	f.publish("team", "review", "1.1.0", map[string]string{"SKILL.md": "# v1.1", "new.md": "fresh"})
	rec := skillhubRequest(t, h, http.MethodPost, "/api/skillhub/update", map[string]any{"skillId": "my-review"})
	if rec.Code != http.StatusOK {
		t.Fatalf("update %d: %s", rec.Code, rec.Body.String())
	}
	updated := decodeSkillResponse(t, rec)
	if updated.Source.Version != "1.1.0" || updated.Name != "团队评审" || updated.Description != "自定义描述" || !updated.Shared {
		t.Fatalf("update lost metadata: %+v", updated)
	}
	if !updated.Source.InstalledAt.Equal(installed.Source.InstalledAt) || !updated.Source.UpdatedAt.After(installed.Source.UpdatedAt) {
		t.Fatalf("source times: installed %v updated %v", updated.Source.InstalledAt, updated.Source.UpdatedAt)
	}
	paths := map[string]bool{}
	for _, ref := range updated.FileRefs {
		paths[ref.Path] = true
	}
	if !paths["new.md"] || paths["old.md"] || len(paths) != 2 {
		t.Fatalf("update must REPLACE files: %v", paths)
	}
	if agent, _ := s.Agent("reviewer"); len(agent.Skills) != 1 || agent.Skills[0] != "my-review" {
		t.Fatalf("role binding changed: %+v", agent.Skills)
	}

	// Explicit older version, then a failing update keeps the current state.
	if rec := skillhubRequest(t, h, http.MethodPost, "/api/skillhub/update", map[string]any{"skillId": "my-review", "version": "1.0.0"}); rec.Code != http.StatusOK || decodeSkillResponse(t, rec).Source.Version != "1.0.0" {
		t.Fatalf("update to explicit version %d: %s", rec.Code, rec.Body.String())
	}
	f.publish("team", "review", "2.0.0", map[string]string{"SKILL.md": "# v2", "big.txt": strings.Repeat("y", int(domain.SkillFileMaxBytes)+1)})
	if rec := skillhubRequest(t, h, http.MethodPost, "/api/skillhub/update", map[string]any{"skillId": "my-review"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized update %d: %s", rec.Code, rec.Body.String())
	}
	after, _ := s.Skill("my-review")
	if after.Source.Version != "1.0.0" {
		t.Fatalf("failed update changed version to %s", after.Source.Version)
	}
	if content, _ := s.SkillFileContent("my-review", "SKILL.md"); string(content) != "# v1" {
		t.Fatalf("failed update changed files: %q", content)
	}

	if err := s.PutSkill(domain.SkillDefinition{ID: "hand-made", Name: "Hand", ProjectID: "platform"}); err != nil {
		t.Fatal(err)
	}
	if rec := skillhubRequest(t, h, http.MethodPost, "/api/skillhub/update", map[string]any{"skillId": "hand-made"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("updating a hand-made skill answered %d", rec.Code)
	}
	if rec := skillhubRequest(t, h, http.MethodPost, "/api/skillhub/update", map[string]any{"skillId": "nope"}); rec.Code != http.StatusNotFound {
		t.Fatalf("updating a missing skill answered %d", rec.Code)
	}
}

func TestSkillhubSourcedSkillIsReadOnlyAndSourceTamperProof(t *testing.T) {
	f, s, h := newSkillhubTestServer(t, "")
	f.publish("team", "pdf-tools", "1.3.0", map[string]string{"SKILL.md": "# pdf"})
	if rec := installSkillhub(t, h, map[string]any{"slug": "pdf-tools", "namespace": "team", "projectId": "platform"}); rec.Code != http.StatusCreated {
		t.Fatalf("install %d", rec.Code)
	}

	rec := requestMultipart(t, h, http.MethodPost, "/api/skills/pdf-tools/files", func(w *multipart.Writer) {
		addFilePartWithPath(w, "SKILL.md", []byte("# hacked"))
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "只读") {
		t.Fatalf("upload to sourced skill %d: %s", rec.Code, rec.Body.String())
	}
	rec = skillhubRequest(t, h, http.MethodDelete, "/api/skills/pdf-tools/files/SKILL.md", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete on sourced skill %d: %s", rec.Code, rec.Body.String())
	}
	if rec := skillhubRequest(t, h, http.MethodGet, "/api/skills/pdf-tools/files/SKILL.md", nil); rec.Code != http.StatusOK {
		t.Fatalf("download must stay available: %d", rec.Code)
	}

	// Name/description edits keep the source; a forged or dropped source is ignored.
	rec = skillhubRequest(t, h, http.MethodPut, "/api/skills", map[string]any{
		"id": "pdf-tools", "name": "改名", "description": "新描述", "projectId": "platform",
		"source": map[string]any{"provider": "skillhub", "slug": "evil", "version": "9.9.9"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put %d: %s", rec.Code, rec.Body.String())
	}
	saved, _ := s.Skill("pdf-tools")
	if saved.Name != "改名" || saved.Source == nil || saved.Source.Slug != "pdf-tools" || saved.Source.Version != "1.3.0" {
		t.Fatalf("source tampered: %+v", saved.Source)
	}
	rec = skillhubRequest(t, h, http.MethodPut, "/api/skills", map[string]any{"id": "pdf-tools", "name": "改名", "projectId": "platform", "shared": true})
	if saved, _ := s.Skill("pdf-tools"); rec.Code != http.StatusOK || saved.Source == nil || !saved.Shared {
		t.Fatalf("source dropped by a metadata save: %d %+v", rec.Code, saved)
	}
	rec = skillhubRequest(t, h, http.MethodPut, "/api/skills", map[string]any{
		"id": "forged", "name": "Forged", "projectId": "platform", "source": map[string]any{"provider": "skillhub", "slug": "x", "version": "1"},
	})
	if forged, _ := s.Skill("forged"); rec.Code != http.StatusOK || forged.Source != nil {
		t.Fatalf("new skills must not carry a client source: %+v", forged.Source)
	}
	// Hand-made skills keep the regular upload path.
	rec = requestMultipart(t, h, http.MethodPost, "/api/skills/forged/files", func(w *multipart.Writer) {
		addFilePartWithPath(w, "SKILL.md", []byte("# mine"))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("hand-made upload %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReadSkillZipBytesMatchesUploadRules(t *testing.T) {
	files, err := readSkillZipBytes(zipBytes(t, map[string]string{"pkg/SKILL.md": "# a", "__MACOSX/pkg/._SKILL.md": "", "pkg/b/c.txt": "c"}, nil), "pkg.zip", nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	if !paths["SKILL.md"] || !paths["b/c.txt"] || len(paths) != 2 {
		t.Fatalf("unexpected paths %v", paths)
	}
	if _, err := readSkillZipBytes([]byte("not a zip"), "bad.zip", nil); err == nil || !strings.Contains(err.Error(), "bad.zip") {
		t.Fatalf("invalid zip error = %v", err)
	}
}
