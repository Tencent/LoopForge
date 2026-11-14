package httpapi

import (
	"encoding/base64"
	"testing"

	"agentflow/internal/domain"
)

func TestWorkbenchArtifactContent(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	pngB64 := base64.StdEncoding.EncodeToString(png)
	large := base64.StdEncoding.EncodeToString(append(append([]byte{}, png...), make([]byte, 700<<10)...))
	for _, tc := range []struct {
		name                     string
		args                     workbenchArtifactArgs
		body, format, kind, fail string
	}{
		{name: "plain doc", args: workbenchArtifactArgs{Body: "# hi"}, body: "# hi", format: "markdown", kind: "doc"},
		{name: "trimmed explicit", args: workbenchArtifactArgs{Body: "{}", Format: " json ", Kind: " doc "}, body: "{}", format: "json", kind: "doc"},
		{name: "prototype html", args: workbenchArtifactArgs{Body: "<p>x</p>", Kind: "prototype"}, body: "<p>x</p>", format: "html", kind: "prototype"},
		{name: "ppt markdown", args: workbenchArtifactArgs{Body: "slides", Kind: "ppt"}, body: "slides", format: "markdown", kind: "ppt"},
		{name: "url only browser", args: workbenchArtifactArgs{URL: " https://example.com "}, format: "markdown", kind: "browser"},
		{name: "url with body doc", args: workbenchArtifactArgs{URL: "https://example.com", Body: "x"}, body: "x", format: "markdown", kind: "doc"},
		{name: "local path with url allowed", args: workbenchArtifactArgs{URL: "https://example.com", Body: "/tmp/a.html"}, body: "/tmp/a.html", format: "markdown", kind: "doc"},
		{name: "image", args: workbenchArtifactArgs{ImageBase64: "data:image/png;base64," + pngB64}, body: pngB64, format: "png", kind: "file"},
		{name: "image keeps kind", args: workbenchArtifactArgs{ImageBase64: pngB64, Kind: "prototype", Format: "html", Body: "/tmp/x"}, body: pngB64, format: "png", kind: "prototype"},
		{name: "bad image", args: workbenchArtifactArgs{ImageBase64: "%%%"}, fail: "imageBase64 不是有效的 png、jpeg、gif 或 webp"},
		{name: "large image", args: workbenchArtifactArgs{ImageBase64: large}, fail: "图片不能超过 700 KB，侧边栏才能直接打开"},
		{name: "not an image", args: workbenchArtifactArgs{ImageBase64: base64.StdEncoding.EncodeToString([]byte("hello"))}, fail: "只接受 png、jpeg、gif 或 webp"},
		{name: "local path", args: workbenchArtifactArgs{Body: "/tmp/demo.html"}, fail: "不要提交本机路径。把 HTML、Markdown 或图片内容本身传进来。"},
		{name: "bad format", args: workbenchArtifactArgs{Body: "x", Format: "pdf"}, fail: "format 只能是 markdown、html、json、text 或图片"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, format, kind, reject := workbenchArtifactContent(tc.args)
			if reject != tc.fail {
				t.Fatalf("reject = %q, want %q", reject, tc.fail)
			}
			if tc.fail != "" {
				return
			}
			if body != tc.body || format != tc.format || kind != tc.kind {
				t.Fatalf("got (%q, %q, %q), want (%q, %q, %q)", body, format, kind, tc.body, tc.format, tc.kind)
			}
		})
	}
}

func TestWorkbenchArtifactAuthor(t *testing.T) {
	for _, tc := range []struct {
		user domain.User
		want string
	}{
		{domain.User{ChineseName: " 张三 ", LoginName: "zhangsan"}, "张三"},
		{domain.User{ChineseName: " ", LoginName: " zhangsan "}, "zhangsan"},
		{domain.User{}, spaceAIAuthor},
	} {
		if got := workbenchArtifactAuthor(tc.user); got != tc.want {
			t.Errorf("workbenchArtifactAuthor(%+v) = %q, want %q", tc.user, got, tc.want)
		}
	}
}
