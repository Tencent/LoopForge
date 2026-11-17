package repo

import (
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestDeepSeekCredentialsPersistAndRemainPrivate(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutUser(domain.User{LoginName: "deepseek-user", ClaudeCodeAPIKey: "existing-claude-key"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("deepseek-user")
	var update UserCredentialUpdate
	if err := json.Unmarshal([]byte(`{"agentCliCredentials":{"deepseek":{"apiKey":"deepseek-test-secret","baseUrl":"https://gateway.example/v1"}}}`), &update); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserCredentials(u.ID, update); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(domain.User{ID: u.ID, LoginName: u.LoginName, ChineseName: "updated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserCredentials(u.ID, UserCredentialUpdate{GitToken: "git-test-secret"}); err != nil {
		t.Fatal(err)
	}
	saved, _ := st.User(u.ID)
	cred := saved.AgentCLICredentials["deepseek"]
	if cred.APIKey != "deepseek-test-secret" || cred.BaseURL != "https://gateway.example/v1" {
		t.Fatal("DeepSeek credentials did not survive persistence and partial updates")
	}
	public := domain.PublicUser(saved)
	data, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "existing-claude-key") {
		t.Fatal("public user exposed credentials")
	}
	if !public.AgentCLICredentials["deepseek"].APIKeyConfigured {
		t.Fatal("public user is missing the DeepSeek configured flag")
	}
	if err := json.Unmarshal([]byte(`{"agentCliCredentials":{"deepseek":{"apiKeyClear":true,"baseUrl":"","models":[]}}}`), &update); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserCredentials(u.ID, update); err != nil {
		t.Fatal(err)
	}
	cleared, _ := st.User(u.ID)
	if _, ok := cleared.AgentCLICredentials["deepseek"]; ok || cleared.ClaudeCodeAPIKey != "existing-claude-key" || cleared.GitToken != "git-test-secret" {
		t.Fatal("clearing DeepSeek changed another provider or retained its credentials")
	}
}
