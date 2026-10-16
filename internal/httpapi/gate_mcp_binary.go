package httpapi

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

var gateMCPBinaryMu sync.Mutex

// serveGateMCPBinary distributes the platform helper used by Agent CLIs to
// submit terminal node decisions. It mirrors runner binary distribution so
// installed execution machines need neither Go nor Python.
func serveGateMCPBinary(c *gin.Context) {
	targetOS := sanitizePlatformParam(c.Query("os"))
	targetArch := sanitizePlatformParam(c.Query("arch"))
	if targetOS == "" {
		targetOS = "darwin"
	}
	if targetArch == "" {
		targetArch = "amd64"
	}
	if targetOS != "darwin" && targetOS != "linux" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported os %q (darwin, linux)", targetOS)})
		return
	}
	if targetArch != "amd64" && targetArch != "arm64" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported arch %q (amd64, arm64)", targetArch)})
		return
	}
	if pattern := strings.TrimSpace(os.Getenv("AGENTFLOW_GATE_MCP_BINARY")); pattern != "" {
		path := strings.ReplaceAll(strings.ReplaceAll(pattern, "{os}", targetOS), "{arch}", targetArch)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() == 0 {
			c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("gate MCP binary %s/%s is not deployed", targetOS, targetArch)})
			return
		}
		serveGateMCPFile(c, path)
		return
	}

	gateMCPBinaryMu.Lock()
	defer gateMCPBinaryMu.Unlock()
	root := runnerSourceRoot()
	if root == "" || !dirExists(filepath.Join(root, "cmd", "agentflow-gate-mcp")) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "此平台未配置 Gate MCP 二进制分发：请设置 AGENTFLOW_GATE_MCP_BINARY（预编译产物）或 AGENTFLOW_SOURCE_ROOT（源码目录）后重启服务"})
		return
	}
	// Keep the download cache separate from runtime materialization: both
	// paths may build concurrently and use different in-process locks.
	sourceHash, err := runnerSourceDigest(root)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("计算 Gate MCP 源码摘要失败：%v", err)})
		return
	}
	cache := filepath.Join(os.TempDir(), fmt.Sprintf("agentflow-gate-mcp-download-%s-%s-%s", targetOS, targetArch, sourceHash[:16]))
	if info, err := os.Stat(cache); err != nil || info.IsDir() || info.Size() == 0 {
		tmp, err := os.CreateTemp(os.TempDir(), ".agentflow-gate-mcp-build-*")
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("创建 Gate MCP 构建文件失败：%v", err)})
			return
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		build := exec.Command("go", "build", "-o", tmp.Name(), "./cmd/agentflow-gate-mcp")
		build.Dir = root
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+targetOS, "GOARCH="+targetArch)
		out, err := build.CombinedOutput()
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("构建 Gate MCP 二进制失败：%v：%s", err, clipBuildOutput(out))})
			return
		}
		if err := os.Rename(tmp.Name(), cache); err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("保存 Gate MCP 二进制失败：%v", err)})
			return
		}
		log.Printf("built gate MCP binary %s/%s at %s", targetOS, targetArch, cache)
	}
	serveGateMCPFile(c, cache)
}

func serveGateMCPFile(c *gin.Context, path string) {
	if c.Query("metadata") == "1" {
		serveBinaryMetadata(c, path)
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", `attachment; filename="agentflow-gate-mcp"`)
	http.ServeFile(c.Writer, c.Request, path)
}
