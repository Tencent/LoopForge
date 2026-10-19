package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// The server distributes the runner binary so bind-cli.sh needs no local Go
// toolchain or module registry: the copied command simply downloads the
// build for the target OS/arch. Source of the binary, in priority order:
//
//  1. AGENTFLOW_RUNNER_BINARY — a prebuilt binary path. The path may contain
//     {os} and {arch} placeholders for production multi-platform bundles.
//  2. AGENTFLOW_SOURCE_ROOT, or the server working directory — a source
//     checkout; the handler builds ./cmd/agentflow-runner on demand with
//     GOOS/GOARCH from the request and caches the artifact.
var runnerBinaryMu sync.Mutex

// 路由: g.GET("/deploy/agentflow-runner", serveRunnerBinary)
//
//	g.GET("/runners/agentflow-runner", serveRunnerBinary)
func serveRunnerBinary(c *gin.Context) {
	targetOS := sanitizePlatformParam(c.Query("os"))
	targetArch := sanitizePlatformParam(c.Query("arch"))
	if targetOS == "" {
		targetOS = "darwin"
	}
	if targetArch == "" {
		targetArch = "amd64"
	}
	switch targetOS {
	case "darwin", "linux":
	default:
		c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported os %q (darwin, linux)", targetOS)})
		return
	}
	switch targetArch {
	case "amd64", "arm64":
	default:
		c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported arch %q (amd64, arm64)", targetArch)})
		return
	}

	if pattern := strings.TrimSpace(os.Getenv("AGENTFLOW_RUNNER_BINARY")); pattern != "" {
		path := strings.ReplaceAll(strings.ReplaceAll(pattern, "{os}", targetOS), "{arch}", targetArch)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() == 0 {
			c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("runner binary %s/%s is not deployed", targetOS, targetArch)})
			return
		}
		serveRunnerArtifact(c, path)
		return
	}

	runnerBinaryMu.Lock()
	defer runnerBinaryMu.Unlock()
	root := runnerSourceRoot()
	if root == "" {
		c.JSON(http.StatusNotFound, map[string]string{"error": "此平台未配置 runner 二进制分发：请设置 AGENTFLOW_RUNNER_BINARY（预编译产物）或 AGENTFLOW_SOURCE_ROOT（源码目录）后重启服务，或在执行机上使用 bind-cli.sh --from <二进制> 安装"})
		return
	}
	sourceHash, err := runnerSourceDigest(root)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("计算 runner 源码摘要失败：%v", err)})
		return
	}
	cache := filepath.Join(os.TempDir(), fmt.Sprintf("agentflow-runner-%s-%s-%s", targetOS, targetArch, sourceHash[:16]))
	if info, err := os.Stat(cache); err != nil || info.IsDir() || info.Size() == 0 {
		tmp, err := os.CreateTemp(os.TempDir(), ".agentflow-runner-build-*")
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("创建 runner 构建文件失败：%v", err)})
			return
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		build := exec.Command("go", "build", "-o", tmp.Name(), "./cmd/agentflow-runner")
		build.Dir = root
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+targetOS, "GOARCH="+targetArch)
		out, err := build.CombinedOutput()
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("构建 runner 二进制失败：%v：%s", err, clipBuildOutput(out))})
			return
		}
		if err := os.Rename(tmp.Name(), cache); err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("保存 runner 二进制失败：%v", err)})
			return
		}
		log.Printf("built runner binary %s/%s at %s", targetOS, targetArch, cache)
	}
	serveRunnerArtifact(c, cache)
}

func serveRunnerArtifact(c *gin.Context, path string) {
	if c.Query("metadata") == "1" {
		serveBinaryMetadata(c, path)
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", `attachment; filename="agentflow-runner"`)
	http.ServeFile(c.Writer, c.Request, path)
}

func serveBinaryMetadata(c *gin.Context, path string) {
	file, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "open binary: " + err.Error()})
		return
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "hash binary: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"sha256": hex.EncodeToString(hash.Sum(nil))})
}

func sanitizePlatformParam(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) > 16 {
		return ""
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return v
}

func runnerSourceRoot() string {
	if root := strings.TrimSpace(os.Getenv("AGENTFLOW_SOURCE_ROOT")); root != "" {
		if dir := filepath.Join(root, "cmd", "agentflow-runner"); dirExists(dir) {
			return root
		}
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if dirExists(filepath.Join(cwd, "cmd", "agentflow-runner")) {
		return cwd
	}
	return ""
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// A content-based cache key avoids serving an old build when a deployment
// restores source files with preserved/older mtimes or removes a source file.
func runnerSourceDigest(root string) (string, error) {
	hash := sha256.New()
	io.WriteString(hash, runtime.Version())
	paths := []string{"go.mod", "go.sum"}
	for _, dir := range []string{"cmd/agentflow-runner", "cmd/agentflow-gate-mcp", "internal"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, entry os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Mode().IsRegular() && !strings.HasSuffix(path, "_test.go") {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				paths = append(paths, rel)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	for _, rel := range paths {
		io.WriteString(hash, rel)
		io.WriteString(hash, "\x00")
		file, err := os.Open(filepath.Join(root, rel))
		if err != nil {
			return "", err
		}
		_, err = io.Copy(hash, file)
		file.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func clipBuildOutput(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 400 {
		s = s[len(s)-400:]
	}
	return s
}
