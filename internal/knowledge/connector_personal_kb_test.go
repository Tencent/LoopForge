package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

func isAppErrKind(err error, kind apperr.Kind) bool {
	ae, ok := apperr.As(err)
	return ok && ae.Kind == kind
}

func TestNativeRESTConnectorDomainPublicEndpoint(t *testing.T) {
	for _, typ := range []string{
		domain.ConnectorTypeWeKnora,
		domain.ConnectorTypeObsidian,
		domain.ConnectorTypeNotion,
		domain.ConnectorTypeSiYuan,
		domain.ConnectorTypeFeishu,
	} {
		if !domain.IsNativeRESTConnectorType(typ) {
			t.Fatalf("expected %q to be a native REST connector type", typ)
		}
		pub := domain.KnowledgeConnector{
			Type:     typ,
			Endpoint: "https://user:pass@kb.example.com/base?secret=leak#frag",
		}.Public()
		if pub.Endpoint != "https://kb.example.com/base" {
			t.Fatalf("type %s unexpected public endpoint %q", typ, pub.Endpoint)
		}
	}
	if domain.IsNativeRESTConnectorType(domain.ConnectorTypeMCP) {
		t.Fatalf("mcp should not be classified as native REST connector")
	}
}

func TestSiYuanConnectorAuthScopeAndEvidence(t *testing.T) {
	const rawSecret = "siyuan-super-secret-token-99"
	var gotAuthHeader string
	var gotMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")

		if gotAuthHeader == "Token bad-token-leak" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": -1,
				"msg":  "auth failed for bad-token-leak",
			})
			return
		}
		if gotAuthHeader != "Token "+rawSecret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":-1,"msg":"unauthorized"}`))
			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		switch r.URL.Path {
		case "/api/notebook/lsNotebooks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "",
				"data": map[string]any{
					"notebooks": []map[string]any{
						{"id": "nb-work", "name": "工作笔记", "closed": false},
						{"id": "nb-private", "name": "私密日记", "closed": false},
						{"id": "nb-closed", "name": "已关闭", "closed": true},
					},
				},
			})
		case "/api/filetree/listDocsByPath":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"files": []map[string]any{
						{"id": "doc-1", "name": "架构规范.sy", "path": "/doc-1.sy"},
					},
				},
			})
		case "/api/search/fullTextSearchBlock":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"blocks": []map[string]any{
						{
							"id":       "blk-1",
							"rootID":   "doc-1",
							"box":      "nb-work",
							"hPath":    "/架构规范",
							"markdown": "",
							"content":  "采用 <mark>Go 原生</mark> 只读架构",
						},
						{
							"id":       "blk-2",
							"rootID":   "doc-secret",
							"box":      "nb-private",
							"hPath":    "/私密计划",
							"markdown": "未授权笔记本内容",
						},
					},
				},
			})
		case "/api/block/getBlockInfo":
			id, _ := body["id"].(string)
			box := "nb-work"
			hPath := "/架构规范"
			if id == "doc-secret" {
				box = "nb-private"
				hPath = "/私密计划"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"box": box, "rootID": id, "hPath": hPath},
			})
		case "/api/export/exportMdContent":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"hPath":   "/架构规范",
					"content": "# 架构规范\n\nLoopForge 使用原生只读连接器。",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := NewService(newMemoryConnectorStore(), nil)
	ctx := context.Background()

	// 1. HTTP 200 with code == -1 must fail Check() and redact secret token.
	badRes := svc.CheckConnector(ctx, 1, "", &ConnectorInput{
		Name:     "Bad SiYuan",
		Type:     domain.ConnectorTypeSiYuan,
		Endpoint: srv.URL,
		Secret:   domain.KnowledgeConnectorSecret{Token: "Token bad-token-leak"},
	})
	if badRes.OK {
		t.Fatalf("expected Check to fail on code=-1")
	}
	if strings.Contains(badRes.Error, "bad-token-leak") {
		t.Fatalf("expected secret token to be redacted, got %q", badRes.Error)
	}

	// 2. Create connector (with "Bearer " prefix stripped) and verify Check + tools.
	created, err := svc.CreateConnector(ctx, 1, ConnectorInput{
		Name:     "我的思源",
		Type:     domain.ConnectorTypeSiYuan,
		Endpoint: srv.URL,
		Secret:   domain.KnowledgeConnectorSecret{Token: "Bearer " + rawSecret},
	})
	if err != nil {
		t.Fatalf("create siyuan connector: %v", err)
	}
	checkRes := svc.CheckConnector(ctx, 1, created.ID, nil)
	if !checkRes.OK || len(checkRes.Sources) != 2 {
		t.Fatalf("expected OK with 2 open notebooks, got %+v", checkRes)
	}
	if gotMethod != http.MethodPost || gotAuthHeader != "Token "+rawSecret {
		t.Fatalf("unexpected method/auth: %s / %q", gotMethod, gotAuthHeader)
	}

	exec := NewExecutor(svc)
	ref := domain.KnowledgeRef{
		ConnectorID: created.ID,
		Mode:        "allowlist",
		Sources:     []string{"nb-work"},
	}

	// 3. list_knowledge_bases respects allowlist.
	_, kbText, err := exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolListKnowledgeBases, map[string]any{}, "")
	if err != nil || !strings.Contains(kbText, "nb-work") || strings.Contains(kbText, "nb-private") {
		t.Fatalf("unexpected list_knowledge_bases: err=%v text=%s", err, kbText)
	}

	// 4. search_knowledge strips <mark>, filters out nb-private, produces citation evidences.
	searchRead, searchText, err := exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolSearchKnowledge, map[string]any{"query": "只读"}, "")
	if err != nil {
		t.Fatalf("search_knowledge: %v", err)
	}
	if strings.Contains(searchText, "<mark>") || strings.Contains(searchText, "nb-private") {
		t.Fatalf("search result leaked highlight tag or private notebook: %s", searchText)
	}
	if len(searchRead.Evidences) != 1 || searchRead.Evidences[0].DocumentID != "doc-1" {
		t.Fatalf("unexpected search evidences: %+v", searchRead.Evidences)
	}

	// 5. read_document succeeds for authorized notebook and blocks unauthorized notebook with Forbidden.
	readRes, readText, err := exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "doc-1"}, "")
	if err != nil || !strings.Contains(readText, "[[knowledge:") || len(readRes.Evidences) == 0 {
		t.Fatalf("read_document failed: err=%v ev=%+v text=%s", err, readRes.Evidences, readText)
	}
	_, _, err = exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "doc-secret"}, "")
	if !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for unauthorized notebook doc, got %v", err)
	}
}

func TestObsidianConnectorSelfSignedTLSAuthAndScope(t *testing.T) {
	const apiKey = "obsidian-key-xyz"
	var hiddenReadCalls atomic.Int32

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authenticated": auth == "Bearer "+apiKey,
				"status":        "OK",
			})
			return
		}
		if auth != "Bearer "+apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/vault/":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []string{"Projects/", "Diary/", "README.md", ".obsidian/", ".git/", ".private.md"},
			})
		case "/vault/Projects/":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []string{"arch.md", ".hidden.md", " .padded-hidden.md", "nested/.secret.md"},
			})
		case "/vault/Projects/arch.md":
			w.Header().Set("Content-Type", "application/vnd.olrapi.note+json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": "# Architecture\n\nLocal REST API read-only vault.",
				"tags":    []string{"design"},
			})
		case "/vault/.obsidian/secret.md", "/vault/Projects/.hidden.md", "/vault/Projects/nested/.secret.md":
			hiddenReadCalls.Add(1)
			w.Header().Set("Content-Type", "application/vnd.olrapi.note+json")
			_ = json.NewEncoder(w).Encode(map[string]any{"content": "hidden credentials"})
		case "/search/simple/":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"filename": "Projects/arch.md",
					"score":    0.95,
					"matches":  []map[string]any{{"context": "Local REST API read-only vault."}},
				},
				{
					"filename": "Diary/secret.md",
					"score":    0.99,
					"matches":  []map[string]any{{"context": "Private diary entry"}},
				},
				{
					"filename": "Projects/.hidden.md",
					"score":    0.98,
					"matches":  []map[string]any{{"context": "hidden credentials"}},
				},
				{
					"filename": ".obsidian/secret.md",
					"score":    0.97,
					"matches":  []map[string]any{{"context": "plugin credentials"}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := NewService(newMemoryConnectorStore(), nil)
	ctx := context.Background()

	// 1. Public host with tlsInsecureSkipVerify=true must be rejected during validation.
	_, err := svc.CreateConnector(ctx, 1, ConnectorInput{
		Name:     "Public Obsidian",
		Type:     domain.ConnectorTypeObsidian,
		Endpoint: "https://example.com:27124",
		Config: &domain.KnowledgeConnectorConfig{
			Extra: map[string]string{"tlsInsecureSkipVerify": "true"},
		},
		Secret: domain.KnowledgeConnectorSecret{Token: apiKey},
	})
	if err == nil || !strings.Contains(err.Error(), "仅允许对本地或内网地址跳过自签名证书校验") {
		t.Fatalf("expected public host self-signed TLS rejection, got %v", err)
	}

	// 2. GET / returning HTTP 200 with authenticated=false must fail Check().
	unauthRes := svc.CheckConnector(ctx, 1, "", &ConnectorInput{
		Name:     "Unauth Obsidian",
		Type:     domain.ConnectorTypeObsidian,
		Endpoint: srv.URL,
		Config: &domain.KnowledgeConnectorConfig{
			Extra: map[string]string{"tlsInsecureSkipVerify": "true"},
		},
		Secret: domain.KnowledgeConnectorSecret{Token: "wrong-key"},
	})
	if unauthRes.OK || !strings.Contains(unauthRes.Error, "鉴权未通过") {
		t.Fatalf("expected Check failure when authenticated=false, got %+v", unauthRes)
	}

	// 3. Local 127.0.0.1 TLS with tlsInsecureSkipVerify=true succeeds.
	created, err := svc.CreateConnector(ctx, 1, ConnectorInput{
		Name:     "Local Obsidian",
		Type:     domain.ConnectorTypeObsidian,
		Endpoint: srv.URL,
		Config: &domain.KnowledgeConnectorConfig{
			Extra: map[string]string{"tlsInsecureSkipVerify": "true"},
		},
		Secret: domain.KnowledgeConnectorSecret{Token: apiKey},
	})
	if err != nil {
		t.Fatalf("create obsidian connector: %v", err)
	}
	okRes := svc.CheckConnector(ctx, 1, created.ID, nil)
	if !okRes.OK || len(okRes.Sources) != 3 {
		t.Fatalf("expected OK with 3 sources (Diary, Projects, __root__), got %+v", okRes)
	}

	// 4. SourceScope isolates Projects from Diary.
	exec := NewExecutor(svc)
	ref := domain.KnowledgeRef{
		ConnectorID: created.ID,
		Mode:        "allowlist",
		Sources:     []string{"Projects"},
	}

	_, listText, err := exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolListDocuments, map[string]any{"knowledge_base_id": "Projects"}, "")
	if err != nil || !strings.Contains(listText, "Projects/arch.md") {
		t.Fatalf("unexpected obsidian document list: err=%v text=%s", err, listText)
	}
	_, searchText, err := exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolSearchKnowledge, map[string]any{"query": "vault"}, "")
	if err != nil || !strings.Contains(searchText, "Projects/arch.md") || strings.Contains(searchText, "Diary/secret.md") {
		t.Fatalf("unexpected obsidian search result: err=%v text=%s", err, searchText)
	}
	for _, hidden := range []string{".obsidian/secret.md", "Projects/.hidden.md", "Projects/.padded-hidden.md", "Projects/nested/.secret.md"} {
		if strings.Contains(listText, hidden) || strings.Contains(searchText, hidden) {
			t.Fatalf("hidden path %q leaked through list/search: list=%s search=%s", hidden, listText, searchText)
		}
	}

	readRec, readText, err := exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "Projects/arch.md"}, "")
	if err != nil || !strings.Contains(readText, "Local REST API read-only vault.") || len(readRec.Evidences) != 1 {
		t.Fatalf("expected authorized obsidian read to succeed: err=%v text=%s", err, readText)
	}

	_, _, err = exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "Diary/secret.md"}, "")
	if !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for Diary/secret.md, got %v", err)
	}
	allRef := domain.KnowledgeRef{ConnectorID: created.ID, Mode: "all"}
	for _, hidden := range []string{".obsidian/secret.md", "Projects/.hidden.md", "Projects/nested/.secret.md"} {
		_, _, err = exec.ExecuteReferenceTool(ctx, 1, allRef, WeKnoraToolReadDocument, map[string]any{"knowledge_id": hidden}, "")
		if !isAppErrKind(err, apperr.Invalid) {
			t.Fatalf("expected Invalid for hidden path %q, got %v", hidden, err)
		}
	}
	if got := hiddenReadCalls.Load(); got != 0 {
		t.Fatalf("hidden document reads reached upstream %d times", got)
	}
}

func TestNotionConnectorHeadersBlocksAnd429Retry(t *testing.T) {
	const token = "ntn_test_secret_123"
	var searchCalls atomic.Int32
	var gotVersion string
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("Notion-Version")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/users/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "user", "id": "bot-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/search":
			if searchCalls.Add(1) == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "rate limited"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{
						"object": "page",
						"id":     "11111111-1111-1111-1111-111111111111",
						"url":    "https://www.notion.so/root-page",
						"parent": map[string]any{"type": "workspace", "workspace": true},
						"properties": map[string]any{
							"title": map[string]any{
								"type":  "title",
								"title": []map[string]any{{"plain_text": "研发手册"}},
							},
						},
					},
				},
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/pages/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "page",
				"id":     "11111111-1111-1111-1111-111111111111",
				"url":    "https://www.notion.so/root-page",
				"parent": map[string]any{"type": "workspace", "workspace": true},
				"properties": map[string]any{
					"title": map[string]any{
						"type":  "title",
						"title": []map[string]any{{"plain_text": "研发手册"}},
					},
				},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/children"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"id": "b1", "type": "heading_1", "heading_1": map[string]any{"rich_text": []map[string]any{{"plain_text": "Overview"}}}},
					{"id": "b2", "type": "bulleted_list_item", "bulleted_list_item": map[string]any{"rich_text": []map[string]any{{"plain_text": "Item A"}}}},
					{"id": "b3", "type": "code", "code": map[string]any{"language": "go", "rich_text": []map[string]any{{"plain_text": "fmt.Println(1)"}}}},
					{"id": "b4", "type": "quote", "quote": map[string]any{"rich_text": []map[string]any{{"plain_text": "Read only"}}}},
				},
				"has_more": false,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// 1. Blank endpoint defaults to https://api.notion.com.
	f := newNotionFactory()
	normEndpoint, _, _, err := f.ValidateAndNormalize("", domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{Token: token}, false)
	if err != nil || normEndpoint != notionDefaultBaseURL {
		t.Fatalf("expected default notion endpoint %q, got %q err=%v", notionDefaultBaseURL, normEndpoint, err)
	}

	// 2. Check succeeds after automatic 429 retry and sends Notion-Version: 2026-03-11.
	svc := NewService(newMemoryConnectorStore(), nil)
	ctx := context.Background()
	created, err := svc.CreateConnector(ctx, 1, ConnectorInput{
		Name:     "我的 Notion",
		Type:     domain.ConnectorTypeNotion,
		Endpoint: srv.URL,
		Secret:   domain.KnowledgeConnectorSecret{Token: token},
	})
	if err != nil {
		t.Fatalf("create notion connector: %v", err)
	}
	res := svc.CheckConnector(ctx, 1, created.ID, nil)
	if !res.OK || len(res.Sources) != 1 {
		t.Fatalf("expected notion Check OK after 429 retry, got %+v", res)
	}
	if gotVersion != notionAPIVersion || gotAuth != "Bearer "+token {
		t.Fatalf("unexpected headers: version=%q auth=%q", gotVersion, gotAuth)
	}
	if searchCalls.Load() < 2 {
		t.Fatalf("expected at least 2 /v1/search calls due to 429 retry, got %d", searchCalls.Load())
	}

	// 3. read_document renders Markdown blocks cleanly.
	exec := NewExecutor(svc)
	ref := domain.KnowledgeRef{ConnectorID: created.ID, Mode: "all"}
	_, readText, err := exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "11111111111111111111111111111111"}, "")
	if err != nil {
		t.Fatalf("notion read_document: %v", err)
	}
	if !strings.Contains(readText, "[[knowledge:") {
		t.Fatalf("expected citation marker in notion read_document output, got:\n%s", readText)
	}
	jsonPart, _, _ := strings.Cut(readText, "\n证据引用编号")
	var payload struct {
		Chunks []struct {
			Content string `json:"content"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal([]byte(jsonPart), &payload); err != nil || len(payload.Chunks) == 0 {
		t.Fatalf("unmarshal notion read_document payload: err=%v raw=%s", err, jsonPart)
	}
	mdContent := payload.Chunks[0].Content
	for _, want := range []string{"# Overview", "- Item A", "```go", "fmt.Println(1)", "> Read only"} {
		if !strings.Contains(mdContent, want) {
			t.Fatalf("expected notion markdown to contain %q, got:\n%s", want, mdContent)
		}
	}
}

func TestFeishuConnectorTATCacheRefreshPermErrorAndCitation(t *testing.T) {
	const appSecret = "feishu-app-secret-ultra-private"
	var tatCalls atomic.Int32
	var forceExpiredOnce atomic.Bool
	var forcePermDenied atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			n := tatCalls.Add(1)
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["app_id"] != "cli_test123" || body["app_secret"] != appSecret {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 10003, "msg": "invalid app_secret " + appSecret})
				return
			}
			tok := "t-first-tat"
			if n >= 2 {
				tok = "t-refreshed-tat"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":                0,
				"tenant_access_token": tok,
				"expire":              7200,
			})
		case "/open-apis/wiki/v2/spaces":
			if forceExpiredOnce.CompareAndSwap(true, false) {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 99991663, "msg": "token expired"})
				return
			}
			if forcePermDenied.Load() {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 1770032, "msg": "no perm " + appSecret})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"items": []map[string]any{
						{"space_id": "sp-eng", "name": "产研知识库", "description": "研发规范"},
						{"space_id": "sp-hr", "name": "人事制度", "description": "HR"},
					},
					"has_more": false,
				},
			})
		case "/open-apis/wiki/v2/spaces/sp-eng/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"items": []map[string]any{
						{
							"space_id":      "sp-eng",
							"node_token":    "wikcnDeploy01",
							"obj_token":     "doxcnDeploy01",
							"obj_type":      "docx",
							"title":         "发布流程规范",
							"obj_edit_time": "1730000000",
							"has_child":     false,
						},
					},
					"has_more": false,
				},
			})
		case "/open-apis/wiki/v2/spaces/get_node":
			if r.URL.Query().Get("token") == "wikcnSecretHR" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"node": map[string]any{
							"space_id":   "sp-hr",
							"node_token": "wikcnSecretHR",
							"obj_token":  "doxcnSecretHR",
							"obj_type":   "docx",
							"title":      "薪酬保密制度",
						},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
		case "/open-apis/docx/v1/documents/doxcnDeploy01/raw_content":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"content": "发布前必须完成灰度验证与回滚演练。",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := NewService(newMemoryConnectorStore(), nil)
	ctx := context.Background()
	created, err := svc.CreateConnector(ctx, 1, ConnectorInput{
		Name:     "飞书知识库",
		Type:     domain.ConnectorTypeFeishu,
		Endpoint: srv.URL,
		Config: &domain.KnowledgeConnectorConfig{
			Extra: map[string]string{"appId": "cli_test123"},
		},
		Secret: domain.KnowledgeConnectorSecret{Token: appSecret},
	})
	if err != nil {
		t.Fatalf("create feishu connector: %v", err)
	}

	// 1. First two calls reuse cached TAT (only 1 token request).
	if _, err := svc.ListConnectorSources(ctx, 1, created.ID); err != nil {
		t.Fatalf("first ListConnectorSources: %v", err)
	}
	if _, err := svc.ListConnectorSources(ctx, 1, created.ID); err != nil {
		t.Fatalf("second ListConnectorSources: %v", err)
	}
	if tatCalls.Load() != 1 {
		t.Fatalf("expected TAT to be cached across calls, got %d token requests", tatCalls.Load())
	}

	// 2. Upstream code 99991663 triggers automatic TAT refresh and succeeds on retry.
	forceExpiredOnce.Store(true)
	if _, err := svc.ListConnectorSources(ctx, 1, created.ID); err != nil {
		t.Fatalf("expected auto refresh on 99991663, got %v", err)
	}
	if tatCalls.Load() != 2 {
		t.Fatalf("expected 2nd TAT request after 99991663, got %d", tatCalls.Load())
	}

	// 3. Search + read produce [[knowledge:ev_...]] citation markers and enforce SourceScope.
	exec := NewExecutor(svc)
	ref := domain.KnowledgeRef{
		ConnectorID: created.ID,
		Mode:        "allowlist",
		Sources:     []string{"sp-eng"},
	}
	searchRead, searchText, err := exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolSearchKnowledge, map[string]any{"query": "灰度验证"}, "")
	if err != nil {
		t.Fatalf("feishu search_knowledge: %v", err)
	}
	if len(searchRead.Evidences) != 1 || !strings.Contains(searchText, "[[knowledge:ev_") {
		t.Fatalf("expected feishu citation suffix with ev_ prefix, got ev=%+v text=%s", searchRead.Evidences, searchText)
	}
	_, readText, err := exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "wikcnDeploy01"}, "")
	if err != nil || !strings.Contains(readText, "[[knowledge:ev_") {
		t.Fatalf("feishu read_document failed: err=%v text=%s", err, readText)
	}
	_, _, err = exec.ExecuteReferenceTool(ctx, 1, ref, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "wikcnSecretHR"}, "")
	if !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for unauthorized feishu space node, got %v", err)
	}

	// 4. Error code 1770032 returns actionable Chinese hint and redacts appSecret.
	forcePermDenied.Store(true)
	_, err = svc.ListConnectorSources(ctx, 1, created.ID)
	if err == nil || !strings.Contains(err.Error(), "成员设置") {
		t.Fatalf("expected actionable 1770032 message containing 成员设置, got %v", err)
	}
	if strings.Contains(err.Error(), appSecret) {
		t.Fatalf("appSecret leaked in error message: %v", err)
	}
}

func TestNativeHTTPClientRejectsCrossOriginRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}))
	defer redirector.Close()

	client, err := newNativeHTTPClient(nativeDefaultTimeout, redirector.URL, false)
	if err != nil {
		t.Fatalf("newNativeHTTPClient: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, redirector.URL+"/start", nil)
	req.Header.Set("Authorization", "Bearer top-secret")
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "跨源重定向") {
		t.Fatalf("expected cross-origin redirect rejection, got %v", err)
	}
	var unwrapped error = err
	if unwrapped == nil {
		t.Fatal("expected non-nil error")
	}
	_ = errors.Unwrap(unwrapped)
}
