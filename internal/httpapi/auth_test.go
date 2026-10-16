package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newTestAuthServer(t *testing.T, authClient *auth.Client) (*gin.Engine, *repo.Store) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	e := engine.New(s, nil)
	srv := &Server{
		deps: &deps{repo: s, engine: e, authClient: authClient},
	}
	srv.auth = &authHandlers{deps: srv.deps}
	srv.users = &usersHandlers{deps: srv.deps}

	g := gin.New()
	g.GET("/api/auth/current", srv.auth.authCurrent)
	g.GET("/api/auth/exchange", srv.auth.authExchange)
	g.POST("/api/auth/exchange", srv.auth.authExchange)
	g.POST("/api/auth/logout", srv.auth.authLogout)
	g.GET("/api/auth/photo", srv.auth.authPhoto)
	g.GET("/api/auth/photo/:spec/:username", srv.auth.authPhoto)
	g.GET("/api/users", srv.users.users)
	return g, s
}

func TestAuthCurrent_NoCookie(t *testing.T) {
	authClient := auth.NewClient(auth.Config{})
	handler, _ := newTestAuthServer(t, authClient)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	if !body.User.IsGuest || body.User.ChineseName != "访客" {
		t.Errorf("expected guest user, got %+v", body.User)
	}
}

func TestAuthExchange_MissingCode(t *testing.T) {
	authClient := auth.NewClient(auth.Config{})
	handler, _ := newTestAuthServer(t, authClient)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/exchange", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAuthFlow_Success(t *testing.T) {
	// Mock TOF backend
	mockTOF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := auth.IOAAuthResponse{
			Ret: 0,
			Data: &auth.IOAAuthData{
				StaffId:     54321,
				LoginName:   "alexzhao",
				ChineseName: "赵小龙",
				DeptName:    "研发一部",
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockTOF.Close()

	authClient := auth.NewClient(auth.Config{
		PaaSID:        "test_paas",
		AppToken:      "test_token",
		AuthHost:      mockTOF.URL,
		AuthURI:       "/ebus/tof4/api/v1/passport/AccessToken",
		AppDomain:     "dns-afp.example.com",
		SessionSecret: "secret123",
	})
	authClient.SetHTTPClient(mockTOF.Client())

	handler, st := newTestAuthServer(t, authClient)

	// 1. Exchange code
	req := httptest.NewRequest(http.MethodGet, "/api/auth/exchange?code=my_valid_code", nil)
	req.Host = "dns-afp.example.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var exchangeBody struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &exchangeBody); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if exchangeBody.User.IsGuest || exchangeBody.User.ChineseName != "赵小龙" || exchangeBody.User.LoginName != "alexzhao" {
		t.Errorf("unexpected user from exchange: %+v", exchangeBody.User)
	}

	// 验证用户已被持久化至 users 表（store）
	storedUser, ok := st.UserByLoginName("alexzhao")
	if !ok {
		t.Fatalf("expected user alexzhao to be persisted in store")
	}
	if storedUser.ChineseName != "赵小龙" || storedUser.StaffID != 54321 {
		t.Errorf("unexpected persisted user: %+v", storedUser)
	}

	// 验证 /api/users 列表包含该用户
	usersReq := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	usersRec := httptest.NewRecorder()
	handler.ServeHTTP(usersRec, usersReq)
	if usersRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /api/users, got %d", usersRec.Code)
	}
	var usersList []domain.User
	if err := json.Unmarshal(usersRec.Body.Bytes(), &usersList); err != nil {
		t.Fatalf("unmarshal users list: %v", err)
	}
	if _, ok := st.UserByLoginName("alexzhao"); len(usersList) < 1 || !ok {
		t.Errorf("unexpected users list: %+v", usersList)
	}

	// Verify cookie was set
	cookies := rec.Result().Cookies()
	var authCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "agentflow_user" {
			authCookie = c
			break
		}
	}
	if authCookie == nil {
		t.Fatalf("expected agentflow_user cookie to be set")
	}
	if authCookie.Domain != "dns-afp.example.com" {
		t.Errorf("expected cookie domain dns-afp.example.com, got %s", authCookie.Domain)
	}

	// 2. Call /api/auth/current with cookie
	curReq := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	curReq.AddCookie(authCookie)
	curRec := httptest.NewRecorder()
	handler.ServeHTTP(curRec, curReq)

	if curRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", curRec.Code)
	}
	var curBody struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(curRec.Body.Bytes(), &curBody); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if curBody.User.IsGuest || curBody.User.ChineseName != "赵小龙" {
		t.Errorf("unexpected current user: %+v", curBody.User)
	}

	// 3. Logout
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutReq.Host = "dns-afp.example.com"
	logoutRec := httptest.NewRecorder()
	handler.ServeHTTP(logoutRec, logoutReq)

	if logoutRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on logout, got %d", logoutRec.Code)
	}
	logoutCookies := logoutRec.Result().Cookies()
	var clearedCookie *http.Cookie
	for _, c := range logoutCookies {
		if c.Name == "agentflow_user" {
			clearedCookie = c
			break
		}
	}
	if clearedCookie == nil || clearedCookie.MaxAge != -1 {
		t.Errorf("expected cookie to be cleared (MaxAge=-1), got %+v", clearedCookie)
	}
}

func TestAuthCurrent_TamperedCookie(t *testing.T) {
	authClient := auth.NewClient(auth.Config{
		SessionSecret: "secret123",
	})
	handler, _ := newTestAuthServer(t, authClient)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	req.AddCookie(&http.Cookie{
		Name:  "agentflow_user",
		Value: "tampered.jwt.or.payload",
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	if !body.User.IsGuest || body.User.ChineseName != "访客" {
		t.Errorf("expected graceful fallback to guest user on tampered cookie, got %+v", body.User)
	}
}

func TestAuthCurrent_GatewayHeader(t *testing.T) {
	t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "true")
	authClient := auth.NewClient(auth.Config{
		AppDomain:     "dns-afp.example.com",
		SessionSecret: "test-secret-123",
	})
	handler, st := newTestAuthServer(t, authClient)

	// 1. 模拟太湖 / AIO-Forward 网关透传 Staffname 和 Staffid 请求头
	req := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Staffname", "example-maintainer")
	req.Header.Set("Staffid", "88888")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if body.User.IsGuest {
		t.Errorf("expected isGuest to be false, got true")
	}
	if body.User.LoginName != "example-maintainer" || body.User.StaffID != 88888 {
		t.Errorf("unexpected gateway user: %+v", body.User)
	}

	// 验证用户已被自动持久化到数据库 users 表
	u, ok := st.UserByLoginName("example-maintainer")
	if !ok {
		t.Fatalf("expected gateway user to be saved into store")
	}
	if u.StaffID != 88888 {
		t.Errorf("expected staffId 88888 in store, got %d", u.StaffID)
	}

	// 验证已自动签发会话 Cookie
	cookies := rec.Result().Cookies()
	var authCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "agentflow_user" {
			authCookie = c
			break
		}
	}
	if authCookie == nil {
		t.Fatalf("expected agentflow_user cookie to be set for gateway request")
	}

	// 2. 验证小写 Header (staffname) 兼容性
	req2 := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	req2.RemoteAddr = "127.0.0.1:12346"
	req2.Header.Set("staffname", "loweruser")
	req2.Header.Set("staffid", "99999")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 for lowercase header, got %d", rec2.Code)
	}
	var body2 struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &body2); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if body2.User.LoginName != "loweruser" || body2.User.StaffID != 99999 {
		t.Errorf("unexpected user from lowercase header: %+v", body2.User)
	}
}

func TestAuthCurrent_IgnoresUntrustedGatewayHeader(t *testing.T) {
	t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "false")
	authClient := auth.NewClient(auth.Config{SessionSecret: "test-secret-123"})
	handler, st := newTestAuthServer(t, authClient)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	req.Header.Set("Staffname", "forged-user")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.User.IsGuest {
		t.Fatalf("untrusted identity header authenticated as %+v", body.User)
	}
	if _, ok := st.UserByLoginName("forged-user"); ok {
		t.Fatal("untrusted identity header persisted a user")
	}
}

func TestAuthCookieSecureBehindHTTPSProxy(t *testing.T) {
	s := &Server{deps: &deps{authClient: auth.NewClient(auth.Config{})}}
	s.auth = &authHandlers{deps: s.deps}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/api/auth/current", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	s.auth.setAuthCookie(rec, req, "signed")
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("HTTPS proxy response must set a Secure cookie: %+v", cookies)
	}
}

func TestAuthCurrent_FallbackWithLoginURL(t *testing.T) {
	authClient := auth.NewClient(auth.Config{
		PaaSID:    "ngn_test_paas",
		AppDomain: "dns-afp.example.com",
	})
	handler, _ := newTestAuthServer(t, authClient)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/current?return_url=https://dns-afp.example.com/#/dashboard", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body struct {
		User     auth.User `json:"user"`
		LoginURL string    `json:"loginUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	if !body.User.IsGuest || body.User.ChineseName != "访客" {
		t.Errorf("expected guest user, got %+v", body.User)
	}
	if body.LoginURL == "" {
		t.Errorf("expected non-empty loginUrl")
	}
	if body.LoginURL != "/login.html" {
		t.Errorf("expected passport.example.com in loginUrl, got %s", body.LoginURL)
	}
	if body.LoginURL != "/login.html" {
		t.Errorf("expected paasID in loginUrl, got %s", body.LoginURL)
	}
}

func TestAuthCurrent_CookieUserMissingInStore_AutoPersist(t *testing.T) {
	authClient := auth.NewClient(auth.Config{
		AppDomain:     "dns-afp.example.com",
		SessionSecret: "secret-cookie-persist",
	})
	handler, st := newTestAuthServer(t, authClient)

	// 构造已登录的合法 Cookie，但此时 store 中没有该用户
	cookieUser := &auth.User{
		StaffID:     667788,
		LoginName:   "cookie_only_user",
		ChineseName: "临时会话员",
		DeptID:      999,
		DeptName:    "云架构部",
		IsGuest:     false,
		AuthTime:    time.Now().UTC(),
	}
	token, err := authClient.SignUser(cookieUser)
	if err != nil {
		t.Fatalf("sign user failed: %v", err)
	}

	// 确认调用前 store 中没有此用户
	if _, ok := st.UserByLoginName("cookie_only_user"); ok {
		t.Fatalf("expected user to NOT exist in store before request")
	}

	// 发起 /api/auth/current 请求
	req := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	req.AddCookie(&http.Cookie{
		Name:  "agentflow_user",
		Value: token,
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	if body.User.IsGuest {
		t.Fatalf("expected authenticated user, got guest")
	}
	if body.User.ID <= 0 {
		t.Errorf("expected user ID > 0 after auto-persist, got %d", body.User.ID)
	}

	// 验证 store 中已被自动补齐写表
	persisted, ok := st.UserByLoginName("cookie_only_user")
	if !ok {
		t.Fatalf("expected cookie_only_user to be persisted into store")
	}
	if persisted.ID != body.User.ID || persisted.ChineseName != "临时会话员" {
		t.Errorf("unexpected persisted user data: %+v", persisted)
	}
}

func TestAuthPhoto_Success(t *testing.T) {
	fakePNG := []byte("\x89PNG\r\n\x1a\nphoto_data_123")
	mockTOF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ebus/rhrc/photo/100/nonexistent.png" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path != "/ebus/rhrc/photo/100/zhangsan.png" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("x-rio-paasid") != "test_paas" {
			t.Errorf("expected x-rio-paasid, got %s", r.Header.Get("x-rio-paasid"))
		}
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fakePNG)
	}))
	defer mockTOF.Close()

	authClient := auth.NewClient(auth.Config{
		PaaSID:   "test_paas",
		AppToken: "test_token",
		AuthHost: mockTOF.URL,
	})
	authClient.SetHTTPClient(mockTOF.Client())

	handler, _ := newTestAuthServer(t, authClient)

	// 1. 测试路径参数形式 GET /api/auth/photo/100/zhangsan.png
	req := httptest.NewRequest(http.MethodGet, "/api/auth/photo/100/zhangsan.png", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("expected image/png, got %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Errorf("expected cache-control, got %s", rec.Header().Get("Cache-Control"))
	}
	if rec.Body.String() != string(fakePNG) {
		t.Errorf("expected fake png body, got %s", rec.Body.String())
	}

	// 2. 测试查询参数形式 GET /api/auth/photo?username=zhangsan&spec=100
	reqQuery := httptest.NewRequest(http.MethodGet, "/api/auth/photo?username=zhangsan&spec=100", nil)
	recQuery := httptest.NewRecorder()
	handler.ServeHTTP(recQuery, reqQuery)

	if recQuery.Code != http.StatusOK {
		t.Fatalf("expected 200 for query params, got %d", recQuery.Code)
	}

	// 3. 测试 404
	req404 := httptest.NewRequest(http.MethodGet, "/api/auth/photo/100/nonexistent.png", nil)
	rec404 := httptest.NewRecorder()
	handler.ServeHTTP(rec404, req404)

	if rec404.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec404.Code)
	}

	// 4. 测试缺少 username
	reqBad := httptest.NewRequest(http.MethodGet, "/api/auth/photo", nil)
	recBad := httptest.NewRecorder()
	handler.ServeHTTP(recBad, reqBad)

	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when username missing, got %d", recBad.Code)
	}
}

func TestAuthCurrent_IncludesAvatarURL(t *testing.T) {
	authClient := auth.NewClient(auth.Config{
		SessionSecret: "test-secret",
	})
	handler, st := newTestAuthServer(t, authClient)

	_ = st.PutUser(domain.User{
		ID:          1,
		LoginName:   "alex",
		ChineseName: "亚历克斯",
	})

	user := &auth.User{
		ID:          1,
		LoginName:   "alex",
		ChineseName: "亚历克斯",
		IsGuest:     false,
	}
	token, err := authClient.SignUser(user)
	if err != nil {
		t.Fatalf("sign user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	req.AddCookie(&http.Cookie{
		Name:  "agentflow_user",
		Value: token,
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if body.User.AvatarURL != "/api/auth/photo/100/alex.png" {
		t.Errorf("expected avatarUrl /api/auth/photo/100/alex.png, got %q", body.User.AvatarURL)
	}
}
