package httpapi

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/schedule"
	"agentflow/internal/tracker"
)

func TestScheduledNativeUsageReproduction(t *testing.T) {
	h, user := workbenchFixture(t)
	if _, err := h.repo.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{CodexAPIKey: "test-codex-key"}); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "codex")
	t.Setenv("AGENTFLOW_CODEX_BIN", bin)
	job := domain.ScheduleJob{ID: "native-usage-job", ProjectID: "workbench", OwnerUserID: user.ID, RunAsUserID: user.ID, Name: "inspect", SkipDispatch: true, AssistantPrompt: "检查文件", AssistantModel: "codex:gpt-5.5", Enabled: true, SessionMode: domain.ScheduleSessionFixed}
	if err := h.repo.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	svc := schedule.New(h.repo, tracker.NewService(), nil)
	svc.SetAssistantSessionPrep(h.runScheduleJobPromptResult)
	var session string
	for _, tc := range []struct {
		name, events string
		failed       bool
		total        int64
		cost         float64
	}{
		{name: "codex_two_calls", events: `{"method":"thread/tokenUsage/updated","params":{"threadId":"native-thread","turnId":"native-turn","tokenUsage":{"last":{"inputTokens":100000,"cachedInputTokens":40000,"outputTokens":10000,"totalTokens":110000},"total":{"inputTokens":100000,"cachedInputTokens":40000,"outputTokens":10000,"totalTokens":110000}}}}
{"method":"thread/tokenUsage/updated","params":{"threadId":"native-thread","turnId":"native-turn","tokenUsage":{"last":{"inputTokens":20000,"outputTokens":10000,"totalTokens":30000},"total":{"inputTokens":120000,"cachedInputTokens":40000,"outputTokens":20000,"totalTokens":140000}}}}`, total: 140000, cost: 7.14},
		{name: "codex_fixed_failed", events: `{"method":"thread/tokenUsage/updated","params":{"threadId":"native-thread","turnId":"native-turn","tokenUsage":{"last":{"inputTokens":100000,"cachedInputTokens":40000,"outputTokens":10000,"totalTokens":110000},"total":{"inputTokens":220000,"cachedInputTokens":80000,"outputTokens":30000,"totalTokens":250000}}}}`, failed: true, total: 110000, cost: 4.34},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end := `{"method":"item/completed","params":{"threadId":"native-thread","turnId":"native-turn","item":{"type":"agentMessage","id":"answer","text":"检查结果正常。"}}}
{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"completed"}}}`
			if tc.failed {
				end = `{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"failed","error":{"message":"request failed"}}}}`
			}
			script := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"thread/start"'*|*'"method":"thread/resume"'*) echo '{"id":2,"result":{"thread":{"id":"native-thread"}}}' ;;
    *'"method":"turn/start"'*)
      cat <<'JSON'
{"method":"turn/started","params":{"threadId":"native-thread","turn":{"id":"native-turn"}}}
{"id":3,"result":{"turn":{"id":"native-turn"}}}
` + tc.events + "\n" + end + `
JSON
      exit 0 ;;
  esac
done
`
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			out, err := svc.RunNow(job.ID)
			if (err != nil) != tc.failed {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			e, ok := h.repo.ScheduleJobExecution(domain.ScheduleJobExecutionID(out.LastTraceID))
			if !ok || e.Usage == nil || e.ExecutionTotalTokens() != tc.total || e.CostCNY == nil || math.Abs(*e.CostCNY-tc.cost) > 1e-9 {
				t.Fatalf("record=%+v usage=%+v", e, e.Usage)
			}
			if session == "" {
				session = e.SessionID
			} else if e.SessionID != session {
				t.Fatalf("fixed session changed: %s != %s", e.SessionID, session)
			}
		})
	}
	models := []domain.CLIModel{{ID: "claude-sonnet-4-6", Name: "Sonnet"}}
	if _, err := h.repo.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{CodeBuddyModels: &models}); err != nil {
		t.Fatal(err)
	}
	job.ID, job.SessionMode, job.AssistantModel = "cache-usage-job", "", "claude-sonnet-4-6"
	if err := h.repo.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	codebuddy := filepath.Join(t.TempDir(), "codebuddy")
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", codebuddy)
	if err := os.WriteFile(codebuddy, []byte("#!/bin/sh\necho '{\"type\":\"result\",\"is_error\":true,\"result\":\"request failed\",\"usage\":{\"cache_read_input_tokens\":40000}}'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := svc.RunNow(job.ID)
	if err == nil {
		t.Fatal("expected CodeBuddy failure")
	}
	e, ok := h.repo.ScheduleJobExecution(domain.ScheduleJobExecutionID(out.LastTraceID))
	if !ok || e.Usage == nil || e.Usage.CacheReadTokens != 40000 || e.CostCNY == nil {
		t.Fatalf("cache record=%+v usage=%+v", e, e.Usage)
	}
	current, _ := h.repo.ScheduleJobExecutionOverview([]string{"native-usage-job", job.ID}, time.Now())
	if current.Total != 3 || current.Metered != 3 || current.Priced != 3 || current.Tokens.Total != 250000 || current.Tokens.CacheRead != 120000 || math.Abs(current.CostCNY-11.56) > 1e-9 {
		t.Fatalf("overview=%+v", current)
	}
}
