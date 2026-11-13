package httpapi

import (
	"crypto/sha256"
	"fmt"
	"net/http"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"

	"github.com/gin-gonic/gin"
)

func (h *catalogHandlers) saveGitLabRepository(c *gin.Context, repository domain.Repository) {
	c.Header("Cache-Control", "no-store")
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录 LoopForge"})
		return
	}
	if _, ok := h.repo.Project(repository.ProjectID); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project not found"})
		return
	}
	if repository.GitLabProjectID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请从 GitLab 搜索并选择仓库"})
		return
	}
	credential := h.repo.GitLabCredential(current.ID)
	if credential.Token == "" {
		gitLabTokenRequired(c)
		return
	}
	// The numeric GitLab project ID only has meaning on its original instance.
	if repository.GitLabURL != "" && repository.GitLabURL != credential.BaseURL {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择当前 GitLab 实例中的仓库"})
		return
	}
	if existing, ok := h.repo.Repository(repository.ID); ok && existing.Provider == domain.RepositoryProviderGitLab {
		scope := gitwork.HTTPSCredential{BaseURL: credential.BaseURL}
		if (existing.GitLabURL != "" && existing.GitLabURL != credential.BaseURL) || !scope.Matches(existing.CloneURL) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请配置此仓库所属的 GitLab 实例后再编辑"})
			return
		}
	}
	project, err := gitLabClient(credential.BaseURL).ReadableProject(c.Request.Context(), credential.Token, repository.GitLabProjectID)
	if err != nil {
		gitLabError(c, err)
		return
	}
	if repository.ID == "" {
		key := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", repository.ProjectID, credential.BaseURL, project.ID)))
		repository.ID = fmt.Sprintf("repo-gitlab-%x", key[:16])
	}
	repository.GitLabURL = credential.BaseURL
	repository.CloneURL, repository.WebURL = project.CloneURL, project.WebURL
	repository.GongfengProjectID, repository.LocalPath = "", ""
	if repository.Name == "" {
		repository.Name = project.Name
	}
	if repository.DefaultBranch == "" {
		repository.DefaultBranch = project.DefaultBranch
	}
	h.saveRepository(c, repository)
}
