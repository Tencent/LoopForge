package runtime

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestPrepareMCPServersRebasesForExecutionMachines(t *testing.T) {
	servers := []domain.MCPServerDefinition{
		{ID: domain.KnowledgeMCPServerID, URL: "http://127.0.0.1:8080/runners/knowledge/mcp"},
		{ID: domain.PlatformMCPServerID, URL: "/mcp"},
		{ID: "third-party", URL: "http://kb.example/mcp"},
	}
	got, warnings, err := PrepareMCPServers(domain.AgentAssetBundle{MCPServers: servers}, MCPTarget{ServerURL: "https://loop.example/"})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("err = %v, warnings = %v", err, warnings)
	}
	want := []string{"https://loop.example/runners/knowledge/mcp", "https://loop.example/mcp", "http://kb.example/mcp"}
	for i := range want {
		if got.MCPServers[i].URL != want[i] {
			t.Fatalf("server %d URL = %q, want %q", i, got.MCPServers[i].URL, want[i])
		}
	}
	if servers[0].URL != "http://127.0.0.1:8080/runners/knowledge/mcp" {
		t.Fatal("prepare mutated the caller's slice")
	}
}

func TestPrepareMCPServersDropsUnaddressableForCloud(t *testing.T) {
	got, warnings, err := PrepareMCPServers(domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		{ID: domain.KnowledgeMCPServerID, URL: "/runners/knowledge/mcp"},
		{ID: domain.PlatformMCPServerID, URL: "http://10.0.0.1:8080/mcp"},
		{ID: "relative-third-party", URL: "/not-ours"},
	}}, MCPTarget{})
	if err != nil || len(got.MCPServers) != 2 || got.MCPServers[0].ID != domain.PlatformMCPServerID {
		t.Fatalf("servers = %#v, err = %v", got.MCPServers, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], domain.KnowledgeMCPServerID) || !strings.Contains(warnings[0], "AGENTFLOW_PLATFORM_MCP_URL") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestPrepareMCPServersGitHubCredentialPolicy(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{{ID: "github", Name: "GitHub", AuthType: "github-token", URL: "https://api.githubcopilot.com/mcp/"}}}
	if _, _, err := PrepareMCPServers(assets, MCPTarget{RequireCredentials: true}); err == nil {
		t.Fatal("agent invocation without a GitHub token must fail")
	}
	got, _, err := PrepareMCPServers(assets, MCPTarget{})
	if err != nil || got.MCPServers[0].Headers["Authorization"] != "" {
		t.Fatalf("optional bind without token = %#v, %v", got.MCPServers, err)
	}
	got, _, err = PrepareMCPServers(assets, MCPTarget{GitHTTPCredentials: testHTTPSCredentials("ghp_test"), RequireCredentials: true})
	if err != nil || got.MCPServers[0].Headers["Authorization"] != "Bearer ghp_test" {
		t.Fatalf("strict bind = %#v, %v", got.MCPServers, err)
	}
}

func TestPrepareMCPServersOptionalGitHubTokenForSSHAndCNBRuns(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		{ID: "github", Name: "GitHub", Transport: "http", AuthType: "github-token", URL: "https://api.githubcopilot.com/mcp/"},
		{ID: "kb", Name: "Knowledge", Transport: "http", URL: "https://kb.example.com/mcp"},
	}}

	t.Run("preserves SSHRepositoriesOnly backward compatibility", func(t *testing.T) {
		got, warnings, err := PrepareMCPServers(assets, MCPTarget{RequireCredentials: true, SSHRepositoriesOnly: true})
		if err != nil || len(got.MCPServers) != 1 || got.MCPServers[0].ID != "kb" {
			t.Fatalf("SSHRepositoriesOnly got=%#v err=%v", got.MCPServers, err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "github") {
			t.Fatalf("warnings = %v", warnings)
		}
	})

	t.Run("skips github-token for CNB-only RepositoryContext", func(t *testing.T) {
		got, warnings, err := PrepareMCPServers(assets, MCPTarget{
			RequireCredentials: true,
			Repositories: []domain.RepositoryContext{
				{RepositoryID: "cnb", Provider: domain.RepositoryProviderCNB, CloneURL: "https://cnb.cool/org/repo.git"},
			},
		})
		if err != nil || len(got.MCPServers) != 1 || got.MCPServers[0].ID != "kb" {
			t.Fatalf("CNB-only got=%#v err=%v", got.MCPServers, err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "github") {
			t.Fatalf("warnings = %v", warnings)
		}
	})

	t.Run("skips github-token for CNB plus SSH RepositoryContext", func(t *testing.T) {
		got, warnings, err := PrepareMCPServers(assets, MCPTarget{
			RequireCredentials: true,
			Repositories: []domain.RepositoryContext{
				{RepositoryID: "cnb", Provider: domain.RepositoryProviderCNB, CloneURL: "https://cnb.cool/org/repo.git"},
				{RepositoryID: "ssh", Provider: domain.RepositoryProviderSSH, CloneURL: "git@example.com:org/infra.git"},
			},
		})
		if err != nil || len(got.MCPServers) != 1 || got.MCPServers[0].ID != "kb" {
			t.Fatalf("CNB+SSH got=%#v err=%v", got.MCPServers, err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "github") {
			t.Fatalf("warnings = %v", warnings)
		}
	})

	t.Run("skips github-token for CNB-only and CNB plus SSH RunnerRepository", func(t *testing.T) {
		for name, repos := range map[string][]domain.RunnerRepository{
			"cnb-only": {{RepositoryID: "cnb", CloneURL: "https://cnb.cool/org/repo.git"}},
			"cnb+ssh": {
				{RepositoryID: "cnb", CloneURL: "https://cnb.cool/org/repo.git"},
				{RepositoryID: "ssh", CloneURL: "git@example.com:org/infra.git", UseSSH: true},
			},
		} {
			t.Run(name, func(t *testing.T) {
				got, warnings, err := PrepareMCPServers(assets, MCPTarget{
					RequireCredentials: true,
					RunnerRepositories: repos,
				})
				if err != nil || len(got.MCPServers) != 1 || got.MCPServers[0].ID != "kb" {
					t.Fatalf("%s got=%#v err=%v", name, got.MCPServers, err)
				}
				if len(warnings) != 1 || !strings.Contains(warnings[0], "github") {
					t.Fatalf("%s warnings = %v", name, warnings)
				}
			})
		}
	})

	t.Run("binds github-token when GitToken is provided even on CNB-only", func(t *testing.T) {
		got, warnings, err := PrepareMCPServers(assets, MCPTarget{
			GitHTTPCredentials: map[string]domain.GitHTTPExecutionCredential{"github.com": {Token: "ghp_test"}},
			RequireCredentials: true,
			Repositories: []domain.RepositoryContext{
				{RepositoryID: "cnb", Provider: domain.RepositoryProviderCNB, CloneURL: "https://cnb.cool/org/repo.git"},
			},
		})
		if err != nil || len(warnings) != 0 || len(got.MCPServers) != 2 || got.MCPServers[0].Headers["Authorization"] != "Bearer ghp_test" {
			t.Fatalf("CNB with GitToken got=%#v warnings=%v err=%v", got.MCPServers, warnings, err)
		}
	})

	t.Run("errors when run has 0 repositories or includes GitHub", func(t *testing.T) {
		cases := []struct {
			name   string
			target MCPTarget
		}{
			{
				name:   "0 repositories",
				target: MCPTarget{RequireCredentials: true},
			},
			{
				name: "CNB plus GitHub RepositoryContext",
				target: MCPTarget{
					RequireCredentials: true,
					Repositories: []domain.RepositoryContext{
						{RepositoryID: "cnb", Provider: domain.RepositoryProviderCNB, CloneURL: "https://cnb.cool/org/repo.git"},
						{RepositoryID: "gh", Provider: "github", CloneURL: "https://github.com/org/repo.git"},
					},
				},
			},
			{
				name: "CNB-SSH plus GitHub RunnerRepository without double count",
				target: MCPTarget{
					RequireCredentials: true,
					RunnerRepositories: []domain.RunnerRepository{
						{RepositoryID: "cnb-ssh", CloneURL: "git@cnb.cool:org/repo.git", UseSSH: true},
						{RepositoryID: "gh", CloneURL: "https://github.com/org/repo.git"},
					},
				},
			},
			{
				name: "explicit github provider with cnb URL",
				target: MCPTarget{
					RequireCredentials: true,
					Repositories: []domain.RepositoryContext{
						{RepositoryID: "gh", Provider: "github", CloneURL: "https://cnb.cool/org/repo.git"},
					},
				},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, _, err := PrepareMCPServers(assets, tc.target); err == nil {
					t.Fatalf("%s: expected GitHub token error, got nil", tc.name)
				}
			})
		}
	})
}
