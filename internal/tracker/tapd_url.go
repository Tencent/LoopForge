package tracker

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// TAPDStoryRef is a TAPD record identified from a pasted web URL.
// Kind is "story" (default) or "bug".
type TAPDStoryRef struct {
	WorkspaceID string
	StoryID     string
	Kind        string
}

var (
	tapdStoryIDRe      = regexp.MustCompile(`(?:^|story[_/]|detail/)(\d{10,19})(?:\D|$)`)
	tapdWorkspacePathRe = regexp.MustCompile(`(?:tapd_fe/|/+)(\d{2,12})(?:/|$)`)
)

// ParseTAPDStoryURL extracts workspace and story IDs from a TAPD web link.
func ParseTAPDStoryURL(raw string) (TAPDStoryRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return TAPDStoryRef{}, fmt.Errorf("请粘贴 TAPD 需求链接")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		if strings.Contains(raw, "tapd.example.com") || strings.Contains(raw, "tapd.cn") {
			parsed, err = url.Parse("https://" + strings.TrimPrefix(raw, "https://"))
		}
		if err != nil || parsed == nil || parsed.Host == "" {
			return TAPDStoryRef{}, fmt.Errorf("无法识别 TAPD 链接")
		}
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "tapd.example.com" && host != "www.tapd.cn" && host != "tapd.cn" {
		return TAPDStoryRef{}, fmt.Errorf("请粘贴 tapd.example.com 的需求详情链接")
	}
	path := parsed.EscapedPath()
	if unesc, err := url.PathUnescape(path); err == nil {
		path = unesc
	}
	ref := TAPDStoryRef{Kind: "story"}
	if m := tapdWorkspacePathRe.FindStringSubmatch(path); len(m) == 2 {
		ref.WorkspaceID = m[1]
	}
	if id := parsed.Query().Get("dialog_preview_id"); strings.HasPrefix(id, "story_") {
		ref.StoryID = strings.TrimPrefix(id, "story_")
	}
	if id := parsed.Query().Get("dialog_preview_id"); strings.HasPrefix(id, "bug_") {
		ref.StoryID = strings.TrimPrefix(id, "bug_")
		ref.Kind = "bug"
	}
	if ref.StoryID == "" {
		if i := strings.Index(path, "/bug/detail/"); i >= 0 {
			rest := path[i+len("/bug/detail/"):]
			ref.StoryID = digitsPrefix(rest)
			ref.Kind = "bug"
		}
	}
	if ref.StoryID == "" {
		if i := strings.Index(path, "/story/detail/"); i >= 0 {
			rest := path[i+len("/story/detail/"):]
			ref.StoryID = digitsPrefix(rest)
		}
	}
	if ref.StoryID == "" {
		if i := strings.Index(path, "/stories/view/"); i >= 0 {
			rest := path[i+len("/stories/view/"):]
			ref.StoryID = digitsPrefix(rest)
		}
	}
	if ref.StoryID == "" {
		if m := tapdStoryIDRe.FindStringSubmatch(path + "?" + parsed.RawQuery); len(m) == 2 && m[1] != ref.WorkspaceID {
			ref.StoryID = m[1]
		}
	}
	if ref.WorkspaceID == "" || ref.StoryID == "" || ref.WorkspaceID == ref.StoryID {
		return TAPDStoryRef{}, fmt.Errorf("链接里找不到 TAPD 项目和需求 ID，请使用需求详情页地址")
	}
	return ref, nil
}

func digitsPrefix(s string) string {
	s = strings.TrimLeft(s, "/")
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return s[:n]
}
