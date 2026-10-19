package tracker

import (
	"strings"
	"testing"
)

func TestParseTAPDStoryURLPastedText(t *testing.T) {
	const storyURL = "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001554"
	want := TAPDStoryRef{WorkspaceID: "10000000", StoryID: "1010000000100001554", Kind: "story"}
	cases := map[string]string{
		"plain URL":                 storyURL,
		"title on preceding line":   "【【DP】【国际站】关闭套餐，只更新免费队列，付费队列无降级或删除操作，导致付费队列存在脏数据】\n" + storyURL,
		"single line":               "关闭套餐需求 " + storyURL,
		"CRLF and whitespace":       "  关闭套餐需求\r\n\t" + storyURL + "  \r\n",
		"markdown link":             "[关闭套餐需求](" + storyURL + ")",
		"markdown title":            "[关闭套餐需求](" + storyURL + " \"TAPD\")",
		"angle brackets":            "关闭套餐需求：<" + storyURL + ">",
		"Chinese parentheses":       "关闭套餐需求（" + storyURL + "）",
		"Chinese brackets":          "关闭套餐需求【" + storyURL + "】",
		"trailing punctuation":      "关闭套餐需求：" + storyURL + "。",
		"inline code":               "需求：`" + storyURL + "`",
		"protocol relative":         strings.TrimPrefix(storyURL, "https:"),
		"unicode space":             storyURL + "\u00a0",
		"without scheme":            "关闭套餐需求\ntapd.example.com/tapd_fe/10000000/story/detail/1010000000100001554",
		"public TAPD":               "https://www.tapd.cn/10000000/prong/stories/view/1010000000100001554",
		"uppercase scheme and host": strings.ToUpper("https://tapd.example.com") + "/tapd_fe/10000000/story/detail/1010000000100001554",
		"HTTP":                      strings.Replace(storyURL, "https://", "http://", 1),
		"duplicate links":           storyURL + "\n[同一需求](" + storyURL + "?from=share)",
		"same record preview":       storyURL + "\nhttps://tapd.example.com/tapd_fe/10000000/iteration/card/1001?dialog_preview_id=story_1010000000100001554",
		"other reference":           "仓库 https://git.example.com/org/repo\n需求 " + storyURL,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseTAPDStoryURL(input)
			if err != nil || got != want {
				t.Fatalf("ParseTAPDStoryURL(%q) = %+v, %v; want %+v", input, got, err, want)
			}
		})
	}
}

func TestParseTAPDStoryURLPastedBug(t *testing.T) {
	for _, input := range []string{
		"修复缺陷\nhttps://tapd.example.com/tapd_fe/10000000/bug/detail/1010000000100002109",
		"[缺陷](https://www.tapd.cn/10000000/bugs/view/1010000000100002109)",
		"缺陷：https://tapd.example.com/tapd_fe/10000000/iteration/card/1001?dialog_preview_id=bug_1010000000100002109。",
	} {
		got, err := ParseTAPDStoryURL(input)
		want := TAPDStoryRef{WorkspaceID: "10000000", StoryID: "1010000000100002109", Kind: "bug"}
		if err != nil || got != want {
			t.Fatalf("ParseTAPDStoryURL(%q) = %+v, %v; want %+v", input, got, err, want)
		}
	}
}

func TestParseTAPDStoryURLRejectsAmbiguousPaste(t *testing.T) {
	const storyURL = "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001554"
	for name, other := range map[string]string{
		"another story":     "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001665",
		"another workspace": "https://tapd.example.com/tapd_fe/10132092/story/detail/1010000000100001554",
		"bug instead":       "https://tapd.example.com/tapd_fe/10000000/bug/detail/1010000000100001554",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseTAPDStoryURL("第一条\n" + storyURL + "\n第二条\n" + other)
			if err == nil || !strings.Contains(err.Error(), "多个不同") {
				t.Fatalf("expected explicit ambiguity error, got %v", err)
			}
		})
	}
}

func TestParseTAPDStoryURLRejectsInvalidPaste(t *testing.T) {
	cases := map[string]string{
		"empty":                  " \n ",
		"title only":             "【DP】关闭套餐需求",
		"foreign host":           "需求\nhttps://example.com/tapd_fe/10000000/story/detail/1010000000100001554",
		"host suffix":            "需求\nhttps://tapd.example.com.example.com/tapd_fe/10000000/story/detail/1010000000100001554",
		"bare host prefix":       "需求\nevil-tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001554",
		"userinfo spoof":         "https://tapd.example.com@example.com/tapd_fe/10000000/story/detail/1010000000100001554",
		"embedded TAPD URL":      "https://example.com/redirect?url=https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001554",
		"unsupported scheme":     "ftp://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001554",
		"no record":              "https://tapd.example.com/tapd_fe/10000000/story/list",
		"iteration is not story": "https://tapd.example.com/tapd_fe/10000000/iteration/card/1010000000100001554",
		"invalid preview":        "https://tapd.example.com/tapd_fe/10000000/iteration/card/1010000000100001554?dialog_preview_id=story_invalid",
		"ID suffix":              "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001554invalid",
		"missing workspace":      "https://tapd.example.com/story/detail/1010000000100001554",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseTAPDStoryURL(input); err == nil {
				t.Fatalf("ParseTAPDStoryURL(%q) unexpectedly accepted %+v", input, got)
			}
		})
	}
}
