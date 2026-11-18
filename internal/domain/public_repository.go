package domain

import (
	"net/url"
	"regexp"
	"strings"
)

var githubRepositoryPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

var cnbPathSegment = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// parseCNBRepositoryURL accepts https://cnb.cool/<group>/<repo> with any
// number of group levels, optionally followed by .git or a /-/ sub page, and
// the cnb:<slug> shorthand stored as the project identity.
func parseCNBRepositoryURL(s string) (GongfengRepoInfo, bool) {
	if slug, ok := strings.CutPrefix(s, "cnb:"); ok {
		s = "https://cnb.cool/" + slug
	} else if strings.HasPrefix(s, "cnb.cool/") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host != "cnb.cool" || u.User != nil || u.Scheme != "https" {
		return GongfengRepoInfo{}, false
	}
	path, _, _ := strings.Cut(strings.Trim(u.Path, "/"), "/-/")
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/-"), ".git")
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return GongfengRepoInfo{}, false
	}
	for _, segment := range segments {
		if !cnbPathSegment.MatchString(segment) || segment == "." || segment == ".." {
			return GongfengRepoInfo{}, false
		}
	}
	web := "https://cnb.cool/" + path
	return GongfengRepoInfo{Project: "cnb:" + path, Name: segments[len(segments)-1], WebURL: web, CloneURL: web + ".git"}, true
}

// ParseRepositoryURL adds public hosting without changing the upstream adapter.
// The prefixed remote identity prevents collisions with enterprise project IDs.
func ParseRepositoryURL(raw string) (GongfengRepoInfo, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "git clone ")
	s = strings.Trim(s, "\"'")
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
	if info, ok := parseCNBRepositoryURL(s); ok {
		return info, true
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
