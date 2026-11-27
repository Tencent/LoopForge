package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"
)

func TestExecutionMachinePrivilegedSandboxSettings(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	owner := domain.User{ID: 95, LoginName: "machine-owner"}
	if err := f.s.PutUser(owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GrantPlatformAdmin(f.user.LoginName); err != nil {
		t.Fatal(err)
	}
	const origin = "http://localhost"
	request := func(user domain.User, method, path, origin, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req = authedRequest(t, req, user)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s from %q got %d, want %d: %s", method, path, user.LoginName, rec.Code, want, rec.Body)
		}
		return rec
	}
	bind := request(owner, http.MethodPost, "/api/runners/bind-codes", origin, "{}", http.StatusOK)
	var issued struct{ Code string }
	if err := json.Unmarshal(bind.Body.Bytes(), &issued); err != nil || issued.Code == "" {
		t.Fatalf("missing bind code: %v %s", err, bind.Body)
	}
	rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/register", map[string]any{
		"name": "Owner's machine", "version": domain.RunnerVersionPrivilegedSandbox,
	}, map[string]string{"X-Bootstrap-Token": issued.Code})
	var registered struct{ RunnerID, SessionToken string }
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); rec.Code != http.StatusOK || err != nil || registered.RunnerID == "" {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	_, poolSession := registerTestRunner(t, f.h)
	const platformPath = "/api/runners/platform/privileged-sandbox"
	path := "/api/runners/" + registered.RunnerID + "/privileged-sandbox"
	const on = `{"allowed":true}`
	const off = `{"allowed":false}`
	request(owner, http.MethodPut, platformPath, origin, on, http.StatusForbidden)
	request(f.user, http.MethodPut, path, origin, on, http.StatusForbidden)
	request(owner, http.MethodPut, path, "https://other.example", on, http.StatusForbidden)
	request(f.user, http.MethodPut, platformPath, "", on, http.StatusForbidden)
	request(owner, http.MethodPut, path, origin, "{}", http.StatusBadRequest)

	const settingPath = "/runners/privileged-sandbox"
	readSetting := func(session string) bool {
		t.Helper()
		rec := requestWithHeaders(t, f.h, http.MethodGet, settingPath, nil, map[string]string{"Authorization": "Bearer " + session})
		var setting struct{ Allowed *bool }
		if err := json.Unmarshal(rec.Body.Bytes(), &setting); rec.Code != http.StatusOK || err != nil || setting.Allowed == nil || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("read machine setting: %d %s", rec.Code, rec.Body)
		}
		return *setting.Allowed
	}
	request(owner, http.MethodGet, settingPath, origin, "", http.StatusUnauthorized)
	if readSetting(registered.SessionToken) || readSetting(poolSession) {
		t.Fatal("new personal and platform machines should default to off")
	}
	request(owner, http.MethodPut, path, origin, on, http.StatusNoContent)
	rec = requestWithHeaders(t, f.h, http.MethodPost, "/runners/heartbeat", map[string]any{}, map[string]string{"Authorization": "Bearer " + registered.SessionToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", rec.Code, rec.Body)
	}
	rec = request(owner, http.MethodGet, "/api/runners?mine=1", origin, "", http.StatusOK)
	var machines []domain.Runner
	if err := json.Unmarshal(rec.Body.Bytes(), &machines); err != nil || len(machines) != 1 || !machines[0].AllowPrivilegedSandbox {
		t.Fatalf("heartbeat lost the saved permission: %s", rec.Body)
	}

	if err := f.s.SetPlatformRunnerRunLimit(4, f.user.ID); err != nil {
		t.Fatal(err)
	}
	request(f.user, http.MethodPut, platformPath, origin, on, http.StatusNoContent)
	if !readSetting(poolSession) {
		t.Fatal("pool machine did not get the enabled platform policy")
	}
	request(f.user, http.MethodPut, platformPath, origin, off, http.StatusNoContent)
	if got := f.s.PlatformRunnerRunLimit(); got != 4 {
		t.Fatalf("privileged sandbox write changed the run limit to %d", got)
	}
	if readSetting(poolSession) {
		t.Fatal("pool machine did not get the platform policy")
	}
	if !readSetting(registered.SessionToken) {
		t.Fatal("platform write changed the owner's machine setting")
	}
	request(owner, http.MethodPut, path, origin, off, http.StatusNoContent)
	if readSetting(registered.SessionToken) {
		t.Fatal("owner's deny did not reach the Runner")
	}
}
