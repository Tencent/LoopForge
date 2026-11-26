package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/opensandbox"
)

func TestOpenSandboxBackendBinding(t *testing.T) {
	makeClient := func(scope string) *opensandbox.Client {
		t.Helper()
		client, err := opensandbox.New(opensandbox.Config{
			Endpoint: "http://127.0.0.1:1337", APIKey: "opensandbox-test", RunnerURL: "https://loopforge.example.test",
			RunnerImage: "runner:test", Scope: scope, RunnerSigningKey: strings.Repeat("testing", 5),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	e := &Engine{}
	docker, kube := makeClient("docker-test"), makeClient("kube-test")
	if err := e.ConfigureOpenSandbox(DefaultOpenSandboxBackend, docker, []string{"docker-project"}); err != nil {
		t.Fatal(err)
	}
	if err := e.ConfigureOpenSandbox("kube", kube, []string{"kube-project"}); err != nil {
		t.Fatal(err)
	}
	if err := e.ConfigureOpenSandbox("duplicate", kube, []string{"docker-project"}); err == nil {
		t.Fatal("a project cannot route to multiple backends")
	}
	if e.openSandboxProjects["docker-project"] != DefaultOpenSandboxBackend || e.openSandboxProjects["kube-project"] != "kube" {
		t.Fatal("backend registration changed existing project bindings")
	}
	run := domain.Run{ExecutionContext: domain.TelemetryContext{ExecutionProviderID: "kube", ProjectID: "docker-project"}}
	if e.openSandboxForRun(run) != kube {
		t.Fatal("a run followed the current project instead of its pinned backend")
	}
	run.ExecutionContext.ExecutionProviderID = ""
	if e.openSandboxForRun(run) != docker {
		t.Fatal("a run without a provider ID did not use the original default backend")
	}
}
