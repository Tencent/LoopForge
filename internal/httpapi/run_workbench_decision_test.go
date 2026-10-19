package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkbenchDecisionRejectsStaleAttempt(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.PutRun(domain.Run{ID: "decision", Status: "waiting", Nodes: map[string]*domain.NodeRun{"test": {NodeID: "test", Status: "waiting_approval", Attempt: 2, ReworkCount: 1}}}); err != nil {
		t.Fatal(err)
	}
	h := &runsHandlers{deps: &deps{repo: st, engine: engine.New(st, runtime.NewManager(t.TempDir()))}}
	g := gin.New()
	g.POST("/api/runs/:id/approve", h.run)
	request := func(attempt string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/runs/decision/approve", strings.NewReader(`{"nodeId":"test","expected":{"status":"waiting_approval","attempt":`+attempt+`,"reworkCount":1}}`))
		req.Header.Set("Content-Type", "application/json")
		g.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("1"); rec.Code == 200 {
		t.Fatal("stale card approved latest attempt")
	}
	r, _ := st.RunState("decision")
	if r.Nodes["test"].Status != "waiting_approval" {
		t.Fatal("stale action changed state")
	}
	if rec := request("2"); rec.Code != 200 {
		t.Fatalf("current action: %s", rec.Body.String())
	}
}
