package knowledge

import "testing"

func TestParseDistilledEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Distilled
	}{
		{"empty", "", Distilled{}},
		{"no front matter no heading", "plain body", Distilled{Body: "plain body"}},
		{"crlf front matter", "---\r\ntitle: T\r\n---\r\nbody", Distilled{Title: "T", Body: "body"}},
		{"unclosed front matter falls back to heading", "---\ntitle: T\n# H\n", Distilled{Title: "H", Body: "---\ntitle: T\n# H\n"}},
		{"empty title uses heading", "---\nsource: s\n---\n\n\n# 标题\n正文", Distilled{Title: "标题", Source: "s", Body: "# 标题\n正文"}},
		{"unknown keys and junk lines ignored", "---\nTitle: 'Q'\nauthor: x\nnot a key\nPATH: a/b.md\n---\nbody", Distilled{Title: "Q", Path: "a/b.md", Body: "body"}},
		{"later key wins", "---\ntitle: a\ntitle: b\n---\n", Distilled{Title: "b"}},
		{"heading with extra hashes", "x\n  ### 深层标题  \n", Distilled{Title: "深层标题", Body: "x\n  ### 深层标题  \n"}},
	}
	for _, tc := range cases {
		if got := ParseDistilled(tc.in); got != tc.want {
			t.Errorf("%s: got %#v want %#v", tc.name, got, tc.want)
		}
	}
}
