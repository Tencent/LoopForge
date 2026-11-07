package runtime

import (
	"agentflow/internal/domain"
	"testing"
)

func TestLocalDriverRunsInDockerWhenIsolated(t *testing.T) {
	custom := domain.DockerfileDefinition{ID: "custom", Content: "FROM alpine\n"}
	t.Setenv("AGENTFLOW_LOCAL_SANDBOX", "")
	if p, _ := isolateLocalDriver(domain.SandboxProfile{Driver: "local"}, domain.DockerfileDefinition{}); p.Driver != "local" {
		t.Fatalf("native installs keep local execution: %q", p.Driver)
	}
	t.Setenv("AGENTFLOW_LOCAL_SANDBOX", "docker")
	for _, driver := range []string{"", "local"} {
		p, d := isolateLocalDriver(domain.SandboxProfile{Driver: driver}, domain.DockerfileDefinition{})
		if p.Driver != "docker" || d.Content != IsolatedDockerfile {
			t.Fatalf("driver %q: %+v %+v", driver, p, d)
		}
	}
	if _, d := isolateLocalDriver(domain.SandboxProfile{}, custom); d.ID != "custom" || d.Content != custom.Content {
		t.Fatal("a configured Dockerfile is kept")
	}
	if p, _ := isolateLocalDriver(domain.SandboxProfile{Driver: "bwrap"}, domain.DockerfileDefinition{}); p.Driver != "bwrap" {
		t.Fatal("explicit isolation drivers are unchanged")
	}
}
