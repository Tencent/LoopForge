package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentDefinitionFoldsLegacyBriefIntoPrompt(t *testing.T) {
	raw := `{"id":"summary","name":"交付汇总","instructions":"只汇总已验证产物；不修改代码或上游报告。",
		"taskPrompt":{"role":"交付汇总（SUMMARY 阶段）","objective":"汇总交付结果。","context":"只读流程状态。",
		"inputs":["上游结论：{{upstream}}"],"actions":["汇总产物"," ","列出风险"],"deliverables":["summary.md"],
		"acceptanceCriteria":["不夸大结论"],"constraints":["不重新实现上游产物","不修改代码"],"retryInstructions":"只补缺失项。"},
		"rules":["engineering"]}`
	var agent AgentDefinition
	if err := json.Unmarshal([]byte(raw), &agent); err != nil {
		t.Fatal(err)
	}
	want := "你是交付汇总（SUMMARY 阶段）。\n\n只汇总已验证产物；不修改代码或上游报告。\n\n汇总交付结果。" +
		"\n\n### 工作上下文\n\n只读流程状态。\n\n### 输入\n\n- 上游结论：{{upstream}}" +
		"\n\n### 执行步骤\n\n1. 汇总产物\n2. 列出风险\n\n### 交付物要求\n\n- summary.md" +
		"\n\n### 完成标准\n\n- 不夸大结论\n\n### 禁止\n\n- 不重新实现上游产物\n\n### 失败或被打回时\n\n只补缺失项。"
	if agent.Prompt != want {
		t.Fatalf("legacy brief folded as\n%q\nwant\n%q", agent.Prompt, want)
	}
	if agent.ID != "summary" || len(agent.Rules) != 1 {
		t.Fatalf("other fields lost: %+v", agent)
	}
	out, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"taskPrompt"`) || strings.Contains(string(out), `"instructions"`) || !strings.Contains(string(out), `"prompt"`) {
		t.Fatalf("re-encoded role should carry only prompt: %s", out)
	}
}

func TestAgentDefinitionLegacyRoleLine(t *testing.T) {
	cases := map[string]string{
		`{"name":"前端交付工程师","instructions":"不改业务代码。","taskPrompt":{"role":"前端交付工程师","objective":"交付 MR。"}}`: "不改业务代码。\n\n交付 MR。",
		`{"name":"评审","instructions":"你是资深评审员。","taskPrompt":{"role":"代码评审员","objective":"写报告。"}}`:         "你是资深评审员。\n\n写报告。",
		`{"name":"实现","instructions":"[[mock:summary=ok]]"}`:                                               "[[mock:summary=ok]]",
		`{"name":"实现","instructions":"按需实现。","taskPrompt":null}`:                                           "按需实现。",
		`{"name":"实现","taskPrompt":"直接完成任务。"}`:                                                             "直接完成任务。",
		`{"name":"空","taskPrompt":{}}`: "",
	}
	for raw, want := range cases {
		var agent AgentDefinition
		if err := json.Unmarshal([]byte(raw), &agent); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if agent.Prompt != want {
			t.Fatalf("%s: prompt %q, want %q", raw, agent.Prompt, want)
		}
	}
}

func TestAgentDefinitionStoredPromptWins(t *testing.T) {
	var agent AgentDefinition
	raw := `{"name":"实现","prompt":"新 prompt","instructions":"旧指令","taskPrompt":{"objective":"旧目标"}}`
	if err := json.Unmarshal([]byte(raw), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.Prompt != "新 prompt" {
		t.Fatalf("migrated prompt must not be rebuilt: %q", agent.Prompt)
	}
	agent = AgentDefinition{ID: "keep", Prompt: "已有"}
	if err := json.Unmarshal([]byte(`{"name":"更新"}`), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.ID != "keep" || agent.Name != "更新" || agent.Prompt != "已有" {
		t.Fatalf("decoding into an existing value should merge like encoding/json: %+v", agent)
	}
	if err := json.Unmarshal([]byte(`{"name":1}`), &agent); err == nil {
		t.Fatal("type errors must still surface")
	}
}
