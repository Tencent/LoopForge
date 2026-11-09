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
	if err := json.Unmarshal([]byte(`{"deepSeekApiKey":"deepseek-test-secret","deepSeekBaseUrl":"https://gateway.example/anthropic"}`), &update); err != nil {
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
	data, _ := json.Marshal(saved)
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["deepSeekApiKey"] != "deepseek-test-secret" || fields["deepSeekBaseUrl"] != "https://gateway.example/anthropic" {
		t.Fatal("DeepSeek credentials did not survive persistence and partial updates")
	}
	public, _ := json.Marshal(domain.PublicUser(saved))
	if strings.Contains(string(public), "secret") || strings.Contains(string(public), "existing-claude-key") {
		t.Fatal("public user exposed credentials")
	}
	if err := json.Unmarshal(public, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["deepSeekApiKeyConfigured"] != true {
		t.Fatal("public user is missing the DeepSeek configured flag")
	}
	if err := json.Unmarshal([]byte(`{"deepSeekApiKeyClear":true,"deepSeekBaseUrl":""}`), &update); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserCredentials(u.ID, update); err != nil {
		t.Fatal(err)
	}
	cleared, _ := st.User(u.ID)
	data, _ = json.Marshal(cleared)
	fields = nil
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["deepSeekApiKey"] != nil || fields["deepSeekBaseUrl"] != nil || cleared.ClaudeCodeAPIKey != "existing-claude-key" || cleared.GitToken != "git-test-secret" {
		t.Fatal("clearing DeepSeek changed another provider or retained its credentials")
	}
}
