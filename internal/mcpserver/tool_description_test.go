package mcpserver

import (
	"strings"
	"testing"

	"agentflow/internal/application"
)

func TestPublishedToolDescriptionsExplainArgumentEnvelopes(t *testing.T) {
	server := New(nil)
	operations := map[string]application.Operation{}
	for _, op := range application.PublicOperations() {
		operations[op.Name] = op
	}
	for _, tool := range server.toolList() {
		name := tool["name"].(string)
		description := tool["description"].(string)
		properties := contractObject(ToolInputSchema(operations[name])["properties"])
		_, hints, ok := strings.Cut(description, "MCP arguments: ")
		if !ok {
			t.Fatalf("%s omits MCP argument instructions: %s", name, description)
		}
		for _, envelope := range []string{"path", "query", "body"} {
			if strings.Contains(hints, `"`+envelope+`"`) != (properties[envelope] != nil) {
				t.Errorf("%s instructions disagree with the %s envelope schema: %s", name, envelope, description)
			}
		}
	}
	args := map[string]any{"query": map[string]any{"projectId": "welcome"}}
	if err := ValidateToolInput(operations["workflows_list"], args); err != nil {
		t.Fatalf("documented query envelope rejected: %v", err)
	}
	if err := ValidateToolInput(operations["workflows_list"], map[string]any{"projectId": "welcome"}); err == nil {
		t.Fatal("top-level endpoint field unexpectedly accepted")
	}
}
