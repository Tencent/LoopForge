package repo

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// TestCursorCatalogRejectsBadIDs covers the shared model ID check on both
// the row ID and each effort target, and drops a default the row lacks.
func TestCursorCatalogRejectsBadIDs(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutUser(domain.User{LoginName: "cursor-validate"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("cursor-validate")
	long := strings.Repeat("a", maxCLIModelIDLen+1)
	for name, c := range map[string]struct {
		model domain.CLIModel
		want  string
	}{
		"bad row id":   {domain.CLIModel{ID: "wid get"}, "不能包含空格"},
		"empty target": {domain.CLIModel{ID: "widget", Efforts: map[string]string{"low": "  "}}, "模型 ID 不能为空"},
		"long target":  {domain.CLIModel{ID: "widget", Efforts: map[string]string{"low": long}}, "长度不能超过"},
		"empty row id": {domain.CLIModel{ID: " ", Name: "x"}, "模型 ID 不能为空"},
		"too long row": {domain.CLIModel{ID: long}, "长度不能超过"},
	} {
		_, err := st.UpdateUserCredentials(user.ID, UserCredentialUpdate{AgentCLICredentials: map[string]AgentCLICredentialUpdate{
			"cursor": {APIKey: "crsr_x", Models: &[]domain.CLIModel{c.model}},
		}})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	models := []domain.CLIModel{{ID: "widget", DefaultEffort: "max", Efforts: map[string]string{" low ": " widget-low "}}}
	if _, err := st.UpdateUserCredentials(user.ID, UserCredentialUpdate{AgentCLICredentials: map[string]AgentCLICredentialUpdate{
		"cursor": {APIKey: "crsr_x", Models: &models},
	}}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.User(user.ID)
	saved := got.AgentCLICredentials["cursor"].Models
	if len(saved) != 1 || saved[0].DefaultEffort != "" || len(saved[0].Efforts) != 1 || saved[0].Efforts["low"] != "widget-low" {
		t.Fatalf("saved = %+v", saved)
	}
}
