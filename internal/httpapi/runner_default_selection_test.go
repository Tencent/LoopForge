package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// PUT /api/runners/default must apply the same rules as the runners page:
// an offline or outdated machine cannot become the default.
func TestRunnerDefaultRejectsOfflineAndOutdatedMachines(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	runnerID, _ := registerTestRunner(t, f.h)
	runner, _ := f.s.Runner(runnerID)
	runner.OwnerUserID = f.user.ID

	put := func(id string) (int, map[string]any) {
		t.Helper()
		rec := cvBRequest(t, f, http.MethodPut, "/api/runners/default", map[string]any{"runnerId": id}, true)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	save := func(status, version string) {
		t.Helper()
		runner.Status, runner.RunnerVersion = status, version
		if err := f.s.PutRunner(runner); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name, status, version, want string
	}{
		{"offline", domain.RunnerStatusOffline, domain.RunnerVersionDefaultSelection, "机器离线"},
		{"outdated", domain.RunnerStatusOnline, "0.1.0", "0.1.0 过低"},
		{"unknown version", domain.RunnerStatusOnline, "", "未知 过低"},
	} {
		save(tc.status, tc.version)
		code, out := put(runnerID)
		msg, _ := out["error"].(string)
		if code != http.StatusConflict || !strings.Contains(msg, tc.want) {
			t.Errorf("%s: PUT default = %d %v, want 409 containing %q", tc.name, code, out, tc.want)
		}
		if got := f.s.DefaultRunner(f.user.ID); got != "" {
			t.Fatalf("%s: rejected machine became default: %q", tc.name, got)
		}
	}

	save(domain.RunnerStatusOnline, domain.RunnerVersionDefaultSelection)
	if code, out := put(runnerID); code != http.StatusOK || out["runnerId"] != runnerID {
		t.Fatalf("eligible machine: PUT default = %d %v", code, out)
	}
	if code, out := put(""); code != http.StatusOK || out["message"] != "已切回云端执行" {
		t.Fatalf("clear default = %d %v, want 已切回云端执行", code, out)
	}

	rec := cvBRequest(t, f, http.MethodGet, "/api/runners/default", nil, true)
	var def map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &def); err != nil || def["minRunnerVersion"] != domain.RunnerVersionDefaultSelection {
		t.Fatalf("GET default should expose minRunnerVersion: %d %s", rec.Code, rec.Body.String())
	}
}
