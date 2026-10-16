package domain

import (
	"net/url"
	"regexp"
	"strings"
)

var githubRepositoryPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ParseRepositoryURL adds public hosting without changing the upstream adapter.
// The prefixed remote identity prevents collisions with enterprise project IDs.
func ParseRepositoryURL(raw string) (GongfengRepoInfo, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "git clone ")
	s = strings.Trim(s, "\"'")
	if strings.HasPrefix(s, "github:") {
		s = "https://github.com/" + strings.TrimPrefix(s, "github:")
	}
	if strings.HasPrefix(s, "git@github.com:") {
		s = "https://github.com/" + strings.TrimPrefix(s, "git@github.com:")
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
	return ParseGongfengRepoURL(raw)
}
