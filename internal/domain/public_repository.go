package domain

import (
	"net/url"
	"regexp"
	"strings"
)

var githubRepositoryPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// CodeupRepoHost is the Git/web host of Aliyun Yunxiao Codeup (central station).
// A repository web/clone URL is https://codeup.aliyun.com/{orgId}/{group}/{repo}.
const CodeupRepoHost = "codeup.aliyun.com"

// codeupRepositoryPath matches "{orgId}/{group}/{repo}" (orgId plus at least two
// path segments); Codeup repositories always live under an organization group.
var codeupRepositoryPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+){2,}$`)

// ParseRepositoryURL adds public hosting without changing the upstream adapter.
// The prefixed remote identity prevents collisions with enterprise project IDs.
func ParseRepositoryURL(raw string) (GongfengRepoInfo, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "git clone ")
	s = strings.Trim(s, "\"'")
	if info, ok := parseCodeupRepositoryURL(s); ok {
		return info, true
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
	if !strings.ContainsAny(s, ":@") {
		host, _, _ := strings.Cut(s, "/")
		if !IsGongfengHost(host) {
			return GongfengRepoInfo{}, false
		}
	}
	return ParseGongfengRepoURL(raw)
}

// parseCodeupRepositoryURL recognizes a Codeup repository reference and returns
// the prefixed identity "codeup:{orgId}/{group}/{repo}" plus normalized HTTPS
// URLs. Accepted inputs mirror GitHub: the codeup: shorthand, git@/ssh:// clone
// forms, a bare codeup.aliyun.com/... path and an https:// web or clone URL.
func parseCodeupRepositoryURL(s string) (GongfengRepoInfo, bool) {
	if strings.HasPrefix(s, "codeup:") {
		s = "https://" + CodeupRepoHost + "/" + strings.TrimPrefix(s, "codeup:")
	}
	for _, prefix := range []string{"git@" + CodeupRepoHost + ":", "ssh://git@" + CodeupRepoHost + "/", "ssh://git@" + CodeupRepoHost + ":22/"} {
		if strings.HasPrefix(s, prefix) {
			s = "https://" + CodeupRepoHost + "/" + strings.TrimPrefix(s, prefix)
			break
		}
	}
	if strings.HasPrefix(s, CodeupRepoHost+"/") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host != CodeupRepoHost || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return GongfengRepoInfo{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 {
		return GongfengRepoInfo{}, false
	}
	parts[len(parts)-1] = strings.TrimSuffix(parts[len(parts)-1], ".git")
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return GongfengRepoInfo{}, false
		}
	}
	path := strings.Join(parts, "/")
	if !codeupRepositoryPath.MatchString(path) {
		return GongfengRepoInfo{}, false
	}
	base := "https://" + CodeupRepoHost + "/" + path
	return GongfengRepoInfo{Project: "codeup:" + path, Name: parts[len(parts)-1], WebURL: base, CloneURL: base + ".git"}, true
}

// ParseCodeupProject splits the "codeup:{orgId}/{group}/{repo}" repository
// identity into its organization ID and full repository path. The full path
// (orgId/group/repo) is what the Codeup OpenAPI accepts as a URL-encoded
// repositoryId, and the organization ID scopes every central-station call.
func ParseCodeupProject(project string) (org, repoPath string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(project), "codeup:")
	if !found {
		return "", "", false
	}
	org, path, found := strings.Cut(rest, "/")
	if !found || org == "" || path == "" || !codeupRepositoryPath.MatchString(rest) {
		return "", "", false
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "." || seg == ".." {
			return "", "", false
		}
	}
	return org, rest, true
}
