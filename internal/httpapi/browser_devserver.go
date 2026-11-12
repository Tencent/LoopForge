package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/preview"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Dev server preview: the user's own browser renders a dev server running in
// the run sandbox, under /devpreview/{runID}/{port}/ on the platform origin,
// so the page is native (input, scrolling, devtools) instead of a screencast.
// Like build previews it lives under a path prefix: markup, module imports and
// stylesheets are rewritten under it, the HTML gets the history shim and an
// intercept script, and requests a page still sends to root paths are
// redirected back under the prefix by Referer. Each request travels the
// browser operation channel ("http") to the executor hosting the sandbox.
// Workspace files the Agent opens as file:// are served the same way under
// /devpreview/{runID}/file/ (operation "file"), as they are, unrewritten.

const (
	devServerPathPrefix   = "/devpreview/"
	devServerTimeout      = 90 * time.Second
	devServerRequestLimit = 8 << 20
)

var devServerRunIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func devServerPrefix(runID string, port int) string {
	return devServerPathPrefix + runID + "/" + strconv.Itoa(port)
}

// parseDevServerPath splits /devpreview/{runID}/{port}/{rest}. rest keeps its
// escaping; slash reports whether anything, even "/", follows the port.
func parseDevServerPath(escaped string) (runID string, port int, rest string, slash, ok bool) {
	tail, found := strings.CutPrefix(escaped, devServerPathPrefix)
	if !found {
		return "", 0, "", false, false
	}
	runID, tail, found = strings.Cut(tail, "/")
	if !found || !devServerRunIDPattern.MatchString(runID) {
		return "", 0, "", false, false
	}
	rawPort, rest, slash := strings.Cut(tail, "/")
	port, err := strconv.Atoi(rawPort)
	if err != nil || port <= 0 || port > 65535 || strconv.Itoa(port) != rawPort {
		return "", 0, "", false, false
	}
	return runID, port, rest, slash, true
}

// devServerHost runs before routing, like the build preview host: it serves
// /devpreview/ and sends a dev page's root-path requests back under its prefix.
func (h *browserHandlers) devServerHost() gin.HandlerFunc {
	return func(c *gin.Context) {
		r := c.Request
		if runID, file, ok := parseDevFilePath(r.URL.Path); ok {
			h.serveDevFile(c, runID, file)
			c.Abort()
			return
		}
		runID, port, rest, slash, ok := parseDevServerPath(r.URL.EscapedPath())
		if !ok {
			if target, stray := devServerStrayTarget(r); stray {
				c.Redirect(http.StatusTemporaryRedirect, target)
				c.Abort()
				return
			}
			c.Next()
			return
		}
		if !slash {
			target := devServerPrefix(runID, port) + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			c.Redirect(http.StatusFound, target)
			c.Abort()
			return
		}
		h.proxyDevServer(c, runID, port, "/"+rest)
		c.Abort()
	}
}

// devServerStrayTarget maps a request a dev page sent to a root path (module
// imports such as /src/App.tsx, assets, links, forms) under the page's
// prefix. Dev pages are served with Referrer-Policy: same-origin, so their
// requests name the page they come from.
func devServerStrayTarget(r *http.Request) (string, bool) {
	if strings.HasPrefix(r.URL.Path, devServerPathPrefix) || websocket.IsWebSocketUpgrade(r) {
		return "", false
	}
	ref, err := url.Parse(r.Referer())
	if err != nil || ref.Host == "" {
		return "", false
	}
	runID, port, _, _, ok := parseDevServerPath(ref.EscapedPath())
	if !ok {
		return "", false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin":
	case "":
		// Plain-HTTP deployments get no fetch metadata; compare hosts.
		if !strings.EqualFold(ref.Host, r.Host) {
			return "", false
		}
	default:
		return "", false
	}
	target := devServerPrefix(runID, port) + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	return target, true
}

func (h *browserHandlers) proxyDevServer(c *gin.Context, runID string, port int, path string) {
	r := c.Request
	if websocket.IsWebSocketUpgrade(r) {
		devServerFail(c, http.StatusNotImplemented, "开发服务器的实时连接（如热更新）暂不支持，修改代码后请刷新页面", false)
		return
	}
	run, ok := h.devServerRun(c, runID)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, r.Body, devServerRequestLimit))
	if err != nil {
		devServerFail(c, http.StatusRequestEntityTooLarge, "请求体过大", false)
		return
	}
	prefix := devServerPrefix(runID, port)
	target := path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req := domain.BrowserRequest{Operation: "http", HTTP: &domain.BrowserHTTPRequest{
		Port: port, Method: r.Method, Path: target, Header: devServerRequestHeader(r, prefix, port), Body: body,
	}}
	res, ok := h.devServerFetch(c, run, req, "开发服务器暂不可用")
	if !ok {
		return
	}
	writeDevServerResponse(c, res, prefix, port)
}

// devServerRun resolves the run of a preview request for a user who may
// drive its workspace; otherwise it answers the request itself.
func (h *browserHandlers) devServerRun(c *gin.Context, runID string) (domain.Run, bool) {
	r := c.Request
	run, ok := h.repo.RunState(runID)
	if !ok {
		devServerFail(c, http.StatusNotFound, "运行不存在", false)
		return domain.Run{}, false
	}
	user, ok := h.requestUser(r, false)
	if !ok {
		devServerFail(c, http.StatusUnauthorized, "请先登录平台", false)
		return domain.Run{}, false
	}
	if !h.canControl(r, run, user.ID) {
		devServerFail(c, http.StatusForbidden, "无权访问该任务的开发服务器", false)
		return domain.Run{}, false
	}
	h.touchWorkspace(run.ID)
	return run, true
}

// devServerFetch runs a preview operation on the executor, starting the run
// browser when it is not up: dev server ports live in its network namespace
// and file:// paths are the ones it sees. Failures are answered here.
func (h *browserHandlers) devServerFetch(c *gin.Context, run domain.Run, req domain.BrowserRequest, unavailable string) (*domain.BrowserHTTPResponse, bool) {
	r := c.Request
	state, opErr := h.devServerCall(r.Context(), run, req)
	if state.HTTP == nil && !state.Available && opErr == "" {
		req.Ensure = true
		state, opErr = h.devServerCall(r.Context(), run, req)
	}
	if opErr != "" {
		if strings.Contains(opErr, "不支持的浏览器操作") {
			opErr = "执行机版本过旧，暂不支持原生预览，请升级执行机"
		}
		devServerFail(c, http.StatusBadGateway, opErr, false)
		return nil, false
	}
	if state.HTTP == nil {
		devServerFail(c, http.StatusBadGateway, firstNonEmpty(state.Reason, unavailable), devServerNavigation(r))
		return nil, false
	}
	return state.HTTP, true
}

func (h *browserHandlers) devServerCall(ctx context.Context, run domain.Run, req domain.BrowserRequest) (domain.BrowserState, string) {
	payload, opErr := h.dispatch(ctx, run, req)
	var state domain.BrowserState
	_ = json.Unmarshal(payload, &state)
	return state, opErr
}

func devServerNavigation(r *http.Request) bool {
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// Request headers that carry the platform's identity or the proxy chain stay
// on this side; the sandbox only sees what a browser talking to the dev
// server directly would send.
var devServerRequestHeaders = map[string]bool{
	"Accept": true, "Accept-Language": true, "Cache-Control": true, "Pragma": true,
	"Content-Type": true, "If-None-Match": true, "If-Modified-Since": true, "If-Match": true,
	"If-Unmodified-Since": true, "Range": true, "If-Range": true, "User-Agent": true,
	"Sec-Fetch-Dest": true, "Sec-Fetch-Mode": true, "Sec-Fetch-Site": true, "Sec-Fetch-User": true,
	"Sec-Ch-Ua": true, "Sec-Ch-Ua-Mobile": true, "Sec-Ch-Ua-Platform": true,
	"Upgrade-Insecure-Requests": true, "Purpose": true, "Sec-Purpose": true,
}

var devServerBlockedXHeaders = []string{"X-Forwarded-", "X-Real-", "X-Client-", "X-Rio-", "X-Tai-", "X-Ext-", "X-Original-"}

func devServerRequestHeader(r *http.Request, prefix string, port int) map[string][]string {
	out := map[string][]string{}
	for name, values := range r.Header {
		key := http.CanonicalHeaderKey(name)
		switch {
		case devServerRequestHeaders[key]:
		case key == "Authorization":
			// Browsers replay cached Basic credentials of the platform itself.
			if v := r.Header.Get(key); v == "" || strings.HasPrefix(strings.ToLower(v), "basic ") {
				continue
			}
		case strings.HasPrefix(key, "X-") && !devServerBlockedXHeader(key):
		default:
			continue
		}
		out[key] = values
	}
	local := "http://localhost:" + strconv.Itoa(port)
	if r.Header.Get("Origin") != "" {
		out["Origin"] = []string{local}
	}
	if ref, err := url.Parse(r.Referer()); err == nil {
		if refRun, refPort, rest, _, ok := parseDevServerPath(ref.EscapedPath()); ok && devServerPrefix(refRun, refPort) == prefix {
			referer := local + "/" + rest
			if ref.RawQuery != "" {
				referer += "?" + ref.RawQuery
			}
			out["Referer"] = []string{referer}
		}
	}
	if tags := devServerUpstreamETags(r.Header.Values("If-None-Match")); tags != "" {
		out["If-None-Match"] = []string{tags}
	} else {
		delete(out, "If-None-Match")
	}
	cookiePrefix := preview.DevServerCookiePrefix(prefix)
	var cookies []string
	for _, cookie := range r.Cookies() {
		if name, ok := strings.CutPrefix(cookie.Name, cookiePrefix); ok && name != "" {
			cookies = append(cookies, name+"="+cookie.Value)
		}
	}
	if len(cookies) > 0 {
		out["Cookie"] = []string{strings.Join(cookies, "; ")}
	}
	return out
}

// devServerRewriteVersion tags the ETags of rewritten responses: a cached
// copy rewritten by an older gateway must not be revalidated as current.
const devServerRewriteVersion = "-af1"

func devServerETag(etag string) string {
	if strings.HasSuffix(etag, `"`) {
		return strings.TrimSuffix(etag, `"`) + devServerRewriteVersion + `"`
	}
	return etag + devServerRewriteVersion
}

// devServerUpstreamETags keeps the If-None-Match tags this gateway version
// issued, restored to the dev server's own form.
func devServerUpstreamETags(values []string) string {
	var tags []string
	for _, value := range values {
		for _, tag := range strings.Split(value, ",") {
			tag = strings.TrimSpace(tag)
			quoted := strings.HasSuffix(tag, `"`)
			bare := strings.TrimSuffix(tag, `"`)
			if !strings.HasSuffix(bare, devServerRewriteVersion) {
				continue
			}
			bare = strings.TrimSuffix(bare, devServerRewriteVersion)
			if quoted {
				bare += `"`
			}
			tags = append(tags, bare)
		}
	}
	return strings.Join(tags, ", ")
}

func devServerBlockedXHeader(key string) bool {
	for _, prefix := range devServerBlockedXHeaders {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// Response headers that would reach beyond the preview: framing and CSP that
// block the side panel, HSTS and Clear-Site-Data for the platform origin, and
// Service-Worker-Allowed, which would let a worker control the platform.
var devServerDroppedResponseHeaders = map[string]bool{
	"Content-Length": true, "Content-Security-Policy": true, "Content-Security-Policy-Report-Only": true,
	"X-Frame-Options": true, "Strict-Transport-Security": true, "Clear-Site-Data": true,
	"Service-Worker-Allowed": true, "Referrer-Policy": true, "Set-Cookie": true, "Location": true,
	"Alt-Svc": true, "Access-Control-Allow-Origin": true, "Access-Control-Allow-Credentials": true,
}

func writeDevServerResponse(c *gin.Context, res *domain.BrowserHTTPResponse, prefix string, port int) {
	header := c.Writer.Header()
	for name, values := range res.Header {
		key := http.CanonicalHeaderKey(name)
		if devServerDroppedResponseHeaders[key] {
			continue
		}
		for _, v := range values {
			header.Add(key, v)
		}
	}
	for name, values := range res.Header {
		switch http.CanonicalHeaderKey(name) {
		case "Location":
			if len(values) > 0 {
				header.Set("Location", preview.RewriteDevServerLocation(values[0], prefix, port))
			}
		case "Set-Cookie":
			for _, v := range values {
				header.Add("Set-Cookie", rewriteDevServerCookie(v, prefix))
			}
		}
	}
	if etag := header.Get("Etag"); etag != "" {
		header.Set("Etag", devServerETag(etag))
	}
	// Root-path requests find their way back by Referer.
	header.Set("Referrer-Policy", "same-origin")
	header.Set("X-Content-Type-Options", "nosniff")
	body := res.Body
	if header.Get("Content-Encoding") == "" {
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(header.Get("Content-Type"), ";")[0]))
		switch mediaType {
		case "text/html", "application/xhtml+xml":
			body = preview.RewriteDevServerHTML(body, prefix, port)
		case "text/javascript", "application/javascript", "application/x-javascript", "text/ecmascript", "application/ecmascript":
			body = preview.RewriteDevServerScript(body, prefix)
		case "text/css":
			body = preview.RewriteDevServerCSS(body, prefix)
		}
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	status := res.Status
	if status < 100 || status > 999 {
		status = http.StatusBadGateway
	}
	c.Status(status)
	if c.Request.Method != http.MethodHead && status != http.StatusNoContent && status != http.StatusNotModified {
		_, _ = c.Writer.Write(body)
	}
}

// rewriteDevServerCookie namespaces a dev server cookie by run and port and keeps it
// on the platform host under the preview prefix.
func rewriteDevServerCookie(raw, prefix string) string {
	parts := strings.Split(raw, ";")
	out := []string{preview.DevServerCookiePrefix(prefix) + strings.TrimSpace(parts[0])}
	for _, attr := range parts[1:] {
		attr = strings.TrimSpace(attr)
		name, value, _ := strings.Cut(attr, "=")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "":
			continue
		case "domain":
			continue
		case "path":
			value = strings.TrimSpace(value)
			if !strings.HasPrefix(value, "/") {
				value = "/"
			}
			if value != prefix && !strings.HasPrefix(value, prefix+"/") {
				value = prefix + value
			}
			attr = "Path=" + value
		}
		out = append(out, attr)
	}
	return strings.Join(out, "; ")
}

func devServerFail(c *gin.Context, status int, message string, retry bool) {
	if !devServerNavigation(c.Request) {
		c.Data(status, "text/plain; charset=utf-8", []byte(message))
		return
	}
	refresh := ""
	hint := ""
	if retry {
		refresh = `<meta http-equiv="refresh" content="3">`
		hint = `<p class="hint">页面每 3 秒自动重试；让 Agent 启动开发服务器后这里会自动打开。</p>`
	}
	page := fmt.Sprintf(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">%s<title>开发服务器预览</title><style>body{margin:0;min-height:100vh;display:grid;place-items:center;font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#3b3f45;background:#f7f8fa}@media(prefers-color-scheme:dark){body{color:#d7dae0;background:#1d1f23}}main{max-width:420px;padding:24px;text-align:center}.hint{opacity:.65;font-size:13px}</style></head><body><main><p>%s</p>%s</main></body></html>`,
		refresh, html.EscapeString(message), hint)
	c.Header("Cache-Control", "no-store")
	c.Data(status, "text/html; charset=utf-8", []byte(page))
}

// devServerFileSegment marks workspace files in place of a port:
// /devpreview/{runID}/file/{absolute path} serves what the run browser opens
// as file://{absolute path}, so relative links resolve as they do there.
const devServerFileSegment = "file"

// parseDevFilePath splits /devpreview/{runID}/file/{path} of an unescaped
// request path; file is absolute and cleaned, keeping a trailing slash.
func parseDevFilePath(p string) (runID, file string, ok bool) {
	tail, found := strings.CutPrefix(p, devServerPathPrefix)
	if !found {
		return "", "", false
	}
	runID, tail, found = strings.Cut(tail, "/")
	if !found || !devServerRunIDPattern.MatchString(runID) {
		return "", "", false
	}
	rest, found := strings.CutPrefix(tail, devServerFileSegment+"/")
	if !found || rest == "" {
		return "", "", false
	}
	file = path.Clean("/" + rest)
	if strings.HasSuffix(rest, "/") && file != "/" {
		file += "/"
	}
	return runID, file, true
}

func (h *browserHandlers) serveDevFile(c *gin.Context, runID, file string) {
	r := c.Request
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		devServerFail(c, http.StatusMethodNotAllowed, "工作区文件只支持读取", false)
		return
	}
	run, ok := h.devServerRun(c, runID)
	if !ok {
		return
	}
	res, ok := h.devServerFetch(c, run, domain.BrowserRequest{Operation: "file", File: file}, "工作区文件暂不可用")
	if !ok {
		return
	}
	header := c.Writer.Header()
	if res.Status == http.StatusMovedPermanently {
		// A directory: serve it with a slash so its relative links resolve.
		target := r.URL.EscapedPath() + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		c.Redirect(http.StatusMovedPermanently, target)
		return
	}
	if v := res.Header["Content-Type"]; len(v) > 0 {
		header.Set("Content-Type", v[0])
	}
	// The Agent keeps editing these files; always revalidate.
	header.Set("Cache-Control", "no-cache")
	header.Set("Referrer-Policy", "same-origin")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Length", strconv.Itoa(len(res.Body)))
	status := res.Status
	if status < 100 || status > 999 {
		status = http.StatusBadGateway
	}
	c.Status(status)
	if r.Method != http.MethodHead {
		_, _ = c.Writer.Write(res.Body)
	}
}
