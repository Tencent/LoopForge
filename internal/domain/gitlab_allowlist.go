package domain

import (
	"net"
	"os"
	"strings"
)

// GitLabAllowedHostsEnv 声明本部署允许接入的自托管 GitLab 实例：逗号分隔，
// 每项为 host 或 host:port，可带 http(s):// 前缀（归一化时去掉）。
//
// 它是凭据的信任边界而不是可选项：PRIVATE-TOKEN 只会发往这里声明的实例。
// 未声明时一律拒绝，包括回环与内网地址——项目成员可以在任意内网地址上起
// 一个服务，若按"内网即可信"放行，owner 的 Git 令牌仍会被送过去。运维在
// 部署配置里显式声明实例，才把该实例纳入凭据可达范围。
const GitLabAllowedHostsEnv = "AGENTFLOW_GITLAB_ALLOWED_HOSTS"

// GitLabAllowedHosts 返回归一化后的实例 allowlist（小写、去 scheme/斜杠）。
func GitLabAllowedHosts() []string {
	raw := os.Getenv(GitLabAllowedHostsEnv)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	entries := []string{}
	for _, item := range strings.Split(raw, ",") {
		if entry := normalizeGitLabHost(item); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

// GitLabHostAllowed 报告 host[:port] 是否是管理员显式声明的 GitLab 实例。
// 带端口的条目光精确匹配 host:port；只写 host 的条目匹配该 host 的任意端口。
func GitLabHostAllowed(host string) bool {
	target := normalizeGitLabHost(host)
	if target == "" {
		return false
	}
	targetHost, _ := splitGitLabHostPort(target)
	for _, entry := range GitLabAllowedHosts() {
		if entry == target {
			return true
		}
		entryHost, entryHasPort := splitGitLabHostPort(entry)
		// 显式写了端口的条目只匹配同一端口（上面的精确比较已覆盖），
		// 不再按 host 放宽。
		if !entryHasPort && entryHost == targetHost {
			return true
		}
	}
	return false
}

// GitLabInstanceHost 从 "gitlab:<host>/<group>/<repo>" 标识取 host。
func GitLabInstanceHost(id string) (string, bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(id), "gitlab:")
	if !found {
		return "", false
	}
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return "", false
	}
	return rest[:slash], true
}

// normalizeGitLabHost 归一化 allowlist 条目与待匹配 host：去空白、去
// http(s):// 前缀与结尾斜杠、转小写。
func normalizeGitLabHost(raw string) string {
	host := strings.ToLower(strings.TrimSpace(raw))
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	return strings.TrimSuffix(host, "/")
}

// splitGitLabHostPort 拆出 host 与"是否显式带端口"。解析失败（如 IPv6 字面量
// 未加方括号）时按无端口处理，由精确比较兜底。
func splitGitLabHostPort(hostport string) (string, bool) {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host, true
	}
	if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		return strings.Trim(hostport, "[]"), false
	}
	return hostport, false
}
