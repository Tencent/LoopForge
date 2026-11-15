package httpapi

import (
	"net/http"

	deployassets "agentflow/deploy"

	"github.com/gin-gonic/gin"
)

// serveBindCLIWindowsScript serves the native Windows Runner lifecycle installer.
func serveBindCLIWindowsScript(c *gin.Context) {
	script := deployassets.BindCLIWindowsScript()
	if len(script) == 0 {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", script)
}
