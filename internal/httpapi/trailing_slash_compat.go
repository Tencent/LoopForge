package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// apiTrailingSlash405 列出 master(net/http) 时代各子树 handler 在"路径匹配
// 到 /api/<entity>/ 子树"时返回 405 的方法集合（子树 handler 只支持部分
// 方法，其余先走 method(w)→405 JSON，再轮到空/多段 id 的 404）。未列出的
// 实体子树 handler 对任何请求一律 http.NotFound（404 text）。
var apiTrailingSlash405 = map[string][]string{
	"users":        {"GET", "POST", "PUT", "DELETE", "PATCH"},
	"repositories": {"POST", "PUT", "DELETE", "PATCH"},
	"projects":     {"POST", "PUT", "DELETE", "PATCH"},
	"tasks":        {"POST", "PUT", "DELETE", "PATCH"},
	"auth/tokens":  {"GET", "POST", "PUT", "PATCH"},
	"otel":         {"GET", "PUT", "DELETE", "PATCH"},
}

// masterTrailingSlashCompat 复刻 master 子树 handler 对"尾斜杠 /api 请求"的
// 行为，保证两个网关实现（net/http 前缀挂载 vs gin 精确路由）在边缘路径上
// 返回一致。返回 true 表示已写响应。
func masterTrailingSlashCompat(c *gin.Context, s *Server) bool {
	p := c.Request.URL.Path
	if !strings.HasPrefix(p, "/api/") || !strings.HasSuffix(p, "/") {
		return false
	}
	rest := strings.TrimPrefix(p, "/api/")
	entity := rest
	if i := strings.Index(rest, "/"); i >= 0 {
		entity = rest[:i]
	}
	// "auth/tokens" 这类两段子树：兼容表以两段为 key（/api/auth/ 本身没有
	// 子树注册，取首段会 miss）。
	if i := strings.Index(rest, "/"); i >= 0 {
		if second := rest[i+1:]; second != "" {
			if tail := strings.Index(second, "/"); tail >= 0 {
				second = second[:tail]
			}
			if _, ok := apiTrailingSlash405[entity+"/"+second]; ok {
				entity = entity + "/" + second
			}
		}
	}
	// master authToken handler：非 DELETE 一律 405；DELETE 通过方法检查后做
	// 登录检查（未登录 401；登录后空 id 400 "Token ID 不能为空"）。
	if entity == "auth/tokens" {
		if c.Request.Method != http.MethodDelete {
			c.JSON(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		} else if _, logged := s.requestUser(c.Request, false); logged {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "Token ID 不能为空"})
		} else {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后管理个人 Token"})
		}
		return true
	}
	methods, ok := apiTrailingSlash405[entity]
	if ok && containsMethod(methods, c.Request.Method) {
		c.JSON(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return true
	}
	// 其余情形（方法被支持但 id 为空/含多段，或实体无 405 语义）：
	// master 子树 handler 对空/多段 id 一律 http.NotFound（404 text/plain）。
	// 典型：otel 的 POST /api/otel/（非 /v1/traces 路径）→ 404 text。
	http.NotFound(c.Writer, c.Request)
	return true
}

func containsMethod(methods []string, m string) bool {
	for _, method := range methods {
		if method == m {
			return true
		}
	}
	return false
}
