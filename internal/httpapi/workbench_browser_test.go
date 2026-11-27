package httpapi

import (
	"net"
	"net/http"
	"os"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// This loopback-only, in-memory server is for browser acceptance and never
// connects to a real database or uses deployment credentials.
func TestWorkbenchBrowserServer(t *testing.T) {
	if os.Getenv("LOOPFORGE_BROWSER_TEST_SERVER") != "1" {
		t.Skip("browser test server is opt-in")
	}
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "disposable-browser-session-secret")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "")
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutUser(domain.User{LoginName: "browser-test", ChineseName: "浏览器测试", PlatformRole: domain.PlatformRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	user, ok := store.UserByLoginName("browser-test")
	if !ok {
		t.Fatal("test user missing")
	}
	if err := store.SetLocalPassword(user.ID, "disposable-browser-password"); err != nil {
		t.Fatal(err)
	}
	// Optional live-provider acceptance. The key stays in this in-memory store;
	// normal browser tests neither need nor read any deployment credentials.
	if key := os.Getenv("LOOPFORGE_BROWSER_DEEPSEEK_KEY"); key != "" {
		baseURL, protocol := "https://api.deepseek.com/v1", "chat"
		if _, err := store.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{
			AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{
				"dsh": {APIKey: key, BaseURL: &baseURL, Protocol: &protocol},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutProject(domain.Project{ID: "welcome", Key: "WELCOME", Name: "浏览器测试项目"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAssistantSession(user.ID, domain.AssistantSession{ID: "chat-browser-test", Title: "浏览器验收会话", Messages: []domain.AssistantMessage{{Role: "user", Content: "原有会话消息"}, {Role: "assistant", Content: `{"message":"原有助手回复"}`}}}); err != nil {
		t.Fatal(err)
	}
	handler := New(store, engine.New(store, runtime.NewManager(t.TempDir())), tracker.NewService(), "", "")
	listener, err := net.Listen("tcp", "127.0.0.1:18089")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Logf("browser test server ready at %s", listener.Addr())
	if err := http.Serve(listener, handler); err != nil {
		t.Fatal(err)
	}
}
