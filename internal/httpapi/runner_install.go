package httpapi

import (
	"net/http"

	deployassets "agentflow/deploy"

	"github.com/gin-gonic/gin"
)

// serveBindCLIScript 分发 macOS/Linux 执行机器接入脚本。
func serveBindCLIScript(c *gin.Context) {
	serveRunnerInstallScript(c, deployassets.BindCLIScript(), "text/x-shellscript; charset=utf-8")
}

// serveBindCLIWindowsScript 分发无需源码目录的 Windows PowerShell 接入脚本。
func serveBindCLIWindowsScript(c *gin.Context) {
	serveRunnerInstallScript(c, deployassets.BindCLIWindowsScript(), "text/plain; charset=utf-8")
}

// serveRunnerInstallScript 禁止缓存安装脚本，使接入命令随平台版本更新。
func serveRunnerInstallScript(c *gin.Context, script []byte, contentType string) {
	if len(script) == 0 {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, contentType, script)
}
