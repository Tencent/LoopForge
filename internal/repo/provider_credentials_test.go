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
			_, err = st.UpdateUserCredentials(u.ID, UserCredentialUpdate{TaihuPAT: "tai_pat_secret", CodexAPIKey: "codex-secret", CodexBaseURL: &codexURL, ClaudeCodeAPIKey: "claude-secret", ClaudeCodeBaseURL: &claudeURL})
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
			if !ok || got.TaihuPAT != "tai_pat_secret" || got.CodexAPIKey != "codex-secret" || got.ClaudeCodeAPIKey != "claude-secret" || got.CodexBaseURL != codexURL || got.ClaudeCodeBaseURL != claudeURL {
				t.Fatal("credentials did not survive refresh and partial update")
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
			if strings.Contains(string(raw), "secret") || !public.TaihuPATConfigured || !public.CodexAPIKeyConfigured || !public.ClaudeCodeAPIKeyConfigured {
				t.Fatal("public user leaked credentials or lost configured state")
			}
			empty := ""
			_, err = st.UpdateUserCredentials(u.ID, UserCredentialUpdate{TaihuPATClear: true, CodexAPIKeyClear: true, ClaudeCodeAPIKeyClear: true, CodexBaseURL: &empty, ClaudeCodeBaseURL: &empty})
			if err != nil {
				t.Fatal(err)
			}
			cleared, _ := st.User(u.ID)
			if cleared.TaihuPAT != "" || cleared.CodexAPIKey != "" || cleared.ClaudeCodeAPIKey != "" || cleared.CodexBaseURL != "" || cleared.ClaudeCodeBaseURL != "" || cleared.GitToken != "git-secret" {
				t.Fatal("clear operation did not isolate providers")
			}
		})
	}
}
