package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAgentCLICredentialsRoundTrip(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutUser(domain.User{LoginName: "agent-cli-owner"}); err != nil {
		t.Fatal(err)
	}
	user, ok := st.UserByLoginName("agent-cli-owner")
	if !ok {
		t.Fatal("missing user")
	}
	base := "https://gateway.example/v1"
	protocol := "chat"
	models := []domain.CLIModel{{ID: "model-a", Name: "Model A"}}
	updated, err := st.UpdateUserCredentials(user.ID, UserCredentialUpdate{AgentCLICredentials: map[string]AgentCLICredentialUpdate{
		"opencode": {APIKey: "oc-secret", BaseURL: &base, Protocol: &protocol, Models: &models},
		"shell":    {APIKey: "nope"},
	}})
	if err == nil || updated.ID != 0 {
		t.Fatal("unknown CLI was accepted")
	}
	updated, err = st.UpdateUserCredentials(user.ID, UserCredentialUpdate{AgentCLICredentials: map[string]AgentCLICredentialUpdate{
		"opencode": {APIKey: "oc-secret", BaseURL: &base, Protocol: &protocol, Models: &models},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(domain.User{ID: user.ID, LoginName: "agent-cli-owner", ChineseName: "刷新"}); err != nil {
		t.Fatal(err)
	}
	got, ok := st.User(user.ID)
	if !ok {
		t.Fatal("missing saved user")
	}
	cred := got.AgentCLICredentials["opencode"]
	if cred.APIKey != "oc-secret" || cred.BaseURL != base || cred.Protocol != "chat" || len(cred.Models) != 1 || cred.Models[0].ID != "model-a" {
		t.Fatalf("agent CLI credential = %+v", cred)
	}
	if got.CodexAPIKey != "" {
		t.Fatal("agent CLI credential wrote a Codex column")
	}
	public := domain.PublicUser(got)
	raw, _ := json.Marshal(public)
	if strings.Contains(string(raw), "oc-secret") || !public.AgentCLICredentials["opencode"].APIKeyConfigured {
		t.Fatal("public user leaked the agent CLI key or lost the configured flag")
	}
	cleared, err := st.UpdateUserCredentials(user.ID, UserCredentialUpdate{AgentCLICredentials: map[string]AgentCLICredentialUpdate{
		"opencode": {APIKeyClear: true, BaseURL: new(string), Protocol: new(string), Models: &[]domain.CLIModel{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.AgentCLICredentials) != 0 {
		t.Fatalf("clear left %+v", cleared.AgentCLICredentials)
	}
}

func TestProviderUserCredentials(t *testing.T) {
	for _, driver := range []string{"memory", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			var st *Store
			var err error
			if driver == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("set AGENTFLOW_TEST_MYSQL_DSN for MySQL integration")
				}
				st, err = Open(dsn)
			} else {
				st, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			login := fmt.Sprintf("provider_test_%d", time.Now().UnixNano())
			defer st.DB.Exec("DELETE FROM users WHERE login_name=?", login)
			if err := st.PutUser(domain.User{LoginName: login}); err != nil {
				t.Fatal(err)
			}
			u, _ := st.UserByLoginName(login)
			codexURL, claudeURL := "https://codex.example/v1", "https://claude.example"
			codexModels := []domain.CLIModel{{ID: "gpt-5.5", Name: "gpt-5.5"}}
			claudeModels := []domain.CLIModel{{ID: "claude-sonnet", Name: "Sonnet"}}
			_, err = st.UpdateUserCredentials(u.ID, UserCredentialUpdate{CodexAPIKey: "codex-secret", CodexBaseURL: &codexURL, CodexModels: &codexModels, ClaudeCodeAPIKey: "claude-secret", ClaudeCodeBaseURL: &claudeURL, ClaudeCodeModels: &claudeModels})
			if err != nil {
				t.Fatal(err)
			}
			// A login refresh and a partial credential save must preserve all providers.
			if err := st.PutUser(domain.User{ID: u.ID, LoginName: login, ChineseName: "更新姓名"}); err != nil {
				t.Fatal(err)
			}
			_, err = st.UpdateUserCredentials(u.ID, UserCredentialUpdate{GitToken: "git-secret"})
			if err != nil {
				t.Fatal(err)
			}
			got, ok := st.User(u.ID)
			if !ok || got.CodexAPIKey != "codex-secret" || got.ClaudeCodeAPIKey != "claude-secret" || got.CodexBaseURL != codexURL || got.ClaudeCodeBaseURL != claudeURL || len(got.CodexModels) != 1 || got.CodexModels[0].ID != "gpt-5.5" || got.CodexModels[0].Name != "" || len(got.ClaudeCodeModels) != 1 || got.ClaudeCodeModels[0].Name != "Sonnet" {
				t.Fatalf("credentials did not survive refresh and partial update: %+v %+v", got.CodexModels, got.ClaudeCodeModels)
			}
			{
				var configRows int
				if err := st.DB.Raw(`SELECT COUNT(*) FROM user_configs WHERE user_id=?`, u.ID).Scan(&configRows).Error; err != nil {
					t.Fatal(err)
				}
				if configRows != 1 {
					t.Fatalf("user config normalization incomplete: configRows=%d", configRows)
				}
				if st.isMySQL() {
					// The production migration removes legacy credential columns;
					// querying them after startup would itself be an invalid test.
					for _, column := range []string{"git_token", "codebuddy_api_key", "codex_api_key", "claude_code_api_key"} {
						if st.DB.Migrator().HasColumn("users", column) {
							t.Fatalf("legacy credential column still exists: users.%s", column)
						}
					}
				}
			}
			public := domain.PublicUser(got)
			raw, _ := json.Marshal(public)
			if strings.Contains(string(raw), "secret") || !public.CodexAPIKeyConfigured || !public.ClaudeCodeAPIKeyConfigured {
				t.Fatal("public user leaked credentials or lost configured state")
			}
			empty := ""
			noModels := []domain.CLIModel{}
			_, err = st.UpdateUserCredentials(u.ID, UserCredentialUpdate{CodexAPIKeyClear: true, ClaudeCodeAPIKeyClear: true, CodexBaseURL: &empty, ClaudeCodeBaseURL: &empty, CodexModels: &noModels, ClaudeCodeModels: &noModels})
			if err != nil {
				t.Fatal(err)
			}
			cleared, _ := st.User(u.ID)
			if cleared.CodexAPIKey != "" || cleared.ClaudeCodeAPIKey != "" || cleared.CodexBaseURL != "" || cleared.ClaudeCodeBaseURL != "" || len(cleared.CodexModels) != 0 || len(cleared.ClaudeCodeModels) != 0 || cleared.GitToken != "git-secret" {
				t.Fatal("clear operation did not isolate providers")
			}
		})
	}
}
