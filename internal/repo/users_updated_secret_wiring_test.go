package repo

import (
	"agentflow/internal/domain"
	"testing"
)

// The unit test for updatedSecret covers the helper itself; these cases pin
// how UpdateUserCredentials wires each stored secret to its own form value and
// clear flag, so a swapped argument or a dropped "keep stored" path fails.

func seedSecretWiringUser(t *testing.T, st *Store) domain.User {
	t.Helper()
	if err := st.PutUser(domain.User{LoginName: "secret-wiring"}); err != nil {
		t.Fatal(err)
	}
	user, ok := st.UserByLoginName("secret-wiring")
	if !ok {
		t.Fatal("missing user")
	}
	base := "https://gateway.example/v1"
	if _, err := st.UpdateUserCredentials(user.ID, UserCredentialUpdate{
		CodeBuddyAPIKey:  "cb-old",
		CodexAPIKey:      "codex-old",
		ClaudeCodeAPIKey: "claude-old",
		AgentCLICredentials: map[string]AgentCLICredentialUpdate{
			"opencode": {APIKey: "oc-old", BaseURL: &base},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return user
}

type storedSecrets struct {
	codeBuddy, codex, claude, opencode string
}

func readStoredSecrets(t *testing.T, st *Store, id int64) storedSecrets {
	t.Helper()
	got, ok := st.User(id)
	if !ok {
		t.Fatal("missing saved user")
	}
	return storedSecrets{
		codeBuddy: got.CodeBuddyAPIKey,
		codex:     got.CodexAPIKey,
		claude:    got.ClaudeCodeAPIKey,
		opencode:  got.AgentCLICredentials["opencode"].APIKey,
	}
}

func TestUpdateUserCredentialsSecretWiring(t *testing.T) {
	seeded := storedSecrets{codeBuddy: "cb-old", codex: "codex-old", claude: "claude-old", opencode: "oc-old"}
	newBase := "https://other.example/v1"
	cases := []struct {
		name   string
		update UserCredentialUpdate
		want   storedSecrets
	}{
		{
			name: "unrelated update keeps every stored secret",
			update: UserCredentialUpdate{
				AgentCLICredentials: map[string]AgentCLICredentialUpdate{"opencode": {BaseURL: &newBase}},
			},
			want: seeded,
		},
		{
			name:   "blank values keep every stored secret",
			update: UserCredentialUpdate{CodeBuddyAPIKey: "  ", CodexAPIKey: "\t", ClaudeCodeAPIKey: " \n", AgentCLICredentials: map[string]AgentCLICredentialUpdate{"opencode": {APIKey: " "}}},
			want:   seeded,
		},
		{
			name:   "clear CodeBuddy only",
			update: UserCredentialUpdate{CodeBuddyAPIKeyClear: true},
			want:   storedSecrets{codeBuddy: "", codex: "codex-old", claude: "claude-old", opencode: "oc-old"},
		},
		{
			name:   "clear Codex only",
			update: UserCredentialUpdate{CodexAPIKeyClear: true},
			want:   storedSecrets{codeBuddy: "cb-old", codex: "", claude: "claude-old", opencode: "oc-old"},
		},
		{
			name:   "clear Claude Code only",
			update: UserCredentialUpdate{ClaudeCodeAPIKeyClear: true},
			want:   storedSecrets{codeBuddy: "cb-old", codex: "codex-old", claude: "", opencode: "oc-old"},
		},
		{
			name:   "replace CodeBuddy only, trimmed",
			update: UserCredentialUpdate{CodeBuddyAPIKey: "  cb-new  "},
			want:   storedSecrets{codeBuddy: "cb-new", codex: "codex-old", claude: "claude-old", opencode: "oc-old"},
		},
		{
			name:   "replace Codex only, trimmed",
			update: UserCredentialUpdate{CodexAPIKey: " codex-new "},
			want:   storedSecrets{codeBuddy: "cb-old", codex: "codex-new", claude: "claude-old", opencode: "oc-old"},
		},
		{
			name:   "replace Claude Code only, trimmed",
			update: UserCredentialUpdate{ClaudeCodeAPIKey: " claude-new "},
			want:   storedSecrets{codeBuddy: "cb-old", codex: "codex-old", claude: "claude-new", opencode: "oc-old"},
		},
		{
			name:   "clear wins over a new value on the same field",
			update: UserCredentialUpdate{CodexAPIKey: "codex-new", CodexAPIKeyClear: true, ClaudeCodeAPIKey: "claude-new"},
			want:   storedSecrets{codeBuddy: "cb-old", codex: "", claude: "claude-new", opencode: "oc-old"},
		},
		{
			name: "clear agent CLI key wins over a new value",
			update: UserCredentialUpdate{
				AgentCLICredentials: map[string]AgentCLICredentialUpdate{"opencode": {APIKey: "oc-new", APIKeyClear: true}},
			},
			want: storedSecrets{codeBuddy: "cb-old", codex: "codex-old", claude: "claude-old", opencode: ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			user := seedSecretWiringUser(t, st)
			if got := readStoredSecrets(t, st, user.ID); got != seeded {
				t.Fatalf("seeded secrets = %+v, want %+v", got, seeded)
			}
			updated, err := st.UpdateUserCredentials(user.ID, tc.update)
			if err != nil {
				t.Fatal(err)
			}
			returned := storedSecrets{
				codeBuddy: updated.CodeBuddyAPIKey,
				codex:     updated.CodexAPIKey,
				claude:    updated.ClaudeCodeAPIKey,
				opencode:  updated.AgentCLICredentials["opencode"].APIKey,
			}
			if returned != tc.want {
				t.Fatalf("returned secrets = %+v, want %+v", returned, tc.want)
			}
			if got := readStoredSecrets(t, st, user.ID); got != tc.want {
				t.Fatalf("stored secrets = %+v, want %+v", got, tc.want)
			}
		})
	}
}
