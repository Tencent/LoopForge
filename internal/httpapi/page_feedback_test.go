package httpapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

var (
	feedbackPNG  = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{0}, 32)...)
	feedbackGIF  = append([]byte("GIF89a"), bytes.Repeat([]byte{1}, 32)...)
	feedbackWebP = append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{2}, 32)...)
)

type feedbackEnv struct {
	t     *testing.T
	store *repo.Store
	h     http.Handler
	alice domain.User
	bob   domain.User
}

func newFeedbackEnv(t *testing.T) *feedbackEnv {
	t.Helper()
	t.Setenv("AGENTFLOW_SESSION_SECRET", "page-feedback-test-secret")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutUser(domain.User{LoginName: "alice", ChineseName: "爱丽丝"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(domain.User{LoginName: "bob"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := s.UserByLoginName("alice")
	bob, _ := s.UserByLoginName("bob")
	if alice.ID <= 0 || bob.ID <= 0 {
		t.Fatalf("users not stored: %+v %+v", alice, bob)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	return &feedbackEnv{t: t, store: s, h: h, alice: alice, bob: bob}
}

func (e *feedbackEnv) cookie(u auth.User) *http.Cookie {
	e.t.Helper()
	if u.AuthTime.IsZero() {
		u.AuthTime = time.Now().UTC()
	}
	signed, err := auth.NewClientFromEnv().SignUser(&u)
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: authCookieName, Value: signed}
}

func (e *feedbackEnv) session(u domain.User) *http.Cookie {
	return e.cookie(auth.User{ID: u.ID, LoginName: u.LoginName, ChineseName: u.ChineseName})
}

type feedbackFile struct {
	name string
	data []byte
}

func feedbackForm(t *testing.T, payload any, files ...feedbackFile) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	raw, ok := payload.(string)
	if !ok {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	}
	if err := w.WriteField("payload", raw); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		part, err := w.CreateFormFile("images", f.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(f.data)
	}
	_ = w.Close()
	return &body, w.FormDataContentType()
}

func (e *feedbackEnv) submit(c *http.Cookie, payload any, files ...feedbackFile) *httptest.ResponseRecorder {
	e.t.Helper()
	body, contentType := feedbackForm(e.t, payload, files...)
	req := httptest.NewRequest(http.MethodPost, "/api/page-feedback", body)
	req.Header.Set("Content-Type", contentType)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *feedbackEnv) get(path string, c *http.Cookie, bearer string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *feedbackEnv) count() int64 {
	e.t.Helper()
	_, total, err := e.store.PageFeedbacks(1, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return total
}

func decodeFeedback(t *testing.T, rec *httptest.ResponseRecorder) domain.PageFeedback {
	t.Helper()
	var fb domain.PageFeedback
	if err := json.Unmarshal(rec.Body.Bytes(), &fb); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return fb
}

func TestPageFeedbackSubmitStoresSessionSubmitterAndIgnoresPayloadIdentity(t *testing.T) {
	e := newFeedbackEnv(t)
	issuesBefore := len(e.store.Issues())
	payload := map[string]any{
		"title":       "运行详情「重试」按钮错位",
		"description": "运行详情「重试」按钮错位\n在 1280 宽度下与状态徽标重叠",
		"url":         "https://flow.test/?code=abc&tab=1&access_token=x#/runs/r1?ticket=t&view=log",
		"route":       "#/runs/r1?session=s&view=log",
		"view":        "运行记录",
		"frameUrl":    "https://user:pw@flow.test/chatbot/?projectId=p1&token=zzz",
		"context": map[string]any{
			"scroll": map[string]any{"x": 0, "y": 320}, "viewport": map[string]any{"width": 1280, "height": 800},
			"dpr": 2, "userAgent": "UA", "language": "zh-CN", "theme": "dark", "capturedAt": "2026-10-31T12:00:00Z",
			"cookies": "should-be-dropped",
		},
		"element": map[string]any{"selector": "#retry", "tag": "button", "text": strings.Repeat("字", 300), "inFrame": true,
			"rect": map[string]any{"x": 1, "y": 2, "width": 3, "height": 4}},
		"userId": 999, "loginName": "mallory", "chineseName": "冒名", "submitter": map[string]any{"userId": 999, "loginName": "mallory"},
	}
	rec := e.submit(e.session(e.alice), payload, feedbackFile{"shot.png", feedbackPNG}, feedbackFile{"anim.gif", feedbackGIF}, feedbackFile{"x.webp", feedbackWebP})
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	created := decodeFeedback(t, rec)
	want := domain.PageFeedbackUser{UserID: e.alice.ID, LoginName: "alice", ChineseName: "爱丽丝"}
	if created.Submitter != want {
		t.Fatalf("submitter = %+v, want %+v", created.Submitter, want)
	}
	if len(e.store.Issues()) != issuesBefore {
		t.Fatal("page feedback must not create issues")
	}

	stored, found, err := e.store.PageFeedback(created.ID)
	if err != nil || !found {
		t.Fatalf("stored found=%v err=%v", found, err)
	}
	if stored.Submitter != want {
		t.Fatalf("stored submitter = %+v", stored.Submitter)
	}
	if stored.Title != payload["title"] || !strings.Contains(stored.Description, "状态徽标重叠") {
		t.Fatalf("title/description = %q / %q", stored.Title, stored.Description)
	}
	for _, leaked := range []string{"code=", "access_token", "ticket=", "session=", "token=", "user:pw"} {
		if strings.Contains(stored.URL+stored.Route+stored.FrameURL, leaked) {
			t.Fatalf("sensitive %q survived: url=%q route=%q frame=%q", leaked, stored.URL, stored.Route, stored.FrameURL)
		}
	}
	if stored.URL != "https://flow.test/?tab=1#/runs/r1?view=log" || stored.Route != "#/runs/r1?view=log" || stored.FrameURL != "https://flow.test/chatbot/?projectId=p1" {
		t.Fatalf("sanitized url=%q route=%q frame=%q", stored.URL, stored.Route, stored.FrameURL)
	}
	if stored.Context.Theme != "dark" || stored.Context.Viewport.Width != 1280 || stored.Context.DPR != 2 {
		t.Fatalf("context = %+v", stored.Context)
	}
	if stored.Element == nil || !stored.Element.InFrame || len([]rune(stored.Element.Text)) != 200 || stored.Element.Rect.Width != 3 {
		t.Fatalf("element = %+v", stored.Element)
	}
	if len(stored.Images) != 3 || stored.Images[0].MimeType != "image/png" || stored.Images[1].MimeType != "image/gif" || stored.Images[2].MimeType != "image/webp" {
		t.Fatalf("images = %+v", stored.Images)
	}
	if !strings.Contains(rec.Body.String(), `"url":"/api/page-feedback/`+created.ID+`/images/`) {
		t.Fatalf("response lacks image urls: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "should-be-dropped") || strings.Contains(rec.Body.String(), "mallory") {
		t.Fatalf("unknown or forged fields leaked: %s", rec.Body.String())
	}

	// The submitter name is a snapshot: renaming the user later keeps the stored value.
	if err := e.store.PutUser(domain.User{ID: e.alice.ID, LoginName: "alice", ChineseName: "新名字"}); err != nil {
		t.Fatal(err)
	}
	detail := e.get("/api/page-feedback/"+created.ID, e.session(e.alice), "")
	if detail.Code != http.StatusOK || decodeFeedback(t, detail).Submitter.ChineseName != "爱丽丝" {
		t.Fatalf("detail after rename status=%d body=%s", detail.Code, detail.Body.String())
	}
}

func TestPageFeedbackChineseNameFallsBackToRTX(t *testing.T) {
	e := newFeedbackEnv(t)
	rec := e.submit(e.session(e.bob), map[string]any{"title": "没有中文名"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := decodeFeedback(t, rec).Submitter
	if got.LoginName != "bob" || got.ChineseName != "bob" || got.UserID != e.bob.ID {
		t.Fatalf("submitter = %+v", got)
	}
}

func TestPageFeedbackRejectsGuestsAnonymousAndExpiredSessions(t *testing.T) {
	e := newFeedbackEnv(t)
	ok := e.submit(e.session(e.alice), map[string]any{"title": "seed"}, feedbackFile{"a.png", feedbackPNG})
	if ok.Code != http.StatusCreated {
		t.Fatalf("seed status=%d body=%s", ok.Code, ok.Body.String())
	}
	seeded := decodeFeedback(t, ok)
	paths := []string{
		"/api/page-feedback",
		"/api/page-feedback/" + seeded.ID,
		"/api/page-feedback/" + seeded.ID + "/images/" + seeded.Images[0].ID,
	}
	cases := map[string]*http.Cookie{
		"anonymous": nil,
		"guest":     e.cookie(auth.User{LoginName: "guest", ChineseName: "访客", IsGuest: true}),
		"expired":   e.cookie(auth.User{ID: e.alice.ID, LoginName: "alice", AuthTime: time.Now().UTC().Add(-authSessionTTL - time.Hour)}),
		"forged":    {Name: authCookieName, Value: "not-a-signed-cookie"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := e.submit(c, map[string]any{"title": "should not be stored"})
			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "请先登录") {
				t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
			}
			for _, path := range paths {
				if rec := e.get(path, c, ""); rec.Code != http.StatusUnauthorized {
					t.Fatalf("GET %s status=%d body=%s", path, rec.Code, rec.Body.String())
				}
			}
		})
	}
	if n := e.count(); n != 1 {
		t.Fatalf("rejected submissions were stored: %d rows", n)
	}
}

func TestPageFeedbackReadEndpointsAcceptPersonalToken(t *testing.T) {
	e := newFeedbackEnv(t)
	created := decodeFeedback(t, e.submit(e.session(e.alice), map[string]any{"title": "alice 的反馈"}, feedbackFile{"a.png", feedbackPNG}))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/tokens", strings.NewReader(`{"name":"script","expiresInDays":30}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(e.session(e.bob))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token status=%d body=%s", rec.Code, rec.Body.String())
	}
	var token struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &token)

	list := e.get("/api/page-feedback", nil, token.Token)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"loginName":"alice"`) || !strings.Contains(list.Body.String(), `"chineseName":"爱丽丝"`) {
		t.Fatalf("bob must see alice's feedback via token: status=%d body=%s", list.Code, list.Body.String())
	}
	if rec := e.get("/api/page-feedback/"+created.ID, nil, token.Token); rec.Code != http.StatusOK {
		t.Fatalf("detail via token status=%d", rec.Code)
	}
	if rec := e.get("/api/page-feedback/"+created.ID+"/images/"+created.Images[0].ID, nil, token.Token); rec.Code != http.StatusOK {
		t.Fatalf("image via token status=%d", rec.Code)
	}
	if rec := e.get("/api/page-feedback", nil, "afp_invalid"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status=%d", rec.Code)
	}

	before := e.count()
	body, contentType := feedbackForm(t, map[string]any{"title": "脚本提交"})
	submit := httptest.NewRequest(http.MethodPost, "/api/page-feedback", body)
	submit.Header.Set("Content-Type", contentType)
	submit.Header.Set("Authorization", "Bearer "+token.Token)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, submit)
	if rec.Code != http.StatusUnauthorized || e.count() != before {
		t.Fatalf("submit must refuse personal tokens: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPageFeedbackValidation(t *testing.T) {
	e := newFeedbackEnv(t)
	alice := e.session(e.alice)
	big := append(append([]byte{}, feedbackPNG...), bytes.Repeat([]byte{0}, 5<<20)...)
	six := make([]feedbackFile, 6)
	for i := range six {
		six[i] = feedbackFile{"p.png", feedbackPNG}
	}
	cases := []struct {
		name    string
		payload any
		files   []feedbackFile
		want    string
	}{
		{"missing title", map[string]any{"title": "  ", "description": "x"}, nil, "请填写反馈内容"},
		{"title too long", map[string]any{"title": strings.Repeat("长", 121)}, nil, "标题不能超过"},
		{"description too long", map[string]any{"title": "t", "description": strings.Repeat("d", 5001)}, nil, "反馈内容不能超过"},
		{"malformed payload", "{not json", nil, "格式不正确"},
		{"html disguised as png", map[string]any{"title": "t"}, []feedbackFile{{"evil.png", []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")}}, "不是支持的图片格式"},
		{"svg", map[string]any{"title": "t"}, []feedbackFile{{"logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)}}, "不是支持的图片格式"},
		{"oversized image", map[string]any{"title": "t"}, []feedbackFile{{"ok.png", feedbackPNG}, {"big.png", big}}, "big.png 超过 5MB"},
		{"too many images", map[string]any{"title": "t"}, six, "最多上传 5 张图片"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.submit(alice, tc.payload, tc.files...)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("status=%d body=%s, want 400 containing %q", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
	if n := e.count(); n != 0 {
		t.Fatalf("invalid submissions were stored: %d", n)
	}

	body, contentType := feedbackForm(t, map[string]any{"title": "t"}, feedbackFile{"huge.png", append(append([]byte{}, feedbackPNG...), bytes.Repeat([]byte{0}, 28<<20)...)})
	req := httptest.NewRequest(http.MethodPost, "/api/page-feedback", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(alice)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := e.count(); n != 0 {
		t.Fatalf("oversized submission was stored: %d", n)
	}
}

func TestPageFeedbackListPagingAndImageResponse(t *testing.T) {
	e := newFeedbackEnv(t)
	alice := e.session(e.alice)
	var ids []string
	for _, title := range []string{"one", "two", "three"} {
		rec := e.submit(alice, map[string]any{"title": title}, feedbackFile{"p.png", feedbackPNG})
		if rec.Code != http.StatusCreated {
			t.Fatalf("submit %s status=%d", title, rec.Code)
		}
		ids = append(ids, decodeFeedback(t, rec).ID)
		time.Sleep(2 * time.Millisecond)
	}

	var page struct {
		Items   []domain.PageFeedback `json:"items"`
		Total   int64                 `json:"total"`
		Offset  int                   `json:"offset"`
		Limit   int                   `json:"limit"`
		HasMore bool                  `json:"hasMore"`
	}
	rec := e.get("/api/page-feedback?limit=2", alice, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Limit != 2 || !page.HasMore || len(page.Items) != 2 || page.Items[0].Title != "three" {
		t.Fatalf("page 1 = %s", rec.Body.String())
	}
	if page.Items[0].Submitter.LoginName != "alice" || page.Items[0].Submitter.ChineseName != "爱丽丝" || page.Items[0].Submitter.UserID != e.alice.ID {
		t.Fatalf("list submitter = %+v", page.Items[0].Submitter)
	}
	if len(page.Items[0].Images) != 1 || page.Items[0].Images[0].URL == "" {
		t.Fatalf("list images = %+v", page.Items[0].Images)
	}
	rec = e.get("/api/page-feedback?limit=2&offset=2", alice, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if page.HasMore || len(page.Items) != 1 || page.Items[0].Title != "one" || page.Offset != 2 {
		t.Fatalf("page 2 = %s", rec.Body.String())
	}
	rec = e.get("/api/page-feedback?limit=1000&offset=-3", alice, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if page.Limit != 100 || page.Offset != 0 {
		t.Fatalf("limit/offset not clamped: %s", rec.Body.String())
	}

	if rec := e.get("/api/page-feedback/fb-missing", alice, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing detail status=%d", rec.Code)
	}
	first, _, _ := e.store.PageFeedback(ids[0])
	img := e.get("/api/page-feedback/"+ids[0]+"/images/"+first.Images[0].ID, alice, "")
	if img.Code != http.StatusOK || !bytes.Equal(img.Body.Bytes(), feedbackPNG) {
		t.Fatalf("image status=%d len=%d", img.Code, img.Body.Len())
	}
	for header, want := range map[string]string{
		"Content-Type":            "image/png",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'",
		"Content-Disposition":     "inline",
		"Cache-Control":           "private",
	} {
		if got := img.Header().Get(header); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if rec := e.get("/api/page-feedback/"+ids[1]+"/images/"+first.Images[0].ID, alice, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("image of another feedback status=%d", rec.Code)
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/page-feedback/"+ids[0], nil)
		req.AddCookie(alice)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK || rec.Code == http.StatusNoContent {
			t.Fatalf("%s must not modify feedback, got %d", method, rec.Code)
		}
	}
}

func TestSanitizeFeedbackURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://a.test/p?q=1&Token=x&api-key=k&keyword=ok#/x?auth_code=1&y=2": "https://a.test/p?q=1&keyword=ok#/x?y=2",
		"javascript:alert(1)":           "",
		"/relative/path":                "",
		"http://a.test/?sessionId=s":    "http://a.test/",
		"https://a.test/#/issues?code=": "https://a.test/#/issues",
	} {
		if got := sanitizeFeedbackURL(raw); got != want {
			t.Errorf("sanitizeFeedbackURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
