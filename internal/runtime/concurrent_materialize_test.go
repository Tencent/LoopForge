package runtime

import (
	"agentflow/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestConcurrentSameAgentRetainsPreviousConfiguration(t *testing.T) {
	root := t.TempDir()
	ex := domain.ExecutorDefinition{ID: "codebuddy", Kind: "codebuddy"}
	agent := domain.AgentDefinition{ID: "dev", Name: "开发", Instructions: "implement"}
	first, err := materializeInvocation(root, "one", ex, agent, domain.AgentAssetBundle{}, InvocationOptions{Concurrent: true})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, first.AgentRoot, "keep.txt")
	if err = os.WriteFile(marker, []byte("first invocation"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := materializeInvocation(root, "two", ex, agent, domain.AgentAssetBundle{}, InvocationOptions{Concurrent: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.AgentRoot == second.AgentRoot {
		t.Fatal("concurrent invocations share config")
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("second invocation reset first config", err)
	}
}
