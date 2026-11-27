package httpapi

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"agentflow/internal/apperr"
	"agentflow/internal/engine"

	"github.com/gin-gonic/gin"
)

// HTTPError 是携带 HTTP 状态码的领域错误。任何领域在返回错误时都可以用它
// 表达 404/409 等精确状态，respond 会识别并采用。
type HTTPError struct {
	Status  int
	Message string
}

func (e HTTPError) Error() string { return e.Message }

// respond 渲染操作结果，是唯一的统一响应入口：
//   - err == nil → 200 {"ok": true}；
//   - 否则渲染统一 JSON 错误形态，状态码取 HTTPError 自带值，
//     apperr.Error 按 Kind 映射，ErrExecutionDisabled → 403，其余 → 400。
func respond(c *gin.Context, err error) {
	if err == nil {
		c.JSON(http.StatusOK, map[string]bool{"ok": true})
		return
	}
	var he HTTPError
	if errors.As(err, &he) {
		c.JSON(he.Status, map[string]string{"error": he.Message})
		return
	}
	if ae, ok := apperr.As(err); ok {
		body := map[string]any{"error": ae.Message}
		for key, value := range ae.Fields {
			body[key] = value
		}
		c.JSON(appErrStatus(ae.Kind), body)
		return
	}
	if errors.Is(err, engine.ErrExecutionDisabled) {
		c.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func appErrStatus(kind apperr.Kind) int {
	switch kind {
	case apperr.NotFound:
		return http.StatusNotFound
	case apperr.Conflict:
		return http.StatusConflict
	case apperr.Forbidden:
		return http.StatusForbidden
	case apperr.Unavailable:
		return http.StatusServiceUnavailable
	case apperr.Upstream:
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}

// decode binds the JSON request body, answering 400 on malformed input.
// It always caps the body at 2 MiB; a handler that accepts more calls
// decodeLimit with its own limit.
func decode(c *gin.Context, v any) bool {
	return decodeLimit(c, v, 2<<20)
}

// decodeLimit binds the JSON request body with an explicit size limit.
func decodeLimit(c *gin.Context, v any, limit int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	if err := json.NewDecoder(c.Request.Body).Decode(v); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

// decodeOptional behaves like decode but tolerates an empty body.
func decodeOptional(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	err := json.NewDecoder(c.Request.Body).Decode(v)
	if err == nil || err == io.EOF {
		return true
	}
	c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	return false
}

// ---------- 中间件（原 http 中间件的一一对应） ----------

func cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, c.Request.Host) {
				c.JSON(http.StatusForbidden, map[string]string{"error": "cross-origin request denied"})
				c.Abort()
				return
			}
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Headers", "Content-Type")
			c.Header("Access-Control-Allow-Methods", "GET,PUT,POST,PATCH,DELETE,OPTIONS")
		}
		if c.Request.Method == http.MethodOptions {
			c.Writer.WriteHeader(204)
			c.Abort()
			return
		}
		c.Next()
	}
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		path := c.Request.URL.Path
		frameOptions := "DENY"
		csp := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'; frame-src 'self' blob:"
		if strings.HasPrefix(path, "/chatbot/") {
			frameOptions = "SAMEORIGIN"
			csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'self'; frame-src 'self' blob:; form-action 'self'"
		}
		if path == "/chatbot/preview.html" {
			// srcdoc inherits the app's strict CSP and blocks generated scripts.
			// Give only the isolated preview document an inline-script policy.
			csp = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: https: http:; font-src data: https:; connect-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'; form-action 'none'"
		}
		if path == "/vscode.html" || strings.HasPrefix(path, "/vscode-web/") || strings.HasPrefix(path, "/extensions/") {
			// Real vscode-web workbench + extension-host iframe, same-origin only.
			frameOptions = "SAMEORIGIN"
			scriptSrc := "'self' 'unsafe-eval' 'wasm-unsafe-eval' blob:"
			if strings.HasPrefix(path, "/vscode-web/") {
				scriptSrc += " 'unsafe-inline'"
			}
			csp = "default-src 'self'; script-src " + scriptSrc + "; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:; child-src 'self' blob:; frame-src 'self' blob:; font-src 'self' data:; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'self'; form-action 'self'"
		}
		c.Header("X-Frame-Options", frameOptions)
		c.Header("Content-Security-Policy", csp)
		if path == "/" || strings.HasSuffix(path, ".html") {
			c.Header("Cache-Control", "no-store")
		} else if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") {
			if c.Request.URL.RawQuery != "" {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				c.Header("Cache-Control", "no-cache")
			}
		}
		c.Next()
	}
}

type gzipWriter struct {
	gin.ResponseWriter
	gz    *gzip.Writer
	plain bool
}

// body returns where the response body goes. The compressor starts with the
// first byte: a handler can still drop "Content-Encoding: gzip" before that,
// as http.FileServer does for its error pages since Go 1.23, and those bodies
// must then be sent as they are, or clients receive gzip bytes they cannot read.
func (w *gzipWriter) body() io.Writer {
	if w.gz == nil && !w.plain {
		if strings.EqualFold(w.Header().Get("Content-Encoding"), "gzip") {
			w.gz = gzip.NewWriter(w.ResponseWriter)
		} else {
			w.plain = true
		}
	}
	if w.plain {
		return w.ResponseWriter
	}
	w.Header().Del("Content-Length")
	return w.gz
}

func (w *gzipWriter) Write(p []byte) (int, error) {
	return w.body().Write(p)
}

func (w *gzipWriter) WriteString(s string) (int, error) {
	return w.body().Write([]byte(s))
}

func (w *gzipWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// knowledgeStreamPath reports whether path streams a stored document from the
// KB upstream. Those responses are proxied verbatim: gzip buffering would drop
// the exact Content-Length and delay the first byte of large files.
func knowledgeStreamPath(path string) bool {
	if !strings.HasPrefix(path, "/api/knowledge/documents/") {
		return false
	}
	return strings.HasSuffix(path, "/preview") || strings.HasSuffix(path, "/download")
}

// close ends a compressed body. A response that wrote nothing still gets an
// empty gzip stream to match its Content-Encoding header.
func (w *gzipWriter) close() {
	w.body()
	if w.gz != nil {
		_ = w.gz.Close()
	}
}

func gzipMaybe() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodHead || strings.EqualFold(c.GetHeader("Upgrade"), "websocket") || !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
			c.Next()
			return
		}
		if strings.Contains(c.Request.URL.Path, "/stream") || strings.HasPrefix(c.Request.URL.Path, "/api/otel/") || strings.Contains(c.GetHeader("Accept"), "text/event-stream") || knowledgeStreamPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		c.Header("Content-Encoding", "gzip")
		c.Header("Vary", "Accept-Encoding")
		writer := &gzipWriter{ResponseWriter: c.Writer}
		defer writer.close()
		c.Writer = writer
		c.Next()
	}
}
