package domain

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

var githubRepositoryPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// gitlabProjectID 匹配 gitlab:<host>/<group...>/<repo> 形式的自托管 GitLab 标识。
// host 是裸主机名（可带端口，不含 scheme），项目路径至少两段。多实例靠 host 区分。
var gitlabProjectID = regexp.MustCompile(`^gitlab:([A-Za-z0-9][A-Za-z0-9._-]*(?::[0-9]+)?)/([A-Za-z0-9_./-]+)$`)

// gitlabHostname 允许 http(s) URL 中的主机字符（host[:port]）。
var gitlabHostname = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?::[0-9]+)?$`)

// ParseGitLabRepositoryURL 解析自托管 GitLab 仓库（172.16/12 这类非 github 主机）。
// 接受 https://host/group/repo、http、scp、gitlab:host/group/repo 等形态，
// 统一产出 "gitlab:<host>/<group>/<repo>" 前缀标识，host 进 ID 以支持多实例。
func ParseGitLabRepositoryURL(raw string) (GongfengRepoInfo, bool) {
	s := strings.Trim(strings.TrimSpace(raw), `"'`)
	if len(s) >= 10 && strings.EqualFold(s[:10], "git clone ") {
		s = strings.Trim(strings.TrimSpace(s[10:]), `"'`)
		if i := strings.IndexAny(s, " \t"); i >= 0 {
			s = s[:i]
		}
	}
	if s == "" || strings.ContainsAny(s, " \t\"'<>()") {
		return GongfengRepoInfo{}, false
	}
	// 已是前缀标识，直接规范化。前缀不带 scheme 信息，按安全默认 https。
	if strings.HasPrefix(s, "gitlab:") {
		m := gitlabProjectID.FindStringSubmatch(s)
		if m == nil {
			return GongfengRepoInfo{}, false
		}
		host, project := m[1], strings.Trim(strings.TrimSuffix(m[2], ".git"), "/")
		return gitlabRepoInfo("", host, project)
	}
	var host, path, scheme string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return GongfengRepoInfo{}, false
		}
		host, path, scheme = u.Host, strings.Trim(u.Path, "/"), u.Scheme
	case strings.HasPrefix(strings.ToLower(s), "git@"):
		rest := strings.TrimPrefix(s, "git@")
		at := strings.Index(rest, ":")
		if at < 0 {
			return GongfengRepoInfo{}, false
		}
		host, path = rest[:at], rest[at+1:]
	default:
		// 无 scheme 时第一段必须看起来像主机名（含点或端口），避免和工蜂裸 project 冲突。
		first := s
		if i := strings.Index(s, "/"); i >= 0 {
			first = s[:i]
		}
		if !strings.Contains(first, ".") && !strings.Contains(first, ":") {
			return GongfengRepoInfo{}, false
		}
		host = first
		path = strings.TrimPrefix(s, first+"/")
	}
	if !gitlabHostname.MatchString(host) {
		return GongfengRepoInfo{}, false
	}
	// 裸 URL（非显式前缀）必须指向可信内网主机：公网域名/仿冒主机名（如
	// github.com.attacker.invalid）一律拒绝。要接公网自建 GitLab 请用 gitlab: 前缀显式声明。
	if !isTrustedGitLabHost(strings.Split(host, ":")[0]) {
		return GongfengRepoInfo{}, false
	}
	path = strings.Trim(strings.TrimSuffix(path, ".git"), "/")
	project := normalizeGongfengProjectPath(path)
	if project == "" {
		return GongfengRepoInfo{}, false
	}
	// scp/裸主机形态没有 scheme 信息（scheme 为空），由 gitlabRepoInfo 兜底 https。
	return gitlabRepoInfo(scheme, host, project)
}

// gitlabRepoInfo 组装前缀标识与站点 URL。scheme 只认 http/https：
// 显式 http 保留（内网实例常见），其余（空值、https）一律 https，
// 绝不静默降级——HTTPS-only 实例不能被降成明文 http 传输 token。
func gitlabRepoInfo(scheme, host, project string) (GongfengRepoInfo, bool) {
	if scheme != "http" {
		scheme = "https"
	}
	id := "gitlab:" + host + "/" + project
	base := scheme + "://" + host + "/" + project
	info := GongfengRepoInfo{Project: id, Name: project[strings.LastIndex(project, "/")+1:], WebURL: base, CloneURL: base + ".git"}
	return info, true
}

// GitLabInstanceScheme 从仓库记录的 URL 解析实例 scheme：host[:port] 与
// "gitlab:<host>/<path>" 标识一致时返回该 URL 的 scheme，否则默认 https。
// 标识本身不带 scheme，API 调用与 MR 链接统一经此对齐，防止硬编码降级。
func GitLabInstanceScheme(host string, urls ...string) string {
	host = strings.TrimSpace(host)
	for _, raw := range urls {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		if strings.EqualFold(u.Host, host) {
			return u.Scheme
		}
	}
	return "https"
}

// isTrustedGitLabHost 限定自托管 GitLab 的可信主机：裸 URL（无 gitlab: 前缀）
// 只信 RFC1918/回环地址与内网命名（.internal/.local）。172 系只认 RFC1918 的
// 172.16.0.0/12——172.0.0.0/8 其余大半是公网（如 172.217/16 Google、172.64/13
// Cloudflare），整段放行会让公网主机冒充内网实例。link-local（169.254/16，
// 云元数据端点）同样不放行：没有任何理由把实例指向它。非常规网段的内网
// 实例请用 gitlab: 前缀显式声明（管理员行为，任意主机）。
func isTrustedGitLabHost(host string) bool {
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	// 内网主机名：只认 .internal/.local 结尾的后缀。绝不能用子串匹配
	// （Contains）——gitlab.internal.attacker.example 这类公网攻击域名
	// 包含 ".internal" 子串，会被当成内网实例，成员 token 随 PRIVATE-TOKEN
	// 发到攻击者主机。运维可在此扩展精确后缀白名单。
	return strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local")
}

// ParseRepositoryURL adds public hosting without changing the upstream adapter.
// The prefixed remote identity prevents collisions with enterprise project IDs.
func ParseRepositoryURL(raw string) (GongfengRepoInfo, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "git clone ")
	s = strings.Trim(s, "\"'")
	if s == "" || strings.HasPrefix(s, "gitlab:") {
		return ParseGitLabRepositoryURL(s)
	}
	if strings.HasPrefix(s, "github:") {
		s = "https://github.com/" + strings.TrimPrefix(s, "github:")
	}
	for _, prefix := range []string{"git@github.com:", "ssh://git@github.com/", "ssh://git@github.com:22/"} {
		if strings.HasPrefix(s, prefix) {
			s = "https://github.com/" + strings.TrimPrefix(s, prefix)
			break
		}
	}
	if strings.HasPrefix(s, "github.com/") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err == nil && u.Host == "github.com" && u.User == nil && (u.Scheme == "https" || u.Scheme == "http") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return GongfengRepoInfo{}, false
		}
		path := parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
		if !githubRepositoryPath.MatchString(path) || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
			return GongfengRepoInfo{}, false
		}
		base := "https://github.com/" + path
		return GongfengRepoInfo{Project: "github:" + path, Name: strings.TrimSuffix(parts[1], ".git"), WebURL: base, CloneURL: base + ".git"}, true
	}
	// 非 github 的 http(s) URL：ParseGitLabRepositoryURL 内部已限定可信内网主机，
	// 公网域名/仿冒主机（github.com.attacker.invalid）会被拒；公网自建请用 gitlab: 前缀。
	if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil {
		if info, ok := ParseGitLabRepositoryURL(s); ok {
			return info, true
		}
	}
	return ParseGongfengRepoURL(raw)
}
