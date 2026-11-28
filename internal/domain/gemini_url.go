package domain

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeGeminiBaseURL returns the service root used by both model discovery
// and Antigravity's Gemini SDK, which appends /v1beta itself. Proxy prefixes
// remain intact; an operation URL must not be mistaken for a service root.
func NormalizeGeminiBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("Gemini Base URL 必须是无账号密码、查询参数和片段的 HTTP(S) 服务地址")
	}
	path := strings.TrimRight(u.Path, "/")
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part != "v1beta" {
			continue
		}
		if i != len(parts)-1 {
			return "", fmt.Errorf("Gemini Base URL 请填写服务根地址，不要包含 /v1beta/ 后的接口路径")
		}
		path = strings.TrimSuffix(path, "/v1beta")
	}
	u.Path, u.RawPath = path, ""
	return u.String(), nil
}
