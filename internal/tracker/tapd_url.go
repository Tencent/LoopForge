package tracker

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// TAPDStoryRef is a TAPD record identified from a pasted web URL.
// Kind is "story" (default), "bug" or "task".
type TAPDStoryRef struct {
	WorkspaceID string
	StoryID     string
	Kind        string
}

var (
	// Capture complete URLs before looking for bare TAPD hosts, so a TAPD URL
	// inside another host's path or query cannot pass the host check.
	tapdPastedURLRe     = regexp.MustCompile("(?i)(?:[a-z][a-z0-9+.-]*://|//|(?:tapd\\.example\\.com|(?:www\\.)?tapd\\.cn)/)[^\\s\\p{Z}<>\\[\\](){}\"'`，。；：！？、（）【】《》「」『』]+")
	tapdWorkspacePathRe = regexp.MustCompile(`^/(?:tapd_fe/)?(\d{2,12})(?:/|$)`)
	tapdDetailPathRe    = regexp.MustCompile(`/(story/detail|stories/view|bug/detail|bugs/view|task/detail|tasks/view)/(\d+)/?$`)
	tapdPreviewIDRe     = regexp.MustCompile(`^(story|bug|task)_(\d+)$`)
)

// ParseTAPDStoryURL extracts one TAPD record from a URL or pasted title and
// link. Repeated links to the same record are harmless; different records must
// be imported separately to avoid silently choosing the wrong requirement.
func ParseTAPDStoryURL(raw string) (TAPDStoryRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return TAPDStoryRef{}, fmt.Errorf("请粘贴 TAPD 需求链接")
	}
	var found TAPDStoryRef
	var firstErr error
	for _, match := range tapdPastedURLRe.FindAllStringIndex(raw, -1) {
		candidate := raw[match[0]:match[1]]
		// A bare host must stand on its own, rather than be a suffix of a
		// foreign hostname or e-mail address.
		if !strings.Contains(candidate, "://") && !strings.HasPrefix(candidate, "//") && match[0] > 0 && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.@/", rune(raw[match[0]-1])) {
			continue
		}
		candidate = strings.TrimRight(candidate, ".,;:!?")
		ref, err := parseTAPDStoryLink(candidate)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if found.StoryID != "" && ref != found {
			return TAPDStoryRef{}, fmt.Errorf("检测到多个不同的 TAPD 需求、缺陷或任务链接，请每次只导入一个")
		}
		found = ref
	}
	if found.StoryID != "" {
		return found, nil
	}
	if firstErr != nil {
		return TAPDStoryRef{}, firstErr
	}
	return TAPDStoryRef{}, fmt.Errorf("无法识别 TAPD 链接，请粘贴需求详情链接（可包含标题）")
}

func parseTAPDStoryLink(raw string) (TAPDStoryRef, error) {
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	} else if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return TAPDStoryRef{}, fmt.Errorf("无法识别 TAPD 链接")
	}
	host := strings.ToLower(parsed.Hostname())
	if (parsed.Scheme != "https" && parsed.Scheme != "http") || (host != "tapd.example.com" && host != "www.tapd.cn" && host != "tapd.cn") {
		return TAPDStoryRef{}, fmt.Errorf("请粘贴 tapd.example.com 或 tapd.cn 的需求详情链接")
	}
	path := parsed.Path
	ref := TAPDStoryRef{Kind: "story"}
	if m := tapdWorkspacePathRe.FindStringSubmatch(path); len(m) == 2 {
		ref.WorkspaceID = m[1]
	}
	if m := tapdPreviewIDRe.FindStringSubmatch(parsed.Query().Get("dialog_preview_id")); len(m) == 3 {
		ref.Kind, ref.StoryID = m[1], m[2]
	}
	if ref.StoryID == "" {
		if m := tapdDetailPathRe.FindStringSubmatch(path); len(m) == 3 {
			ref.StoryID = m[2]
			if strings.HasPrefix(m[1], "bug") {
				ref.Kind = "bug"
			} else if strings.HasPrefix(m[1], "task") {
				ref.Kind = "task"
			}
		}
	}
	if ref.WorkspaceID == "" || ref.StoryID == "" || ref.WorkspaceID == ref.StoryID {
		return TAPDStoryRef{}, fmt.Errorf("链接里找不到 TAPD 项目和需求 ID，请使用需求详情页地址")
	}
	return ref, nil
}
