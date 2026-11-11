package domain

import (
	"net/url"
	"strings"
)

// ParseGitLabRepositoryURL accepts only the configured GitLab instance.
func ParseGitLabRepositoryURL(raw, instance string) (GongfengRepoInfo, bool) {
	instanceURL, err := url.Parse(strings.TrimSpace(instance))
	if err != nil || (instanceURL.Scheme != "http" && instanceURL.Scheme != "https") || instanceURL.Host == "" || instanceURL.User != nil || instanceURL.RawQuery != "" || instanceURL.Fragment != "" {
		return GongfengRepoInfo{}, false
	}
	basePath := strings.TrimRight(instanceURL.EscapedPath(), "/")
	s := strings.Trim(strings.TrimSpace(raw), "\\\"'")
	s = strings.TrimSpace(strings.TrimPrefix(s, "git clone "))
	if strings.HasPrefix(s, "gitlab:") {
		s = strings.TrimPrefix(s, "gitlab:")
	}
	if strings.HasPrefix(s, "git@") {
		host, path, ok := strings.Cut(strings.TrimPrefix(s, "git@"), ":")
		if !ok || !strings.EqualFold(host, instanceURL.Hostname()) {
			return GongfengRepoInfo{}, false
		}
		s = instanceURL.Scheme + "://" + instanceURL.Host + "/" + strings.TrimLeft(path, "/")
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.EqualFold(u.Scheme, instanceURL.Scheme) || !strings.EqualFold(u.Host, instanceURL.Host) {
		return GongfengRepoInfo{}, false
	}
	path := u.EscapedPath()
	if basePath != "" {
		if !strings.HasPrefix(path, basePath+"/") {
			return GongfengRepoInfo{}, false
		}
		path = strings.TrimPrefix(path, basePath)
	}
	path = normalizeGongfengProjectPath(strings.TrimLeft(path, "/"))
	if path == "" || strings.Contains(path, "%") {
		return GongfengRepoInfo{}, false
	}
	base := instanceURL.Scheme + "://" + instanceURL.Host + basePath + "/" + path
	return GongfengRepoInfo{Project: "gitlab:" + base, Name: path[strings.LastIndex(path, "/")+1:], WebURL: base, CloneURL: base + ".git"}, true
}
