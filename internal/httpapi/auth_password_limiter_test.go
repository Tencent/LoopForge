package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// limiterServer is a full API server with one existing local account.
func limiterServer(t *testing.T) func(path, body string) int {
	t.Helper()
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.CreateLocalAccount("owner", "a-long-enough-password", true); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), nil, "", "")
	return func(path, body string) int {
		r := httptest.NewRequest(http.MethodPost, "http://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
}

const wrongLogin = `{"login":"owner","password":"not-the-password"}`

func spendBudget(t *testing.T, call func(path, body string) int, path, body string, want int) {
	t.Helper()
	for i := 0; i < passwordAttemptLimit; i++ {
		if code := call(path, body); code != want {
			t.Fatalf("attempt %d on %s = %d, want %d", i+1, path, code, want)
		}
	}
}

func TestLoginCountsEveryAttempt(t *testing.T) {
	call := limiterServer(t)
	// Malformed login requests are credential probes too: they count.
	spendBudget(t, call, "/api/auth/password", `{"login":`, http.StatusBadRequest)
	if code := call("/api/auth/password", `{"login":"owner","password":"a-long-enough-password"}`); code != http.StatusTooManyRequests {
		t.Fatalf("login after %d malformed attempts = %d, want 429", passwordAttemptLimit, code)
	}
}

func TestRegistrationCountsOnlyValidRequests(t *testing.T) {
	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "true")
	call := limiterServer(t)
	// Malformed or invalid sign-ups reveal and create nothing: they are free.
	for i, body := range []string{`{`, `not json`, `{"login":"-bad","password":"a-long-enough-password"}`, `{"login":"ok-user","password":"short"}`} {
		for j := 0; j < passwordAttemptLimit; j++ {
			if code := call("/api/auth/register", body); code != http.StatusBadRequest {
				t.Fatalf("invalid sign-up %d/%d = %d, want 400", i, j, code)
			}
		}
	}
	// So the shared budget is intact for a real login.
	if code := call("/api/auth/password", `{"login":"owner","password":"a-long-enough-password"}`); code != http.StatusOK {
		t.Fatalf("login after free invalid sign-ups = %d", code)
	}
	// Valid sign-ups reach the accounts (and "taken" discloses one): they
	// count, and share the budget with login, which already spent one.
	for i := 1; i < passwordAttemptLimit; i++ {
		if code := call("/api/auth/register", `{"login":"owner","password":"a-long-enough-password"}`); code != http.StatusConflict {
			t.Fatalf("valid sign-up %d = %d, want 409", i+1, code)
		}
	}
	if code := call("/api/auth/register", `{"login":"fresh-user","password":"a-long-enough-password"}`); code != http.StatusTooManyRequests {
		t.Fatalf("sign-up after the budget = %d, want 429", code)
	}
	if code := call("/api/auth/password", wrongLogin); code != http.StatusTooManyRequests {
		t.Fatalf("login after sign-ups spent the budget = %d, want 429", code)
	}
}

func TestPasswordBudgetIsPerServer(t *testing.T) {
	first, second := limiterServer(t), limiterServer(t)
	spendBudget(t, first, "/api/auth/password", wrongLogin, http.StatusUnauthorized)
	if code := first("/api/auth/password", wrongLogin); code != http.StatusTooManyRequests {
		t.Fatalf("first server not limited: %d", code)
	}
	if code := second("/api/auth/password", wrongLogin); code != http.StatusUnauthorized {
		t.Fatalf("second server inherited the first server's attempts: %d", code)
	}
}

func TestPasswordLimiterWindowAndSourceCap(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newPasswordLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < passwordAttemptLimit; i++ {
		if !l.allow("198.51.100.1") {
			t.Fatalf("attempt %d refused", i+1)
		}
	}
	if l.allow("198.51.100.1") {
		t.Fatal("attempt over the limit allowed")
	}
	now = now.Add(passwordAttemptWindow + time.Second)
	if !l.allow("198.51.100.1") {
		t.Fatal("budget did not reset after the window")
	}
	for i := 1; i < passwordAttemptSources; i++ {
		l.allow("10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256))
	}
	if l.allow("203.0.113.9") {
		t.Fatal("a new source was admitted past the tracking cap")
	}
	if !l.allow("198.51.100.1") {
		t.Fatal("a tracked source lost its remaining budget at the cap")
	}
}
