package gitwork

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// HTTPSRepositoryURL validates a transport URL without identifying the host's
// product. Credentials are requested separately through the Git helper.
func HTTPSRepositoryURL(raw string) (remote, name string, err error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "git clone "))
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "\\\t\r\n ") {
		return "", "", fmt.Errorf("请提供不含凭据、查询参数或片段的 HTTPS Git 克隆地址")
	}
	host, err := HTTPSHostScope(u.Host)
	if err != nil {
		return "", "", err
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return "", "", fmt.Errorf("Git 地址缺少仓库路径")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return "", "", fmt.Errorf("Git 仓库路径无效")
		}
	}
	u.Host, u.Path = host, "/"+path
	if u.RawPath != "" {
		u.RawPath = "/" + strings.Trim(u.RawPath, "/")
	}
	name = strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".git")
	if name == "" {
		return "", "", fmt.Errorf("Git 仓库名称不能为空")
	}
	return u.String(), name, nil
}
