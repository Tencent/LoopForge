package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssistantSourceConfinement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(root, "safe.go"), []byte("package source\n// revision conflict check\n"), 0600)
	os.WriteFile(filepath.Join(outside, "secret.go"), []byte("sensitive"), 0600)
	os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0600)
	os.Symlink(filepath.Join(outside, "secret.go"), filepath.Join(root, "escape.go"))
	files, err := assistantSourceFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(files, "\n")
	if !strings.Contains(joined, "safe.go") {
		t.Fatalf("missing safe.go: %v", files)
	}
	for _, p := range []string{".env", "escape.go", "secret.go"} {
		if strings.Contains(joined, p) {
			t.Fatalf("leaked %s: %v", p, files)
		}
	}
	for _, p := range []string{"../secret.go", "/etc/passwd", ".env"} {
		if assistantSourceAllowed(p) {
			t.Fatalf("unsafe path allowed: %s", p)
		}
	}
}

func TestAssistantSearchThenAnswer(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "save.go"), []byte("package sample\n// revision conflict rejects stale writes\n"), 0600)
	t.Setenv("AGENTFLOW_ASSISTANT_SOURCE_DIR", root)
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.PutUser(domain.User{LoginName: "reader", CodeBuddyAPIKey: "reader-secret"})
	u, _ := st.UserByLoginName("reader")
	calls := 0
	h := &assistantHandlers{deps: &deps{repo: st}, generate: func(ctx context.Context, prompt string, user domain.User) (string, error) {
		calls++
		if strings.Contains(prompt, "你是 AgentFlow") {
			t.Fatal("system prompt must stay on --system-prompt, not -p")
		}
		return "旧版本写入会被拒绝，见 save.go:2。", nil
	}}
	req := httptest.NewRequest("POST", "/api/assistant/chat", strings.NewReader(`{"messages":[{"role":"user","content":"版本冲突如何处理"}]}`))
	req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(u)))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	h.chat(c)
	var result assistantReply
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &result) != nil || !strings.Contains(result.Message, "save.go:2") {
		t.Fatalf("status %d result %s calls %d", rec.Code, rec.Body.String(), calls)
	}
	if calls != 1 {
		t.Fatalf("expected read-only answer without terminal retry, got %d", calls)
	}
}
