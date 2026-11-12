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
	html = injectBuildCommit(html)
	return []byte(html)
}

// defaultBuildCommitFile is written by docker build from git HEAD when no
// LOOPFORGE_COMMIT build-arg was passed.
const defaultBuildCommitFile = "/app/loopforge-commit"

// buildCommit is the git SHA baked into a published image. A short label in
// the corner is enough to tell two images apart.
func buildCommit() (short, full string) {
	raw := strings.ToLower(strings.TrimSpace(commitSource()))
	if len(raw) < 7 || len(raw) > 40 {
		return "", ""
	}
	for _, r := range raw {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", ""
		}
	}
	return raw[:7], raw
}

func commitSource() string {
	if raw := strings.TrimSpace(os.Getenv("LOOPFORGE_COMMIT")); raw != "" {
		return raw
	}
	path := strings.TrimSpace(os.Getenv("LOOPFORGE_COMMIT_FILE"))
	if path == "" {
		path = defaultBuildCommitFile
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func injectBuildCommit(html string) string {
	short, full := buildCommit()
	if short == "" || !strings.Contains(html, "</body>") {
		return html
	}
	// Inline style: login and vscode pages do not share the workbench stylesheet.
	// The CSP allows style-src 'unsafe-inline' and forbids inline scripts.
	label := fmt.Sprintf(
		`<style>.loopforge-build{position:fixed;right:12px;bottom:8px;z-index:35;margin:0;font:11px/1 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;letter-spacing:.02em;color:#7c7c82;user-select:text}</style><span class="loopforge-build" title="%s">%s</span></body>`,
		htmlpkg.EscapeString("镜像版本 "+full),
		htmlpkg.EscapeString(short),
	)
	return strings.Replace(html, "</body>", label, 1)
}

func studioFileServer(static fs.FS) http.Handler {
	files := http.FileServer(http.FS(static))
	native, _ := fs.Sub(static, "native-chat")
	nativeFiles := http.StripPrefix("/chatbot/", http.FileServer(http.FS(native)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/chatbot/") {
			w.Header().Set("Cache-Control", "no-store")
			nativeFiles.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/login.html" {
			raw, err := fs.ReadFile(static, "login.html")
			if err != nil {
				files.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(injectBuildCommit(string(raw))))
			return
		}
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
