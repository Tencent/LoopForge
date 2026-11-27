package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type assetBundleHandlers struct{ *deps }

const maxAssetBundleRequest = 96 << 20

func (h *assetBundleHandlers) requireProjectManager(c *gin.Context) (string, bool) {
	user, authenticated := h.requestUser(c.Request, true)
	if !authenticated || user == nil || user.IsGuest {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
		return "", false
	}
	id := strings.TrimSpace(c.Param("id"))
	project, ok := h.repo.Project(id)
	if !ok || project.ArchivedAt != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return "", false
	}
	if _, manage := domain.ProjectCredentialAccess(project, user.ID, h.isPlatformAdmin(c.Request)); manage {
		return id, true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "资产包需要项目管理员权限"})
	return "", false
}

func (h *assetBundleHandlers) export(c *gin.Context) {
	project, allowed := h.requireProjectManager(c)
	if !allowed {
		return
	}
	var input struct {
		Assets []domain.BundleAssetRef `json:"assets"`
	}
	if !readAssetBundleJSON(c, &input, 128<<10) {
		return
	}
	bundle, err := h.repo.ExportProjectAssetBundle(project, input.Assets)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		respond(c, err)
		return
	}
	if len(raw) > maxAssetBundleRequest {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "资产包超过 96 MiB"})
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Disposition", `attachment; filename="loopforge-assets.json"`)
	c.Data(http.StatusOK, "application/json", raw)
}

func (h *assetBundleHandlers) preview(c *gin.Context) {
	h.importBundle(c, false)
}

func (h *assetBundleHandlers) importAssets(c *gin.Context) {
	h.importBundle(c, true)
}

func (h *assetBundleHandlers) importBundle(c *gin.Context, save bool) {
	project, allowed := h.requireProjectManager(c)
	if !allowed {
		return
	}
	var bundle domain.ProjectAssetBundle
	if !readAssetBundleJSON(c, &bundle, maxAssetBundleRequest) {
		return
	}
	for i := range bundle.Agents {
		if err := validateCatalogAgentExecutor(&bundle.Agents[i]); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	for _, workflow := range bundle.Workflows {
		if strings.TrimSpace(workflow.SandboxProfileID) == "" {
			workflow.SandboxProfileID = repo.DefaultSandboxProfileID
		}
		if err := engine.ValidateWorkflowGraph(workflow); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	status := http.StatusOK
	var assets []domain.BundleAssetSummary
	var err error
	if save {
		status = http.StatusCreated
		assets, err = h.repo.ImportProjectAssetBundle(project, bundle)
	} else {
		assets, err = h.repo.PreviewProjectAssetBundle(project, bundle)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	bindings := bundle.Bindings
	if save {
		bindings = repo.ImportedBundleBindings(assets, bindings)
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(status, gin.H{"assets": assets, "bindings": bindings})
}

func readAssetBundleJSON(c *gin.Context, value any, limit int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(value)
	if err == nil {
		err = decoder.Decode(new(json.RawMessage))
		if err == io.EOF {
			return true
		}
	}
	var limitErr *http.MaxBytesError
	if errors.As(err, &limitErr) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "资产包超过上传限制"})
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取资产包 JSON，请确认格式和版本"})
	}
	return false
}
