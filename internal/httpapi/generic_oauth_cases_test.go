package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/gongfeng"

	"github.com/gin-gonic/gin"
)

func TestGenericWorkflowHelperBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &catalogHandlers{deps: &deps{repo: f.s}}
	if _, err := h.adoptGenericWorkflow(domain.WorkflowDefinition{}, "id", "name", "", ""); err == nil {
		t.Fatal("blank target project accepted")
	}
	if _, err := h.adoptGenericWorkflow(domain.WorkflowDefinition{}, "id", "name", "", "missing"); err == nil {
		t.Fatal("missing target project accepted")
	}
	if _, err := h.cloneGenericTemplateAgents("missing", "platform"); err == nil {
		t.Fatal("missing generic template accepted")
	}

	if got := remapAssetIDs(nil, map[string]string{"a": "b"}); got != nil {
		t.Fatalf("nil remap = %#v", got)
	}
	got := remapAssetIDs([]string{"a", "keep"}, map[string]string{"a": "mapped"})
	if len(got) != 2 || got[0] != "mapped" || got[1] != "keep" {
		t.Fatalf("remap = %#v", got)
	}
	candidates := genericAssetIDCandidates("asset", "project")
	if len(candidates) != 2 || candidates[1] != "asset-project" {
		t.Fatalf("candidates = %#v", candidates)
	}
	if got := h.pickAvailableID([]string{"first", "second"}, func(id string) bool { return id == "first" }); got != "second" {
		t.Fatalf("picked %q", got)
	}
	if got := h.pickAvailableID([]string{"first", "second"}, func(string) bool { return true }); !strings.HasPrefix(got, "first-") {
		t.Fatalf("random fallback %q", got)
	}

	now := time.Now().UTC()
	rules, err := h.cloneGenericTemplateRules([]domain.RuleDefinition{{ID: "cvb-r2-rule", Name: "Rule"}}, "platform", now)
	if err != nil || rules["cvb-r2-rule"] == "" {
		t.Fatalf("rules = %#v %v", rules, err)
	}
	rules, err = h.cloneGenericTemplateRules([]domain.RuleDefinition{{ID: "cvb-r2-rule", Name: "Rule"}}, "platform", now)
	if err != nil || rules["cvb-r2-rule"] != "cvb-r2-rule" {
		t.Fatalf("reused rules = %#v %v", rules, err)
	}
	skills, err := h.cloneGenericTemplateSkills([]domain.SkillDefinition{{ID: "cvb-r2-skill", Name: "Skill"}}, "platform", now)
	if err != nil || skills["cvb-r2-skill"] == "" {
		t.Fatalf("skills = %#v %v", skills, err)
	}
	skills, err = h.cloneGenericTemplateSkills([]domain.SkillDefinition{{ID: "cvb-r2-skill", Name: "Skill"}}, "platform", now)
	if err != nil || skills["cvb-r2-skill"] != "cvb-r2-skill" {
		t.Fatalf("reused skills = %#v %v", skills, err)
	}

	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-r2-private", Name: "Private", ProjectID: "platform", Driver: "bwrap"}); err != nil {
		t.Fatal(err)
	}
	if h.genericTemplateSandbox("cvb-r2-private", "platform") != "cvb-r2-private" {
		t.Fatal("preferred project sandbox not selected")
	}
	if h.genericTemplateSandbox("missing", "platform") == "" {
		t.Fatal("project fallback sandbox not selected")
	}
	_ = h.resolveGenericTemplateMCP([]string{"missing-market-item"}, "platform")
}

func TestGongfengOAuthValidationBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	client := auth.NewClient(auth.Config{SessionSecret: "cvb-oauth-secret"})
	h := &authHandlers{deps: &deps{repo: f.s, authClient: client}}
	for _, key := range []string{"AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID", "AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET", "AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI"} {
		t.Setenv(key, "")
	}
	cvBGongfengCallRound2(t, h.gongfengOAuthStart, "/api/auth/gongfeng/start", http.StatusServiceUnavailable)
	cvBGongfengCallRound2(t, h.gongfengOAuthCallback, "/api/auth/gongfeng/callback", http.StatusServiceUnavailable)

	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID", "id")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET", "secret")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI", "http://example.test/callback")
	cvBGongfengCallRound2(t, h.gongfengOAuthCallback, "/api/auth/gongfeng/callback?error=denied%3Cbad%3E", http.StatusBadRequest)
	cvBGongfengCallRound2(t, h.gongfengOAuthCallback, "/api/auth/gongfeng/callback?state=bad&code=x", http.StatusBadRequest)
	state, err := signGongfengOAuthState(gongfengOAuthState{UserID: f.user.ID, Exp: time.Now().Add(time.Minute).Unix(), Nonce: "n"}, "cvb-oauth-secret")
	if err != nil {
		t.Fatal(err)
	}
	cvBGongfengCallRound2(t, h.gongfengOAuthCallback, "/api/auth/gongfeng/callback?state="+url.QueryEscape(state), http.StatusBadRequest)
	expired, _ := signGongfengOAuthState(gongfengOAuthState{UserID: f.user.ID, Exp: time.Now().Add(-time.Minute).Unix(), Nonce: "n"}, "cvb-oauth-secret")
	cvBGongfengCallRound2(t, h.gongfengOAuthCallback, "/api/auth/gongfeng/callback?state="+url.QueryEscape(expired)+"&code=x", http.StatusBadRequest)

	if _, err := parseGongfengOAuthState("bad", "secret"); err == nil {
		t.Fatal("malformed state accepted")
	}
	if _, err := parseGongfengOAuthState("%%%.00", "secret"); err == nil {
		t.Fatal("invalid state encoding accepted")
	}
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "")
	if !gongfengLoginMatches("User", " user ") {
		t.Fatal("case-insensitive login mismatch")
	}
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "1")
	if !gongfengLoginMatches("one", "two") {
		t.Fatal("preview login override ignored")
	}
	for _, cfg := range []gongfeng.Config{{RedirectURI: "::bad"}, {RedirectURI: "https://host.test/callback"}} {
		if gongfengOAuthReturnURL(cfg) == "" {
			t.Fatal("empty OAuth return URL")
		}
	}
}

func cvBGongfengCallRound2(t *testing.T, handler gin.HandlerFunc, target string, want int) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	handler(c)
	if rec.Code != want {
		t.Errorf("%s = %d want %d: %s", target, rec.Code, want, rec.Body.String())
	}
}
