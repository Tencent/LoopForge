package httpapi

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// 平台管理 → 外部访问地址. The platform puts this address into the links it
// sends elsewhere: the account binding link and the write confirmation link of
// chat Bots, and the run detail link of notifications. Keeping it here lets an
// administrator correct it without a redeploy, which the environment variable
// AGENTFLOW_PUBLIC_URL cannot.
type platformSettingsHandlers struct {
	*deps
	// envPublicURL reads the deployment's own configuration, shown next to
	// the stored value so an administrator can tell which one is in use.
	envPublicURL func() string
}

// A value saved on the page wins over the environment, so the page can always
// fix a wrong or missing deployment configuration. The response names the
// effective source, so a shadowed environment value is never mistaken for the
// one in use.
const (
	publicURLSourceSettings = "settings"
	publicURLSourceEnv      = "env"
	publicURLSourceNone     = "none"
)

type publicURLView struct {
	// Value is the effective address; empty when neither source has one.
	Value  string `json:"value"`
	Source string `json:"source"`
	// Stored and EnvValue are the two inputs, so the page can show that a
	// saved address is shadowing the deployment's configuration.
	Stored    string `json:"stored"`
	EnvValue  string `json:"envValue"`
	UpdatedBy string `json:"updatedBy,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	// Warning describes a value that was accepted but cannot work in a link
	// sent to somebody else (a loopback address).
	Warning string `json:"warning,omitempty"`
}

func (h *platformSettingsHandlers) env() string {
	if h == nil || h.envPublicURL == nil {
		return strings.TrimRight(strings.TrimSpace(os.Getenv("AGENTFLOW_PUBLIC_URL")), "/")
	}
	return strings.TrimRight(strings.TrimSpace(h.envPublicURL()), "/")
}

func (h *platformSettingsHandlers) view() publicURLView {
	settings := h.repo.PlatformSettings()
	stored := strings.TrimRight(strings.TrimSpace(settings.PublicURL), "/")
	out := publicURLView{Stored: stored, EnvValue: h.env(), UpdatedBy: settings.UpdatedBy, Source: publicURLSourceNone}
	if !settings.UpdatedAt.IsZero() {
		out.UpdatedAt = settings.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	switch {
	case stored != "":
		out.Value, out.Source = stored, publicURLSourceSettings
	case out.EnvValue != "":
		out.Value, out.Source = out.EnvValue, publicURLSourceEnv
	}
	if _, err := domain.NormalizePublicURL(out.Value); errors.Is(err, domain.ErrPublicURLLoopback) {
		out.Warning = domain.ErrPublicURLLoopback.Error()
	}
	return out
}

// publicURL serves the address: GET shows both sources and which one is in
// use, PUT saves one, DELETE drops it so the environment applies again.
func (h *platformSettingsHandlers) publicURL(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodPut:
		h.savePublicURL(c)
		return
	case http.MethodDelete:
		if !h.platformAdminWrite(c) {
			return
		}
		h.storePublicURL(c, "")
		return
	}
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以查看平台外部访问地址"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, h.view())
}

func (h *platformSettingsHandlers) savePublicURL(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	var input struct {
		Value string `json:"value"`
	}
	if !decodeLimit(c, &input, 4<<10) {
		return
	}
	// A loopback address is accepted with a warning: it cannot work in a link
	// sent to somebody's phone, but it is what a local trial uses.
	normalized, err := domain.NormalizePublicURL(input.Value)
	if err != nil && !errors.Is(err, domain.ErrPublicURLLoopback) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.storePublicURL(c, normalized)
}

func (h *platformSettingsHandlers) storePublicURL(c *gin.Context, address string) {
	settings := h.repo.PlatformSettings()
	settings.PublicURL = address
	settings.UpdatedBy = ""
	if user, ok := h.requestUser(c.Request, true); ok {
		settings.UpdatedBy = user.LoginName
	}
	if err := h.repo.PutPlatformSettings(settings); err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, h.view())
}
