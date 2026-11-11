package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/engine"
	"agentflow/internal/knowledge"
	kbengine "agentflow/internal/knowledge/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// memKeywordIndex 是没有 MySQL ngram 全文索引时（sqlite 测试）的假关键词
// 适配器：扫来源里已落库的正文做子串匹配，让试搜能命中刚写的文档。引擎的
// 写正文、切块、读原文都是真的，只替换召回。
type memKeywordIndex struct{ store *repo.Store }

func (m *memKeywordIndex) Search(_ context.Context, query string, sources []string, _ kbengine.SearchOptions) ([]kbengine.Hit, error) {
	terms := strings.Fields(strings.ToLower(query))
	out := []kbengine.Hit{}
	for _, src := range sources {
		for _, d := range m.store.KnowledgeDocuments(src) {
			body := strings.ToLower(d.Body)
			matched := false
			for _, t := range terms {
				if t != "" && strings.Contains(body, t) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			out = append(out, kbengine.Hit{
				Source: src, Commit: d.Commit, Path: d.Path, Title: d.Title,
				StartLine: 1, EndLine: d.LineCount, Text: d.Body, Score: 1,
			})
		}
	}
	return out, nil
}

func newKBMgmtServer(t *testing.T) (*repo.Store, http.Handler) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	old := newKnowledgeKeywordIndex
	newKnowledgeKeywordIndex = func(st *repo.Store) kbengine.KeywordIndex {
		return &memKeywordIndex{store: st}
	}
	t.Cleanup(func() { newKnowledgeKeywordIndex = old; s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	return s, h
}

// createProject 走真实 POST /api/projects，顺带验证建项目时注入的托管库保证。
func createProject(t *testing.T, h http.Handler, id string) {
	t.Helper()
	rec := doJSON(t, h, http.MethodPost, "/api/projects", `{"id":"`+id+`","name":"项目`+id+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("create project %s: %d %s", id, rec.Code, rec.Body.String())
	}
}

const kbDocPath = "guides/deploy.md"

func writeDoc(t *testing.T, h http.Handler, projectID, sourceID, word string) string {
	t.Helper()
	content := "# 部署手册\n\n" + word + "服务器需要配置防火墙规则。\n"
	body, _ := json.Marshal(map[string]string{
		"projectId": projectID, "sourceId": sourceID, "path": kbDocPath, "content": content,
	})
	rec := doJSON(t, h, http.MethodPost, "/api/knowledge/bases/documents", string(body), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("write doc: %d %s", rec.Code, rec.Body.String())
	}
	var out knowledge.WriteResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Commit
}

func searchWord(t *testing.T, h http.Handler, projectID, query string, sources []string) (*httptest.ResponseRecorder, knowledge.TrialResult) {
	t.Helper()
	payload := map[string]any{"projectId": projectID, "query": query}
	if sources != nil {
		payload["sources"] = sources
	}
	body, _ := json.Marshal(payload)
	rec := doJSON(t, h, http.MethodPost, "/api/knowledge/bases/search", string(body), nil)
	var out knowledge.TrialResult
	if rec.Code == http.StatusOK {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func TestKnowledgeManagementReadSearchAndIsolation(t *testing.T) {
	s, h := newKBMgmtServer(t)

	// 建项目后应立即有且只有一个托管库（S9 注入）。
	createProject(t, h, "pA")
	if bases := s.KnowledgeBases("pA"); len(bases) != 1 {
		t.Fatalf("建项目应自动建一个托管库，实际 %d: %+v", len(bases), bases)
	}

	// 打开知识中心：幂等保证 + 列表。
	ovRec := doJSON(t, h, http.MethodGet, "/api/knowledge/bases?projectId=pA", "", nil)
	if ovRec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", ovRec.Code, ovRec.Body.String())
	}
	var ov knowledge.Overview
	if err := json.Unmarshal(ovRec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if len(ov.Bases) != 1 || len(ov.Bases[0].Sources) != 1 {
		t.Fatalf("overview 应有 1 个库 1 个托管来源: %+v", ov)
	}
	hosted := ov.Bases[0].Sources[0]
	if hosted.Kind != "hosted" || hosted.ID != "kb-hosted-pA" {
		t.Fatalf("托管来源字段错误: %+v", hosted)
	}

	// 写一篇含独特词的 Markdown；出处版本是修订号。
	if commit := writeDoc(t, h, "pA", hosted.ID, "麒麟"); commit != "1" {
		t.Fatalf("首次写入版本应为修订号 1，实际 %q", commit)
	}

	// 试搜命中，出处版本为修订号。
	_, result := searchWord(t, h, "pA", "麒麟", nil)
	hits := result.Result.Results
	if len(hits) != 1 || hits[0].Source != hosted.ID || hits[0].Path != kbDocPath {
		t.Fatalf("试搜应命中刚写的文档: %+v", hits)
	}
	if !strings.Contains(hits[0].Citation, "@1:") {
		t.Fatalf("出处应是修订号而非 commit: %q", hits[0].Citation)
	}

	// 读原文：行号与正文一致，版本是修订号。
	docURL := "/api/knowledge/bases/document?projectId=pA&source=" + hosted.ID + "&path=" + kbDocPath
	docRec := doJSON(t, h, http.MethodGet, docURL, "", nil)
	if docRec.Code != http.StatusOK {
		t.Fatalf("read doc: %d %s", docRec.Code, docRec.Body.String())
	}
	var doc knowledge.Document
	if err := json.Unmarshal(docRec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Commit != "1" || doc.LineCount == 0 || len(doc.Lines) != doc.LineCount {
		t.Fatalf("读原文头错误: %+v", doc)
	}
	if doc.Lines[0].Number != 1 || doc.Lines[len(doc.Lines)-1].Number != doc.LineCount {
		t.Fatalf("行号不连续: %+v", doc.Lines)
	}
	joined := ""
	for _, l := range doc.Lines {
		joined += l.Text + "\n"
	}
	if !strings.Contains(joined, "麒麟") {
		t.Fatalf("原文未包含写入的词: %q", joined)
	}

	// 覆盖后版本递增、旧词不再命中、新词命中。
	if commit := writeDoc(t, h, "pA", hosted.ID, "貔貅"); commit != "2" {
		t.Fatalf("覆盖后版本应为 2，实际 %q", commit)
	}
	_, oldRes := searchWord(t, h, "pA", "麒麟", nil)
	if len(oldRes.Result.Results) != 0 {
		t.Fatalf("覆盖后旧词不应再命中: %+v", oldRes.Result.Results)
	}
	_, newRes := searchWord(t, h, "pA", "貔貅", nil)
	if len(newRes.Result.Results) != 1 {
		t.Fatalf("覆盖后新词应命中: %+v", newRes.Result.Results)
	}
	docRec = doJSON(t, h, http.MethodGet, docURL, "", nil)
	_ = json.Unmarshal(docRec.Body.Bytes(), &doc)
	if doc.Commit != "2" || strings.Contains(mustDocText(doc), "麒麟") {
		t.Fatalf("覆盖后读到的应是新正文: %+v", doc)
	}

	// 项目 B 不能碰项目 A 的来源。
	createProject(t, h, "pB")
	forbidden := []struct {
		name, method, path, body string
	}{
		{"读原文", http.MethodGet, "/api/knowledge/bases/document?projectId=pB&source=" + hosted.ID + "&path=" + kbDocPath, ""},
		{"试搜点名 A 的来源", http.MethodPost, "/api/knowledge/bases/search", `{"projectId":"pB","query":"貔貅","sources":["` + hosted.ID + `"]}`},
		{"写入 A 的来源", http.MethodPost, "/api/knowledge/bases/documents", `{"projectId":"pB","sourceId":"` + hosted.ID + `","path":"x.md","content":"# x"}`},
		{"文档清单", http.MethodGet, "/api/knowledge/bases/documents?projectId=pB&source=" + hosted.ID, ""},
	}
	for _, tc := range forbidden {
		rec := doJSON(t, h, tc.method, tc.path, tc.body, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 应拒绝其它项目，实际 %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	// 不点名来源时检索只在 B 自己的空库里查，结果为空而不是越权。
	_, scoped := searchWord(t, h, "pB", "貔貅", nil)
	if len(scoped.Result.Results) != 0 || strings.Join(scoped.Sources, ",") != "kb-hosted-pB" {
		t.Fatalf("B 的检索应只收窄到自己的空库: sources=%v hits=%v", scoped.Sources, scoped.Result.Results)
	}
}

func mustDocText(d knowledge.Document) string {
	out := ""
	for _, l := range d.Lines {
		out += l.Text + "\n"
	}
	return out
}

func TestEnsureHostedBaseConcurrentViaHTTP(t *testing.T) {
	s, h := newKBMgmtServer(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := doJSON(t, h, http.MethodGet, "/api/knowledge/bases?projectId=pConc", "", nil)
			if rec.Code != http.StatusOK {
				t.Errorf("concurrent overview: %d %s", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	if bases := s.KnowledgeBases("pConc"); len(bases) != 1 {
		t.Fatalf("并发打开知识中心应只产生一个托管库，实际 %d", len(bases))
	}
	if srcs := s.KnowledgeSources("kb-pConc"); len(srcs) != 1 || srcs[0].Kind != "hosted" {
		t.Fatalf("并发应只产生一个托管来源: %+v", srcs)
	}
}

func TestRegisterGitSourceAndTriggerSyncOnlySetPending(t *testing.T) {
	s, h := newKBMgmtServer(t)
	createProject(t, h, "pC")

	// 触发同步的接口只置待同步，不克隆、不写正文（同步执行归 S6）。
	body, _ := json.Marshal(map[string]string{
		"projectId": "pC", "baseId": "kb-pC", "name": "公开样例",
		"gitUrl": "https://github.com/example/repo.git", "branch": "main", "pathPrefix": "docs/",
	})
	rec := doJSON(t, h, http.MethodPost, "/api/knowledge/bases/sources", string(body), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("register git source: %d %s", rec.Code, rec.Body.String())
	}
	var src struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	if src.Kind != "git" || src.Status != knowledge.SourceStatusPending {
		t.Fatalf("登记的 Git 来源应为待同步: %+v", src)
	}
	// 待同步来源没有正文，同步不会在这里发生。
	if docs := s.KnowledgeDocuments(src.ID); len(docs) != 0 {
		t.Fatalf("触发接口不应同步正文: %d", len(docs))
	}

	// 模拟 S6 同步失败写了错误，再由同步按钮重新置为待同步并清空错误。
	stored, ok := s.KnowledgeSource(src.ID)
	if !ok {
		t.Fatal("来源未落库")
	}
	stored.Status = knowledge.SourceStatusError
	stored.LastError = "clone failed"
	if err := s.PutKnowledgeSource(stored); err != nil {
		t.Fatal(err)
	}
	syncBody, _ := json.Marshal(map[string]string{"projectId": "pC", "sourceId": src.ID})
	syncRec := doJSON(t, h, http.MethodPost, "/api/knowledge/bases/sync", string(syncBody), nil)
	if syncRec.Code != http.StatusOK {
		t.Fatalf("trigger sync: %d %s", syncRec.Code, syncRec.Body.String())
	}
	after, _ := s.KnowledgeSource(src.ID)
	if after.Status != knowledge.SourceStatusPending || after.LastError != "" {
		t.Fatalf("触发同步应只置待同步并清错: %+v", after)
	}
	if docs := s.KnowledgeDocuments(src.ID); len(docs) != 0 {
		t.Fatal("同步按钮不应写入正文")
	}

	// 非法地址与其它项目的库都被拒绝。
	badBody, _ := json.Marshal(map[string]string{
		"projectId": "pC", "baseId": "kb-pC", "gitUrl": "ftp://example.com/repo",
	})
	if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/bases/sources", string(badBody), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 Git 地址应 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	otherBody, _ := json.Marshal(map[string]string{
		"projectId": "pD", "baseId": "kb-pC", "gitUrl": "https://github.com/example/repo.git",
	})
	if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/bases/sources", string(otherBody), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("其它项目的库应 403，实际 %d %s", rec.Code, rec.Body.String())
	}
}

func TestKnowledgePageNoLongerSubmitsURLOrToken(t *testing.T) {
	t.Helper()
	var source string
	found := false
	for _, p := range []string{
		filepath.Join("..", "..", "web", "knowledge.js"),
		filepath.Join("web", "knowledge.js"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			source, found = string(b), true
			break
		}
	}
	if !found {
		t.Fatal("web/knowledge.js not found")
	}
	for _, marker := range []string{
		"/api/knowledge/settings",
		"name=\"url\"",
		"name=\"token\"",
		"clearToken",
		"tokenConfigured",
		"kbSettingsPayload",
	} {
		if strings.Contains(source, marker) {
			t.Fatalf("页面仍包含连接配置痕迹 %q", marker)
		}
	}
	// 新管理接口确实被界面引用。
	if !strings.Contains(source, "/api/knowledge/bases") {
		t.Fatal("页面未引用新的知识库管理接口")
	}
}

// TestKnowledgeVectorEndpoint 覆盖按知识库启用向量的管理接口：状态回显、
// 项目归属校验，以及未配置 Qdrant/embedding 时拒绝启用（纯关键词回退）。
func TestKnowledgeVectorEndpoint(t *testing.T) {
	s, h := newKBMgmtServer(t)
	createProject(t, h, "pVec")
	if err := s.SetKnowledgeVectorConfig("kb-pVec", false, "", 0, ""); err != nil {
		t.Fatal(err)
	}

	// GET 状态：测试进程没有配置向量组件，available 应为 false。
	statusRec := doJSON(t, h, http.MethodGet, "/api/knowledge/bases/vector", "", nil)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("vector status: %d %s", statusRec.Code, statusRec.Body.String())
	}
	var status struct {
		Status kbengine.VectorStatus `json:"status"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status.Available {
		t.Fatal("未配置环境变量时 available 应为 false")
	}

	// 点名别的项目的知识库应被拒绝。
	wrong := `{"projectId":"other","baseId":"kb-pVec","enabled":true}`
	if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/bases/vector", wrong, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("跨项目启用应被拒绝: %d %s", rec.Code, rec.Body.String())
	}

	// 未配置 Qdrant/embedding 时启用应被拒，不静默成功。
	enable := `{"projectId":"pVec","baseId":"kb-pVec","enabled":true}`
	if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/bases/vector", enable, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("未配置向量时启用应报错: %d %s", rec.Code, rec.Body.String())
	}
	if base, _ := s.KnowledgeBase("kb-pVec"); base.VectorEnabled {
		t.Fatal("启用失败后该库不应处于向量启用状态")
	}
}
