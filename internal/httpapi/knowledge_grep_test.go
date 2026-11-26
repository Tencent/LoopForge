package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
)

func TestKnowledgeGrepOnProxyAndMainBot(t *testing.T) {
	s, h := newKnowledgeTestServer(t)
	token, err := knowledge.SignProxyToken(knowledge.SessionSecret(), knowledge.ProxyClaims{
		ProjectID: "p1", AgentID: "dev", UserID: 3, RunID: "run-grep",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, http.MethodPost, knowledge.ProxyPath, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_grep","arguments":{"pattern":"检索"}}}`, map[string]string{"Authorization": "Bearer " + token})
	text, isErr, msg := rpcText(t, rec)
	if isErr || msg != "" || !strings.Contains(text, `"path":"x.md"`) || !strings.Contains(text, "检索正文") || !strings.Contains(text, `"citation"`) {
		t.Fatalf("proxy grep: err=%s body=%s", msg, text)
	}
	if strings.Contains(text, `"regex":true`) {
		t.Fatalf("grep must default to literal: %s", text)
	}
	denied, isErr, _ := rpcText(t, doJSON(t, h, http.MethodPost, knowledge.ProxyPath, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"kb_grep","arguments":{"pattern":"检索","sources":["b"]}}}`, map[string]string{"Authorization": "Bearer " + token}))
	if !isErr || !strings.Contains(denied, "授权范围") {
		t.Fatalf("proxy grep scope: %s", denied)
	}

	resetAgentToolSessionsForTest()
	session := newKnowledgeAgentToolSession(t, "p1", AgentToolAssistant, false)
	session.RunID = "run-bot"
	session.ChatSessionID = "chat-grep"
	text, isErr = agentToolCall(t, h, session, knowledge.ToolGrep, `{"pattern":"检索正文","regex":false}`)
	if isErr || !strings.Contains(text, `"line":3`) {
		t.Fatalf("main bot grep: isError=%t body=%s", isErr, text)
	}
	regexText, isErr := agentToolCall(t, h, session, knowledge.ToolGrep, `{"pattern":"检索正.","regex":true}`)
	if isErr || !strings.Contains(regexText, "检索正文") {
		t.Fatalf("main bot regex grep: isError=%t body=%s", isErr, regexText)
	}

	restarted := newKnowledgeTestHandler(t, s)
	usage := doJSON(t, restarted, http.MethodGet, "/api/knowledge/citations?projectId=p1", "", nil)
	var payload struct {
		Available bool                        `json:"available"`
		Cited     []knowledge.CitedDocument   `json:"cited"`
		Uncited   []knowledge.UncitedDocument `json:"uncited"`
	}
	if err := json.Unmarshal(usage.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode citations: %v body=%s", err, usage.Body.String())
	}
	if !payload.Available {
		t.Fatalf("citations unavailable: %s", usage.Body.String())
	}
	var count int
	for _, item := range payload.Cited {
		if item.Path == "x.md" && item.SourceID == "a" {
			count = item.Count
		}
	}
	if count < 2 {
		t.Fatalf("x.md should be cited by both channels: %+v", payload.Cited)
	}
	for _, item := range payload.Uncited {
		if item.Path == "x.md" {
			t.Fatalf("cited document listed as uncited: %+v", payload.Uncited)
		}
	}
	other := doJSON(t, restarted, http.MethodGet, "/api/knowledge/citations?projectId=p2", "", nil)
	if strings.Contains(other.Body.String(), `"path":"x.md"`) && strings.Contains(other.Body.String(), `"count"`) {
		var otherPayload struct {
			Cited []knowledge.CitedDocument `json:"cited"`
		}
		_ = json.Unmarshal(other.Body.Bytes(), &otherPayload)
		for _, item := range otherPayload.Cited {
			if item.Path == "x.md" {
				t.Fatalf("project p2 saw p1 citations: %s", other.Body.String())
			}
		}
	}

	rows := s.KnowledgeCitations("p1", 20)
	if len(rows) < 2 {
		t.Fatalf("stored citations = %+v", rows)
	}
	sawRun, sawSession := false, false
	for _, row := range rows {
		if row.RunID == "run-grep" && row.AgentID == "dev" && row.Tool == knowledge.ToolGrep {
			sawRun = true
		}
		if row.SessionID == "chat-grep" && row.RunID == "run-bot" {
			sawSession = true
		}
		if row.ProjectID != "p1" {
			t.Fatalf("citation project = %+v", row)
		}
	}
	if !sawRun || !sawSession {
		t.Fatalf("channels not both stored: %+v", rows)
	}
}

func TestCitationWriteFailureDoesNotAffectGrep(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutKnowledgeBase(domain.KnowledgeBase{ID: "a", Name: "a", ProjectID: "p1", Layer: "project"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: "a", BaseID: "a", Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "p1", BaseID: "a", Permission: "submit"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeDocument(domain.KnowledgeDocument{SourceID: "a", Path: "x.md", Body: "唯一词 ALPHA\n", Commit: "1"}); err != nil {
		t.Fatal(err)
	}
	svc := knowledge.NewService(failingCitationRepo{Store: s}, func() string { return "id1" }, staticKnowledgeUpstream{})
	text, hits, err := callKnowledgeTool(context.Background(), svc, knowledge.Caller{ProjectID: "p1", RunID: "run-1"}, knowledge.ToolGrep, map[string]any{"pattern": "ALPHA"})
	if err != nil {
		t.Fatalf("citation write failure leaked into grep: %v", err)
	}
	if hits != 1 || !strings.Contains(text, "ALPHA") || !strings.Contains(text, `"citation"`) {
		t.Fatalf("grep result changed: hits=%d text=%s", hits, text)
	}
}

type failingCitationRepo struct {
	*repo.Store
}

func (failingCitationRepo) PutKnowledgeCitation(domain.KnowledgeCitation) error {
	return errCitationDown
}

var errCitationDown = errString("citation store down")

type errString string

func (e errString) Error() string { return string(e) }

func TestDocumentFreshnessMarksChangedCitation(t *testing.T) {
	s, h := newKnowledgeTestServer(t)
	current := doJSON(t, h, http.MethodGet, "/api/knowledge/bases/document?projectId=p1&source=a&path=x.md&commit=1", "", nil)
	if current.Code != http.StatusOK || !strings.Contains(current.Body.String(), `"freshness":"current"`) {
		t.Fatalf("current doc: %d %s", current.Code, current.Body.String())
	}
	if err := s.PutKnowledgeDocument(domain.KnowledgeDocument{SourceID: "a", Path: "x.md", Body: "检索正文\n已经改写\n", Commit: "2", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	changed := doJSON(t, h, http.MethodGet, "/api/knowledge/bases/document?projectId=p1&source=a&path=x.md&commit=1", "", nil)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"freshness":"changed"`) || !strings.Contains(changed.Body.String(), `"current_version":"2"`) {
		t.Fatalf("changed doc: %d %s", changed.Code, changed.Body.String())
	}
	body := `{"projectId":"p1","citations":["a@1:x.md#L1","a@2:x.md#L1","b@1:secret.md#L1"]}`
	rec := doJSON(t, h, http.MethodPost, "/api/knowledge/citations/freshness", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("freshness: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []knowledge.Freshness `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 3 || payload.Items[0].Status != knowledge.FreshnessChanged || payload.Items[0].Current != "2" ||
		payload.Items[1].Status != knowledge.FreshnessCurrent || payload.Items[2].Status != knowledge.FreshnessDeleted || payload.Items[2].Current != "" {
		t.Fatalf("freshness items = %+v", payload.Items)
	}
}
