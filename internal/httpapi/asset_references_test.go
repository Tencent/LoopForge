package httpapi

import (
	"slices"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestAssetReferenceHelpers(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	const projectID = "asset-ref-helpers"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutAgent(domain.AgentDefinition{ID: "asset-ref-a", Name: "Agent A", ProjectID: projectID, Rules: []string{"ar-rule", "ar-rule"}, Skills: []string{"ar-skill"}}))
	must(st.PutAgent(domain.AgentDefinition{ID: "asset-ref-b", Name: "Agent B", ProjectID: projectID, Rules: []string{"ar-skill"}}))
	must(st.PutDockerfile(domain.DockerfileDefinition{ID: "ar-df", Name: "DF", ProjectID: projectID, Content: "FROM scratch"}))
	must(st.PutEnvironment(domain.EnvironmentDefinition{ID: "ar-env", Name: "Env", ProjectID: projectID}))
	must(st.PutSandbox(domain.SandboxProfile{ID: "ar-sb-docker", Name: "SB Docker", ProjectID: projectID, Driver: "docker", DockerfileID: "ar-df"}))
	must(st.PutSandbox(domain.SandboxProfile{ID: "ar-sb-env", Name: "SB Env", ProjectID: projectID, Driver: "bwrap", EnvironmentID: "ar-env"}))
	must(st.PutWorkflow(domain.WorkflowDefinition{ID: "ar-wf", Name: "WF", ProjectID: projectID, SandboxProfileID: "ar-sb-docker"}))

	if got := agentNamesReferencing(st, "rule", "ar-rule"); !slices.Equal(got, []string{"Agent A"}) {
		t.Fatalf("rule refs = %#v, want one entry despite duplicate reference", got)
	}
	// kind 决定查 Skills 还是 Rules：Agent B 以 Rules 引用同名 ID，不应被算作技能引用。
	if got := agentNamesReferencing(st, "skill", "ar-skill"); !slices.Equal(got, []string{"Agent A"}) {
		t.Fatalf("skill refs = %#v", got)
	}
	if got := agentNamesReferencing(st, "rule", "ar-missing"); len(got) != 0 {
		t.Fatalf("missing rule refs = %#v", got)
	}

	if got := sandboxNamesReferencing(st, "dockerfile", "ar-df"); !slices.Equal(got, []string{"SB Docker"}) {
		t.Fatalf("dockerfile refs = %#v", got)
	}
	if got := sandboxNamesReferencing(st, "environment", "ar-env"); !slices.Equal(got, []string{"SB Env"}) {
		t.Fatalf("environment refs = %#v", got)
	}
	if got := sandboxNamesReferencing(st, "environment", "ar-df"); len(got) != 0 {
		t.Fatalf("kind mismatch refs = %#v", got)
	}

	if got := workflowNamesReferencingSandbox(st, "ar-sb-docker"); !slices.Equal(got, []string{"WF"}) {
		t.Fatalf("sandbox refs = %#v", got)
	}
	if got := workflowNamesReferencingSandbox(st, "ar-sb-env"); len(got) != 0 {
		t.Fatalf("unreferenced sandbox refs = %#v", got)
	}
}
