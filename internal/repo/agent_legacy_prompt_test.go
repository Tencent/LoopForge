package repo

import (
	"strings"
	"testing"
)

// Rows written before the single-prompt model (or overwritten by an older
// server) only carry instructions / taskPrompt; reading them must still yield
// the role brief.
func TestStoredLegacyAgentReadsWithPrompt(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	payload := `{"id":"legacy-role","name":"备案开发工程师","version":2,"projectId":"ba","instructions":"",` +
		`"taskPrompt":{"role":"备案开发工程师","objective":"完成验收范围内的备案代码变更。","actions":["复现问题","最小变更"]},` +
		`"rules":["engineering"],"skills":[],"mcpServers":[],"loop":{"enabled":false},"verification":[]}`
	if err := st.DB.Create(&agentRow{ID: "legacy-role", Name: "备案开发工程师", Payload: []byte(payload)}).Error; err != nil {
		t.Fatal(err)
	}
	want := "完成验收范围内的备案代码变更。\n\n### 执行步骤\n\n1. 复现问题\n2. 最小变更"
	got, ok := st.Agent("legacy-role")
	if !ok || got.Prompt != want {
		t.Fatalf("legacy row read as %q (ok=%v)", got.Prompt, ok)
	}
	found := false
	for _, agent := range st.Agents() {
		found = found || (agent.ID == "legacy-role" && agent.Prompt == want)
	}
	if !found {
		t.Fatal("agent list lost the legacy brief")
	}
	if err := st.PutAgent(got); err != nil {
		t.Fatal(err)
	}
	var row agentRow
	if err := st.DB.First(&row, "id = ?", "legacy-role").Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(row.Payload), `"prompt"`) || strings.Contains(string(row.Payload), `"taskPrompt"`) {
		t.Fatalf("saving should persist the folded prompt: %s", row.Payload)
	}
}
