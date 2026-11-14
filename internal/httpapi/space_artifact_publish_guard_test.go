package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// These tests pin publishWorkbenchArtifact's wiring around the extracted
// workbenchArtifactContent / workbenchArtifactAuthor helpers: what reaches the
// store, what the model is told, and that a rejection never publishes.

func publishGuardResult(t *testing.T, result map[string]any) (string, bool) {
	t.Helper()
	content, ok := result["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("unexpected tool result shape: %#v", result)
	}
	text, _ := content[0]["text"].(string)
	isError, _ := result["isError"].(bool)
	return text, isError
}

func publishGuardSetup(t *testing.T, roomID string) (cvBHTTPFixture, *agentToolGateway, domain.ChatRoom) {
	t.Helper()
	f := cvBNewHTTPFixture(t)
	room := domain.ChatRoom{ID: roomID, ProjectID: "platform", Name: "Publish guard"}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	return f, &agentToolGateway{deps: &deps{repo: f.s}}, room
}

func artifactMessages(f cvBHTTPFixture, roomID string) []domain.ChatMessage {
	var out []domain.ChatMessage
	for _, m := range f.s.Messages(roomID, 200) {
		if m.Kind == domain.ChatKindArtifact {
			out = append(out, m)
		}
	}
	return out
}

func TestPublishWorkbenchArtifactStoresNormalizedImage(t *testing.T) {
	f, gateway, room := publishGuardSetup(t, "publish-guard-image")
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 'd', 'a', 't', 'a'}
	pngB64 := base64.StdEncoding.EncodeToString(png)
	user := f.user
	user.ChineseName, user.LoginName = " 张三 ", "zhangsan"
	session := &AgentToolSession{Mode: AgentToolAssistant, SpaceID: room.ID, ProjectID: room.ProjectID, UserID: f.user.ID, User: user}
	raw, _ := json.Marshal(map[string]string{
		"title":       "架构图",
		"imageBase64": "data:image/png;base64," + pngB64,
		"summary":     "  一张图  ",
		"url":         "  https://example.test/a  ",
	})

	result, err := gateway.publishWorkbenchArtifact(session, raw)
	if err != nil {
		t.Fatal(err)
	}
	text, isError := publishGuardResult(t, result)
	if isError {
		t.Fatalf("publish failed: %s", text)
	}
	items := f.s.SpaceArtifacts(room.ID)
	if len(items) != 1 {
		t.Fatalf("stored artifacts = %d, want 1", len(items))
	}
	got := items[0]
	if got.Kind != "file" || got.Format != "png" || got.Body != pngB64 {
		t.Fatalf("stored kind/format/body = %q/%q/%q, want file/png/re-encoded image", got.Kind, got.Format, got.Body)
	}
	if got.Title != "架构图" || got.Summary != "一张图" || got.URL != "https://example.test/a" {
		t.Fatalf("stored title/summary/url = %q/%q/%q", got.Title, got.Summary, got.URL)
	}
	if got.AuthorName != "张三" || got.AuthorID != f.user.ID || !got.ByAI || got.SpaceID != room.ID || got.Version != 1 {
		t.Fatalf("stored author/space fields = %+v", got)
	}
	if want := "/api/spaces/" + room.ID + "/artifacts/" + got.ID + "/raw"; !strings.Contains(text, want) || !strings.Contains(text, "类型：file") {
		t.Fatalf("result text %q does not point at %s", text, want)
	}
	msgs := artifactMessages(f, room.ID)
	if len(msgs) != 1 || msgs[0].ArtifactID != got.ID || msgs[0].ArtifactVersion != 1 || msgs[0].Body != "发布了「架构图」" || msgs[0].Author != spaceAIAuthor {
		t.Fatalf("artifact chat messages = %+v", msgs)
	}
}

func TestPublishWorkbenchArtifactAuthorAndSummaryFallbacks(t *testing.T) {
	f, gateway, room := publishGuardSetup(t, "publish-guard-author")
	longSummary := strings.Repeat("长", 600)
	for _, tc := range []struct {
		name string
		user domain.User
		want string
	}{
		{"login name", domain.User{LoginName: " lisi "}, "lisi"},
		{"generic", domain.User{}, spaceAIAuthor},
	} {
		session := &AgentToolSession{Mode: AgentToolAssistant, SpaceID: room.ID, ProjectID: room.ProjectID, UserID: f.user.ID, User: tc.user}
		raw, _ := json.Marshal(map[string]string{"title": tc.name, "body": "# hi", "summary": longSummary})
		result, err := gateway.publishWorkbenchArtifact(session, raw)
		if err != nil {
			t.Fatal(err)
		}
		if text, isError := publishGuardResult(t, result); isError {
			t.Fatalf("%s: publish failed: %s", tc.name, text)
		}
		var stored domain.SpaceArtifact
		for _, item := range f.s.SpaceArtifacts(room.ID) {
			if item.Title == tc.name {
				stored = item
			}
		}
		if stored.AuthorName != tc.want {
			t.Errorf("%s: author = %q, want %q", tc.name, stored.AuthorName, tc.want)
		}
		if stored.Kind != "doc" || stored.Format != "markdown" || stored.Body != "# hi" {
			t.Errorf("%s: kind/format/body = %q/%q/%q", tc.name, stored.Kind, stored.Format, stored.Body)
		}
		if want := strings.Repeat("长", 500) + "…"; stored.Summary != want {
			t.Errorf("%s: summary has %d runes, want trimmed to 500 + ellipsis", tc.name, len([]rune(stored.Summary)))
		}
	}
}

func TestPublishWorkbenchArtifactRejectionsPublishNothing(t *testing.T) {
	f, gateway, room := publishGuardSetup(t, "publish-guard-reject")
	session := &AgentToolSession{Mode: AgentToolAssistant, SpaceID: room.ID, ProjectID: room.ProjectID, UserID: f.user.ID, User: f.user}
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	tooLarge := base64.StdEncoding.EncodeToString(append(append([]byte{}, png...), make([]byte, 700<<10)...))
	for _, tc := range []struct {
		name string
		args map[string]string
		want string
	}{
		{"local path", map[string]string{"title": "x", "body": "/tmp/demo.html"}, "不要提交本机路径。把 HTML、Markdown 或图片内容本身传进来。"},
		{"bad format", map[string]string{"title": "x", "body": "x", "format": "pdf"}, "format 只能是 markdown、html、json、text 或图片"},
		{"bad image", map[string]string{"title": "x", "imageBase64": "%%%"}, "imageBase64 不是有效的 png、jpeg、gif 或 webp"},
		{"not an image", map[string]string{"title": "x", "imageBase64": base64.StdEncoding.EncodeToString([]byte("hello"))}, "只接受 png、jpeg、gif 或 webp"},
		{"too large", map[string]string{"title": "x", "imageBase64": tooLarge}, "图片不能超过 700 KB，侧边栏才能直接打开"},
		{"title too long", map[string]string{"title": strings.Repeat("题", 121), "body": "x"}, "产物标题不能超过 120 个字"},
		{"bad url", map[string]string{"title": "x", "url": "ftp://example.test/a"}, "产物链接必须是 http(s) 地址或站内路径"},
	} {
		raw, _ := json.Marshal(tc.args)
		result, err := gateway.publishWorkbenchArtifact(session, raw)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		text, isError := publishGuardResult(t, result)
		if !isError || strings.TrimSpace(text) != tc.want {
			t.Errorf("%s: result = (%q, isError=%v), want error %q", tc.name, text, isError, tc.want)
		}
	}
	if items := f.s.SpaceArtifacts(room.ID); len(items) != 0 {
		t.Fatalf("rejected publishes stored %d artifacts: %+v", len(items), items)
	}
	if msgs := artifactMessages(f, room.ID); len(msgs) != 0 {
		t.Fatalf("rejected publishes posted %d artifact messages", len(msgs))
	}
}

func TestWorkbenchArtifactContentImageSizeLimitIsInclusive(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	atLimit := append(append([]byte{}, png...), make([]byte, (700<<10)-len(png))...)
	body, format, kind, reject := workbenchArtifactContent(workbenchArtifactArgs{ImageBase64: base64.StdEncoding.EncodeToString(atLimit)})
	if reject != "" || format != "png" || kind != "file" || body != base64.StdEncoding.EncodeToString(atLimit) {
		t.Fatalf("700 KB image: reject=%q format=%q kind=%q", reject, format, kind)
	}
	overLimit := append(atLimit, 0)
	if _, _, _, reject := workbenchArtifactContent(workbenchArtifactArgs{ImageBase64: base64.StdEncoding.EncodeToString(overLimit)}); reject != "图片不能超过 700 KB，侧边栏才能直接打开" {
		t.Fatalf("700 KB + 1 byte image reject = %q", reject)
	}
}
