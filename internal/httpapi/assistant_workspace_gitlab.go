package httpapi

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// Validate every writable GitLab destination before any repository is pushed.
// Conversation delivery is attributed to the requester, never the project owner.
func workspaceGitLabProjects(repositories []domain.RepositoryContext, requester domain.User) (map[string]string, bool, error) {
	projects := map[string]string{}
	needsGitToken := len(repositories) == 0
	for _, rc := range repositories {
		if rc.ReadOnly {
			continue
		}
		if rc.Provider != "gitlab" {
			if strings.HasPrefix(rc.GongfengProjectID, "gitlab:") {
				return nil, false, fmt.Errorf("GitLab repository provider does not match its configured project")
			}
			needsGitToken = true
			continue
		}
		if strings.TrimSpace(requester.GitLabToken) == "" {
			return nil, false, fmt.Errorf("Configure your own GitLab Token in personal credentials before submitting this workspace")
		}
		info, ok := domain.ParseGitLabRepositoryURL(rc.CloneURL, requester.GitLabBaseURL)
		if !ok || info.Project != rc.GongfengProjectID {
			return nil, false, fmt.Errorf("GitLab repository does not match your configured instance and project")
		}
		projects[rc.RepositoryID+"\x00"+rc.WorkBranch] = info.Project
	}
	return projects, needsGitToken, nil
}
