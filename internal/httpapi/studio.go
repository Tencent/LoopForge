package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	htmlpkg "html"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strings"
)

var studioAssetRef = regexp.MustCompile(`((?:href|src)=")(/[^"]+\.(?:js|css))(\?v=[^"]*)?(")`)

func stampStudioIndex(static fs.FS, raw []byte) []byte {
	html := string(raw)
	html = studioAssetRef.ReplaceAllStringFunc(html, func(m string) string {
		sub := studioAssetRef.FindStringSubmatch(m)
		if len(sub) < 5 {
			return m
		}
		name := strings.TrimPrefix(sub[2], "/")
		body, err := fs.ReadFile(static, name)
		if err != nil {
			return m
		}
		sum := sha256.Sum256(body)
		return sub[1] + sub[2] + "?v=" + hex.EncodeToString(sum[:8]) + sub[4]
	})
	domain := strings.TrimSpace(os.Getenv("AGENTFLOW_APP_DOMAIN"))
	if domain == "" {
		domain = strings.TrimSpace(os.Getenv("APP_DOMAIN"))
	}
	// A meta tag, not an inline script: the CSP is script-src 'self', so an
	// inline assignment would be blocked. app.js copies it onto window.
	if domain != "" && strings.Contains(html, "</head>") {
		tag := fmt.Sprintf(`<meta name="agentflow-app-domain" content="%s"></head>`, htmlpkg.EscapeString(domain))
		html = strings.Replace(html, "</head>", tag, 1)
	}
	return []byte(html)
}

func studioFileServer(static fs.FS) http.Handler {
	files := http.FileServer(http.FS(static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" || r.URL.Path == "/vscode.html" {
			// 每次请求实时读取并重新计算版本哈希，
			// 保证前端文件修改后无需重启服务即可生效。
			name := "index.html"
			if r.URL.Path == "/vscode.html" {
				name = "vscode.html"
			}
			raw, err := fs.ReadFile(static, name)
			if err != nil {
				files.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			// 显式写状态码：gin 的 NoRoute 预设 404，隐式 WriteHeader 会沿用。
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(stampStudioIndex(static, raw))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/extensions/agentflow-workspace/") ||
			r.URL.Path == "/vscode.js" || r.URL.Path == "/vscode.css" {
			w.Header().Set("Cache-Control", "no-store")
		}
		files.ServeHTTP(w, r)
	})
}
