package httpapi

import (
	"net/http"

	"agentflow/internal/domain"
	"agentflow/internal/forge"
	"agentflow/internal/gitwork"

	"github.com/gin-gonic/gin"
)

// A Forge binding always records both addresses. The user supplies the SSH
// address instead of assuming the port or Git user from the HTTPS address.
func (h *catalogHandlers) saveForgeRepository(c *gin.Context, v domain.Repository, primary, alternate string) {
	webURL, cloneURL := v.WebURL, v.CloneURL
	if primary != "" {
		if gitwork.IsSSHReference(primary) {
			cloneURL = primary
		} else {
			webURL = primary
		}
	}
	r, remote, err := forge.BindRepository(webURL, cloneURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Do not silently replace conflicting identity fields or drop credentials
	// embedded in a secondary URL.
	for _, raw := range []string{v.WebURL, v.CloneURL, alternate} {
		if raw == "" {
			continue
		}
		if gitwork.IsSSHReference(raw) {
			if other, err := gitwork.ParseSSHRemote(raw); err == nil && other.URL == remote.URL {
				continue
			}
		} else if other, ok := domain.ParseForgeRepositoryURL(raw); ok && other == r {
			continue
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "仓库地址必须指向同一个 Forgejo 仓库"})
		return
	}
	v.GongfengProjectID, v.WebURL, v.CloneURL = r.Project(), r.WebURL(), remote.URL
	v.LocalPath = ""
	if v.Name == "" {
		v.Name = r.Name
	}
	if v.DefaultBranch == "" {
		v.DefaultBranch = "main"
	}
	h.saveRepository(c, v)
}
