package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// staticPublicURL is a fixed external address for handlers that resolve it on
// each use.
func staticPublicURL(address string) func() string {
	return func() string { return address }
}

// publicURLFixture: a store with a platform administrator and the handler that
// serves 平台管理 → 外部访问地址, with env standing in for the deployment's own
// AGENTFLOW_PUBLIC_URL.
func publicURLFixture(t *testing.T, env string) (*repo.Store, *platformSettingsHandlers, domain.User, domain.User) {
	t.Helper()
	t.Setenv("AGENTFLOW_SESSION_SECRET", "platform-public-url-test-secret")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.PutUser(domain.User{ID: 71, LoginName: "platform-admin"})
	s.PutUser(domain.User{ID: 72, LoginName: "plain-member"})
	if _, err := s.GrantPlatformAdmin("platform-admin"); err != nil {
		t.Fatal(err)
	}
	admin, _ := s.User(71)
	member, _ := s.User(72)
	h := &platformSettingsHandlers{deps: &deps{repo: s}, envPublicURL: staticPublicURL(env)}
	return s, h, admin, member
}

func publicURLCall(t *testing.T, h *platformSettingsHandlers, user domain.User, method, body string) (int, publicURLView, string) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/platform/public-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = authedRequest(t, req, user)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	h.publicURL(c)
	var view publicURLView
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	var failure struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &failure)
	return w.Code, view, failure.Error
}

// The address is platform-level configuration: only a platform administrator
// may read or change it.
func TestPublicURLRequiresPlatformAdmin(t *testing.T) {
	_, h, admin, member := publicURLFixture(t, "")
	for _, call := range []struct{ method, body string }{{http.MethodGet, ""}, {http.MethodPut, `{"value":"https://evil.example"}`}, {http.MethodDelete, ""}} {
		if code, _, _ := publicURLCall(t, h, member, call.method, call.body); code != http.StatusForbidden {
			t.Fatalf("%s as a plain member = %d", call.method, code)
		}
	}
	if code, _, _ := publicURLCall(t, h, admin, http.MethodGet, ""); code != http.StatusOK {
		t.Fatalf("GET as admin = %d", code)
	}
}

// A value saved on the page wins over the environment, and the response names
// both so a shadowed deployment value is visible rather than confusing.
func TestPublicURLPageSettingOverridesEnvironment(t *testing.T) {
	s, h, admin, member := publicURLFixture(t, "http://10.0.0.5:8090")
	code, view, _ := publicURLCall(t, h, admin, http.MethodGet, "")
	if code != http.StatusOK || view.Source != publicURLSourceEnv || view.Value != "http://10.0.0.5:8090" || view.Stored != "" {
		t.Fatalf("initial view = %d %+v", code, view)
	}
	code, view, _ = publicURLCall(t, h, admin, http.MethodPut, `{"value":"https://loopforge.example.com/"}`)
	if code != http.StatusOK || view.Source != publicURLSourceSettings || view.Value != "https://loopforge.example.com" {
		t.Fatalf("saved view = %d %+v", code, view)
	}
	if view.EnvValue != "http://10.0.0.5:8090" || view.UpdatedBy != "platform-admin" || view.UpdatedAt == "" {
		t.Fatalf("saved view misses the shadowed env or the actor: %+v", view)
	}
	if stored := s.PlatformSettings(); stored.PublicURL != "https://loopforge.example.com" {
		t.Fatalf("stored = %+v", stored)
	}
	// Clearing falls back to the environment instead of leaving none.
	if _, err := s.GrantPlatformAdmin(member.LoginName); err != nil {
		t.Fatal(err)
	}
	code, view, _ = publicURLCall(t, h, member, http.MethodDelete, "")
	if code != http.StatusOK || view.Source != publicURLSourceEnv || view.Value != "http://10.0.0.5:8090" || view.Stored != "" || view.UpdatedBy != member.LoginName || view.UpdatedAt == "" {
		t.Fatalf("cleared view = %d %+v", code, view)
	}
}

// Saving an empty value is the same as clearing it.
func TestPublicURLEmptyValueClears(t *testing.T) {
	_, h, admin, _ := publicURLFixture(t, "")
	if code, _, _ := publicURLCall(t, h, admin, http.MethodPut, `{"value":"https://loopforge.example.com"}`); code != http.StatusOK {
		t.Fatalf("save = %d", code)
	}
	code, view, _ := publicURLCall(t, h, admin, http.MethodPut, `{"value":"   "}`)
	if code != http.StatusOK || view.Stored != "" || view.Source != publicURLSourceNone || view.UpdatedBy != admin.LoginName {
		t.Fatalf("blank save = %d %+v", code, view)
	}
}

func TestPublicURLRejectsUnusableAddresses(t *testing.T) {
	s, h, admin, _ := publicURLFixture(t, "")
	if err := s.PutPlatformSettings(domain.PlatformSettings{PublicURL: "https://loopforge.example.com", UpdatedBy: "previous-admin"}); err != nil {
		t.Fatal(err)
	}
	previous := s.PlatformSettings()
	for name, value := range map[string]string{
		"no_scheme":    "loopforge.example.com",
		"wrong_proto":  "ftp://loopforge.example.com",
		"credentials":  "https://user:pass@loopforge.example.com",
		"query":        "https://loopforge.example.com?a=1",
		"fragment":     "https://loopforge.example.com#x",
		"path":         "https://loopforge.example.com/platform",
		"no_host":      "https://",
		"invalid_port": "https://loopforge.example.com:65536",
		"listen_any":   "http://0.0.0.0:8090",
		"link_local":   "http://169.254.1.2:8090",
		"too_long":     "https://" + strings.Repeat("a", 520) + ".example.com",
	} {
		code, _, failure := publicURLCall(t, h, admin, http.MethodPut, `{"value":`+strconv.Quote(value)+`}`)
		if code != http.StatusBadRequest || failure == "" {
			t.Errorf("%s (%q) = %d %q", name, value, code, failure)
		}
	}
	if stored := s.PlatformSettings(); stored != previous {
		t.Fatalf("invalid address changed stored settings: got %+v, want %+v", stored, previous)
	}
}

// A loopback address is accepted - it is what a local trial uses - but it
// cannot work in a link sent to somebody's phone, so it is saved with a
// warning instead of silently.
func TestPublicURLWarnsAboutLoopback(t *testing.T) {
	_, h, admin, _ := publicURLFixture(t, "")
	for _, value := range []string{"http://localhost:8090", "http://127.0.0.1:8090"} {
		code, view, _ := publicURLCall(t, h, admin, http.MethodPut, `{"value":`+strconv.Quote(value)+`}`)
		if code != http.StatusOK || view.Value != value || view.Warning == "" {
			t.Fatalf("%s = %d %+v", value, code, view)
		}
	}
}

// The whole point of the page: once an address is saved, the links the
// platform builds resolve it without a restart. notification.Service caches
// only the environment value, so this also guards the resolver.
func TestSavedPublicURLAppliesWithoutRestart(t *testing.T) {
	s, h, admin, _ := publicURLFixture(t, "")
	service := notification.New(s)
	if got := service.PublicAddress(); got != "" {
		t.Fatalf("address before any configuration = %q", got)
	}
	if code, _, _ := publicURLCall(t, h, admin, http.MethodPut, `{"value":"https://loopforge.example.com"}`); code != http.StatusOK {
		t.Fatalf("save = %d", code)
	}
	if got := service.PublicAddress(); got != "https://loopforge.example.com" {
		t.Fatalf("address after saving = %q", got)
	}
	// The same resolver backs the Bot binding link.
	identities := &botIdentityHandlers{deps: &deps{repo: s}, publicURL: service.PublicAddress}
	if got := identities.platformAddress(); got != "https://loopforge.example.com" {
		t.Fatalf("binding link base = %q", got)
	}
	if code, _, _ := publicURLCall(t, h, admin, http.MethodDelete, ""); code != http.StatusOK {
		t.Fatal("clear failed")
	}
	if got := service.PublicAddress(); got != "" {
		t.Fatalf("address after clearing = %q", got)
	}
}
