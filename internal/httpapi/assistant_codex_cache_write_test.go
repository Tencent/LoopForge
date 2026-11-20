package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/schedule"
	"agentflow/internal/tracker"
)

func TestScheduledCodexCacheWriteUsage(t *testing.T) {
	for _, withTotal := range []bool{false, true} {
		t.Run(fmt.Sprintf("total_%v", withTotal), func(t *testing.T) {
			h, user := workbenchFixture(t)
			models := []domain.CLIModel{{ID: "gpt-5.6-sol", Name: "Sol"}}
			if _, err := h.repo.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{CodexAPIKey: "test-only", CodexModels: &models}); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(t.TempDir(), "codex")
			t.Setenv("AGENTFLOW_CODEX_BIN", bin)
			job := domain.ScheduleJob{ID: "cache-write-job", ProjectID: "workbench", OwnerUserID: user.ID, RunAsUserID: user.ID, Name: "inspect", SkipDispatch: true, AssistantPrompt: "检查文件", AssistantModel: "codex:gpt-5.6-sol", SessionMode: domain.ScheduleSessionFixed, Enabled: true}
			if err := h.repo.PutScheduleJob(job); err != nil {
				t.Fatal(err)
			}
			svc := schedule.New(h.repo, tracker.NewService(), nil)
			svc.SetAssistantSessionPrep(h.runScheduleJobPromptResult)
			var sessionID string
			for attempt := range 2 {
				usageEvent := func(last codexTokenUsage, total codexTokenUsage) string {
					usage := map[string]any{"last": last}
					if withTotal {
						usage["total"] = total
					}
					return codexLine(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "write-thread", "turnId": "write-turn", "tokenUsage": usage}})
				}
				prior := int64(attempt)
				first := usageEvent(codexTokenUsage{InputTokens: 100000, OutputTokens: 10000, CachedInputTokens: 20000, CacheWriteInputTokens: 30000, TotalTokens: 110000}, codexTokenUsage{InputTokens: 100000 + prior*120000, OutputTokens: 10000 + prior*15000, CachedInputTokens: 20000 + prior*25000, CacheWriteInputTokens: 30000 + prior*35000, TotalTokens: 110000 + prior*135000})
				second := usageEvent(codexTokenUsage{InputTokens: 20000, OutputTokens: 5000, CachedInputTokens: 5000, CacheWriteInputTokens: 5000, TotalTokens: 25000}, codexTokenUsage{InputTokens: (prior + 1) * 120000, OutputTokens: (prior + 1) * 15000, CachedInputTokens: (prior + 1) * 25000, CacheWriteInputTokens: (prior + 1) * 35000, TotalTokens: (prior + 1) * 135000})
				events := first + "\n" + second
				if withTotal {
					events += "\n" + second
				}
				end := `{"method":"item/completed","params":{"threadId":"write-thread","turnId":"write-turn","item":{"type":"agentMessage","id":"answer","text":"检查完成，没有问题。"}}}
{"method":"turn/completed","params":{"threadId":"write-thread","turn":{"id":"write-turn","status":"completed"}}}`
				if attempt == 1 {
					end = `{"method":"turn/completed","params":{"threadId":"write-thread","turn":{"id":"write-turn","status":"failed","error":{"message":"request failed"}}}}`
				}
				script := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"thread/start"'*|*'"method":"thread/resume"'*) echo '{"id":2,"result":{"thread":{"id":"write-thread"}}}' ;;
    *'"method":"turn/start"'*)
      cat <<'JSON'
{"method":"turn/started","params":{"threadId":"write-thread","turn":{"id":"write-turn"}}}
{"id":3,"result":{"turn":{"id":"write-turn"}}}
` + events + "\n" + end + `
JSON
      exit 0 ;;
  esac
done
`
				if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				out, err := svc.RunNow(job.ID)
				if (err != nil) != (attempt == 1) {
					t.Fatalf("out=%+v err=%v", out, err)
				}
				e, ok := h.repo.ScheduleJobExecution(domain.ScheduleJobExecutionID(out.LastTraceID))
				if !ok || e.Usage == nil || e.Usage.TotalTokens != 135000 || e.Usage.CacheWriteTokens != 35000 || e.Usage.CacheReadTokens != 25000 || e.CostCNY == nil || math.Abs(*e.CostCNY-5.075) > 1e-9 {
					t.Fatalf("record=%+v usage=%+v", e, e.Usage)
				}
				if sessionID == "" {
					sessionID = e.SessionID
				} else if e.SessionID != sessionID {
					t.Fatal("fixed session changed")
				}
			}
			current, _ := h.repo.ScheduleJobExecutionOverview([]string{job.ID}, time.Now())
			if current.Total != 2 || current.Success != 1 || current.Failed != 1 || current.Metered != 2 || current.Priced != 2 || current.Tokens.Total != 270000 || current.Tokens.CacheWrite != 70000 || math.Abs(current.CostCNY-10.15) > 1e-9 {
				t.Fatalf("overview=%+v", current)
			}
		})
	}
}

func TestCodexCacheWriteProtocolFields(t *testing.T) {
	s := codexAppServer{threadID: "thread", turnID: "turn", turnPending: true}
	first := `{"threadId":"thread","turnId":"turn","tokenUsage":{"last":{"inputTokens":100000,"outputTokens":10000,"cachedInputTokens":20000,"cacheWriteInputTokens":30000,"totalTokens":110000},"total":{"inputTokens":100000,"outputTokens":10000,"cachedInputTokens":20000,"cacheWriteInputTokens":30000,"totalTokens":110000}}}`
	second := `{"threadId":"thread","turnId":"turn","tokenUsage":{"last":{"inputTokens":20000,"outputTokens":5000,"cachedInputTokens":5000,"cacheWriteInputTokens":5000,"totalTokens":25000},"total":{"inputTokens":120000,"outputTokens":15000,"cachedInputTokens":25000,"cacheWriteInputTokens":35000,"totalTokens":135000}}}`
	for i, raw := range []string{first, second, second} {
		s.notification("thread/tokenUsage/updated", json.RawMessage(raw))
		u := runtime.ParseTokenUsage(s.usage)
		wantTotal, wantWrite := int64(135000), int64(35000)
		if i == 0 {
			wantTotal, wantWrite = 110000, 30000
		}
		if u.TotalTokens != wantTotal || u.CacheWriteTokens != wantWrite {
			t.Fatalf("event %d usage=%+v", i, u)
		}
	}
	e := domain.ScheduleJobExecution{}
	u := runtime.ParseTokenUsage(s.usage)
	e.RecordUsage("codex:gpt-5.6-sol", &u)
	if e.CostCNY == nil || math.Abs(*e.CostCNY-5.075) > 1e-9 {
		t.Fatalf("priced usage=%+v", e.Usage)
	}
}
