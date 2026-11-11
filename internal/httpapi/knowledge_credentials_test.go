package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"golang.org/x/crypto/ssh"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKnowledgeSourceCredentialNeverReturned(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "test-private-source-encryption-key")
	s, h := newKBMgmtServer(t)
	createProject(t, h, "private")
	payload := `{"projectId":"private","baseId":"kb-private","gitUrl":"https://example.com/private.git","credential":{"kind":"https","username":"private-user","token":"private-secret-token","canWrite":false}}`
	rec := doJSON(t, h, http.MethodPost, "/api/knowledge/bases/sources", payload, nil)
	if rec.Code != 200 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	var src struct {
		ID         string `json:"id"`
		Configured bool   `json:"credentialConfigured"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	if !src.Configured {
		t.Fatal("来源应显示凭据已配置")
	}
	for _, path := range []string{"/api/knowledge/bases?projectId=private", "/api/knowledge/sources?projectId=private"} {
		response := doJSON(t, h, http.MethodGet, path, "", nil)
		for _, secret := range []string{"private-user", "private-secret-token", "ciphertext"} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatalf("响应泄露凭据 %s", path)
			}
		}
	}
	if strings.Contains(rec.Body.String(), "private-secret-token") {
		t.Fatal("登记响应泄露令牌")
	}
	stored, _ := s.KnowledgeSource(src.ID)
	raw, _ := json.Marshal(stored)
	if strings.Contains(string(raw), "private-secret-token") {
		t.Fatal("来源序列化泄露令牌")
	}
	// 重复登记时省略凭据必须保留已有凭据。
	rec = doJSON(t, h, http.MethodPost, "/api/knowledge/bases/sources", `{"projectId":"private","baseId":"kb-private","gitUrl":"https://example.com/private.git"}`, nil)
	if !strings.Contains(rec.Body.String(), `"credentialConfigured":true`) {
		t.Fatalf("重复登记丢失凭据: %s", rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodDelete, "/api/knowledge/bases/sources?projectId=private&sourceId="+src.ID, "", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if _, found := s.KnowledgeSource(src.ID); found {
		t.Fatal("删除后来源仍存在")
	}
}

func TestKnowledgeCredentialRequiresProjectMembership(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "test-encryption-key")
	s, h := newKBMgmtServer(t)
	session := func(login string) *http.Cookie {
		r := httptest.NewRequest("POST", "http://localhost/api/auth/register", strings.NewReader(`{"login":"`+login+`","password":"secret1"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		r.RemoteAddr = "192.0.2.10:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("register %d %s", w.Code, w.Body.String())
		}
		return w.Result().Cookies()[0]
	}
	session("admin")
	owner, member, viewer, other := session("owner"), session("member"), session("viewer"), session("other")
	ou, _ := s.UserByLoginName("owner")
	mu, _ := s.UserByLoginName("member")
	vu, _ := s.UserByLoginName("viewer")
	if err := s.PutProject(domain.Project{ID: "restricted", Name: "Restricted", OwnerID: ou.ID, Members: []domain.ProjectMember{{UserID: mu.ID, Role: "member"}, {UserID: vu.ID, Role: "viewer"}}}); err != nil {
		t.Fatal(err)
	}
	svc := knowledge.NewService(s, nil, nil)
	if _, err := svc.EnsureHostedBase("restricted"); err != nil {
		t.Fatal(err)
	}
	payload := `{"projectId":"restricted","baseId":"kb-restricted","gitUrl":"https://example.com/private.git","credential":{"kind":"https","username":"user","token":"test-secret"}}`
	var id string
	for _, tc := range []struct {
		cookie *http.Cookie
		want   int
	}{{other, 403}, {viewer, 403}, {owner, 200}, {member, 200}} {
		r := httptest.NewRequest("POST", "http://localhost/api/knowledge/bases/sources", strings.NewReader(payload))
		r.AddCookie(tc.cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("credential write got %d want %d: %s", w.Code, tc.want, w.Body.String())
		}
		if w.Code == 200 {
			var out struct {
				ID string `json:"id"`
			}
			json.Unmarshal(w.Body.Bytes(), &out)
			id = out.ID
		}
	}
	for _, cookie := range []*http.Cookie{other, viewer} {
		body := `{"projectId":"restricted","sourceId":"` + id + `","credential":{"kind":"https","username":"user","token":"replacement"}}`
		r := httptest.NewRequest("PUT", "http://localhost/api/knowledge/bases/sources/credential", strings.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("unauthorized replacement: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestKnowledgeCredentialEncryptedAndBoundToSource(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "test-at-rest-key")
	s, h := newKBMgmtServer(t)
	createProject(t, h, "encrypted")
	payload := `{"projectId":"encrypted","baseId":"kb-encrypted","gitUrl":"https://example.com/private.git","credential":{"kind":"https","username":"deploy","token":"at-rest-secret","canWrite":true}}`
	response := doJSON(t, h, "POST", "/api/knowledge/bases/sources", payload, nil)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	json.Unmarshal(response.Body.Bytes(), &out)
	var row struct {
		Ciphertext string `gorm:"column:credential_ciphertext"`
	}
	if err := s.DB.Table("knowledge_sources").Select("credential_ciphertext").Where("id = ?", out.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Ciphertext == "" || strings.Contains(row.Ciphertext, "at-rest-secret") {
		t.Fatal("存储未加密")
	}
	grant, err := knowledge.DecryptGitCredential(out.ID, row.Ciphertext)
	if err != nil || grant.Token != "at-rest-secret" || !grant.CanWrite {
		t.Fatalf("worker cannot use stored grant: %v", err)
	}
	if _, err := knowledge.DecryptGitCredential("another-source", row.Ciphertext); err == nil {
		t.Fatal("凭据被跨来源复用")
	}
	t.Setenv("AGENTFLOW_SESSION_SECRET", "rotated-key")
	if _, err := knowledge.DecryptGitCredential(out.ID, row.Ciphertext); err == nil || strings.Contains(err.Error(), "at-rest-secret") {
		t.Fatal("密钥更换应返回不含凭据的解密错误")
	}
}

func TestKnowledgeSSHSourceCredentialAndInvalidGrant(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "ssh-api-test-key")
	s, h := newKBMgmtServer(t)
	createProject(t, h, "ssh-private")
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	grant := knowledge.GitCredential{Kind: "ssh", PrivateKey: string(pem.EncodeToMemory(block)), KnownHosts: "git.example.com " + string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}
	input := knowledge.GitSourceInput{ProjectID: "ssh-private", BaseID: "kb-ssh-private", GitURL: "git@git.example.com:team/docs.git", Credential: &grant}
	body, _ := json.Marshal(input)
	response := doJSON(t, h, "POST", "/api/knowledge/bases/sources", string(body), nil)
	if response.Code != 200 {
		t.Fatalf("SSH registration: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "PRIVATE KEY") || strings.Contains(response.Body.String(), "knownHosts") {
		t.Fatal("SSH API 回显了凭据")
	}
	var out struct {
		ID         string `json:"id"`
		Configured bool   `json:"credentialConfigured"`
	}
	json.Unmarshal(response.Body.Bytes(), &out)
	stored, _ := s.KnowledgeSource(out.ID)
	decoded, err := knowledge.DecryptGitCredential(out.ID, stored.CredentialCiphertext)
	if err != nil || decoded.PrivateKey != grant.PrivateKey || !out.Configured || stored.GitURL != input.GitURL {
		t.Fatalf("SSH grant not available to worker: %v", err)
	}
	// Invalid credentials must not create partially registered sources.
	input.GitURL = "ssh://git@git.example.com/invalid.git"
	grant.PrivateKey = "invalid-private-key-canary"
	body, _ = json.Marshal(input)
	response = doJSON(t, h, "POST", "/api/knowledge/bases/sources", string(body), nil)
	if response.Code != 400 || strings.Contains(response.Body.String(), grant.PrivateKey) {
		t.Fatalf("invalid SSH key response: %d %s", response.Code, response.Body.String())
	}
	if len(s.KnowledgeSources(input.BaseID)) != 2 {
		t.Fatal("凭据校验失败后留下来源")
	}
}
