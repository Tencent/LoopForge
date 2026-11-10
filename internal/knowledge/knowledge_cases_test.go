package knowledge

import "testing"

func TestAgentToolsAreReadOnly(t *testing.T) {
	for _, name := range ReadOnlyTools {
		if !IsReadOnlyTool(name) {
			t.Fatal(name)
		}
	}
	if IsReadOnlyTool(ToolWrite) {
		t.Fatal("write tool exposed")
	}
}
