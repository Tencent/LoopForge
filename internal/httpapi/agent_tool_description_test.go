package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionPlatformToolsExplainArgumentEnvelopes(t *testing.T) {
	for _, session := range []*AgentToolSession{
		{Mode: AgentToolAssistant},
		{Mode: AgentToolAssistant, ExecMode: assistantExecMode{AutoWrite: true}},
		{Mode: AgentToolClarify},
		{Mode: AgentToolScheduled, ExecMode: assistantExecMode{AutoWrite: true}},
	} {
		catalog := (&agentToolGateway{}).buildCatalog(context.Background(), session)
		foundPlatform, foundWorkflow, foundSchema := false, false, false
		for _, tool := range catalog {
			if tool.Kind == "agentflow" {
				foundPlatform = true
				var schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				}
				if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
					t.Fatal(err)
				}
				for _, envelope := range []string{"path", "query", "body"} {
					if schema.Properties[envelope] != nil && !strings.Contains(tool.Description, `"`+envelope+`"`) {
						t.Errorf("%s/%s omits %s argument wrapping: %s", session.Mode, tool.Name, envelope, tool.Description)
					}
				}
				foundWorkflow = foundWorkflow || tool.UpstreamName == "workflows_list"
			}
			if tool.Name == "workbench_tool_schema" {
				foundSchema = true
				if !strings.Contains(tool.Description, `{"name":"issues_save"}`) {
					t.Errorf("schema lookup omits its required argument example: %s", tool.Description)
				}
			}
		}
		if !foundPlatform || (session.Mode != AgentToolClarify && !foundWorkflow) || (session.Mode == AgentToolAssistant && !foundSchema) {
			t.Fatalf("%s catalog missing tools under test: platform=%v workflow=%v schema=%v", session.Mode, foundPlatform, foundWorkflow, foundSchema)
		}
	}
}
