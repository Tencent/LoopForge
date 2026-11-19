package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestSteerRejectsSecondRequestForSameGeneration(t *testing.T) {
	h, user := workbenchFixture(t)
	const sessionID = "steer-two-tabs"
	session := domain.AssistantSession{ID: sessionID, Model: "default", Messages: []assistantMessage{
		{Role: "user", Content: "original"},
	}}
	g := domain.AssistantGeneration{ID: "running-turn", SessionID: sessionID, UserID: user.ID, Status: "running"}
	if err := h.repo.BeginAssistantGeneration(g, session); err != nil {
		t.Fatal(err)
	}
	steer := func(content string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(domain.AssistantSteer{Content: content})
		path := "/api/assistant/sessions/" + sessionID + "/generation/steer?generationId=" + g.ID
		c, rec := workbenchContext(user, "POST", path, bytes.NewBuffer(body))
		c.Params = gin.Params{{Key: "id", Value: sessionID}}
		h.steerGeneration(c)
		return rec
	}
	if rec := steer("first tab"); rec.Code != 200 {
		t.Fatalf("first steer: %d %s", rec.Code, rec.Body)
	}
	first, _ := h.repo.PeekAssistantGeneration(user.ID, sessionID)
	if rec := steer("second tab"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "已有待处理的引导") {
		t.Fatalf("second steer must report a pending steer: %d %s", rec.Code, rec.Body)
	}
	current, _ := h.repo.PeekAssistantGeneration(user.ID, sessionID)
	if !current.CancelRequested || first.Steer == nil || current.Steer == nil ||
		current.Steer.Content != "first tab" || current.Steer.TriggerID != first.Steer.TriggerID {
		t.Fatalf("the first accepted steer was changed: %+v", current.Steer)
	}
}

func TestSteerDuringTurnFinalization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		steerFirst bool
	}{
		{name: "steer accepted before finalization", steerFirst: true},
		{name: "steer arrives while saving partial reply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user := workbenchFixture(t)
			stubCLIModelDiscovery(t)
			const followUp = "continue with this instruction"
			s := domain.AssistantSession{ID: "steer-at-finish", ProjectID: "workbench", Model: "default", Messages: []assistantMessage{
				{Role: "user", Content: "original"},
			}}
			g := domain.AssistantGeneration{ID: "finishing-turn", SessionID: s.ID, UserID: user.ID, ProjectID: s.ProjectID, Status: "running", MessageCount: 1}
			if err := h.repo.BeginAssistantGeneration(g, s); err != nil {
				t.Fatal(err)
			}
			submitSteer := func() *httptest.ResponseRecorder {
				body, _ := json.Marshal(domain.AssistantSteer{Content: followUp})
				path := "/api/assistant/sessions/" + s.ID + "/generation/steer?generationId=" + g.ID
				c, rec := workbenchContext(user, "POST", path, bytes.NewBuffer(body))
				c.Params = gin.Params{{Key: "id", Value: s.ID}}
				h.steerGeneration(c)
				return rec
			}
			var response *httptest.ResponseRecorder
			var savePartial atomic.Bool
			h.generate = func(ctx context.Context, prompt string, _ domain.User) (string, error) {
				if strings.Contains(prompt, followUp) {
					return `{"message":"follow-up reply","drafts":[]}`, nil
				}
				sink := ctx.Value(assistantGenerationSinkKey{}).(func(string, any))
				sink("thought", assistantStreamEvent{Kind: "answer", Text: "partial reply"})
				if tc.steerFirst {
					response = submitSteer()
				} else {
					savePartial.Store(true)
				}
				return "", errors.New("test model stopped")
			}
			// This read starts the partial transcript write after the worker has
			// stopped checking for steering requests. The old code accepted a
			// request here, then closed the turn without following it.
			const callback = "test:steer_during_transcript_save"
			queries := h.repo.DB.Callback().Query()
			if err := queries.After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "assistant_sessions" && savePartial.CompareAndSwap(true, false) {
					response = submitSteer()
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer queries.Remove(callback)
			h.executeGeneration(t.Context(), assistantRequest{SessionID: s.ID, ProjectID: s.ProjectID, Model: s.Model, Messages: s.Messages}, g, domainUserToAuth(user))

			if response == nil {
				t.Fatal("steering request did not run")
			}
			saved, _ := h.repo.AssistantSession(user.ID, s.ID)
			if len(saved.Messages) < 2 || replyMessageText(saved.Messages[1].Content) != "partial reply" {
				t.Fatalf("the first turn must keep the partial reply: %+v", saved.Messages)
			}
			current, _ := h.repo.PeekAssistantGeneration(user.ID, s.ID)
			if tc.steerFirst {
				if response.Code != 200 || current.ID == g.ID || current.Status != "completed" || len(saved.Messages) != 4 ||
					saved.Messages[2].Content != followUp || saved.Messages[2].TriggerID == "" || replyMessageText(saved.Messages[3].Content) != "follow-up reply" {
					t.Fatalf("accepted steer must start its own turn: %d %s, %s, %+v", response.Code, response.Body, current.Status, saved.Messages)
				}
			} else if response.Code != 409 || !strings.Contains(response.Body.String(), "生成已结束或不存在") ||
				current.ID != g.ID || current.Status != "failed" || current.CancelRequested || current.Steer != nil || len(saved.Messages) != 2 {
				t.Fatalf("a turn that is closing must reject the steer as ended: %d %s, %+v, %+v", response.Code, response.Body, current, saved.Messages)
			}
		})
	}
}

func TestSteerKeepsNewAttachmentsAndReferencesWithSameText(t *testing.T) {
	h, user := workbenchFixture(t)
	stubCLIModelDiscovery(t)
	const projectID, sessionID = "workbench", "same-text-steer"
	const question = "请查看这份文件"
	upload := func(name string) domain.AssistantAttachment {
		t.Helper()
		var form bytes.Buffer
		w := multipart.NewWriter(&form)
		if err := w.WriteField("projectId", projectID); err != nil {
			t.Fatal(err)
		}
		file, err := w.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(name)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		c, rec := workbenchContext(user, "POST", "/api/assistant/attachments", &form)
		c.Request.Header.Set("Content-Type", w.FormDataContentType())
		h.uploadAttachment(c)
		var item domain.AssistantAttachment
		if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &item) != nil {
			t.Fatalf("upload %s: %d %s", name, rec.Code, rec.Body)
		}
		return item
	}
	first, second := upload("first.txt"), upload("second.txt")
	for _, id := range []string{"first-issue", "second-issue"} {
		if err := h.repo.PutIssue(domain.Issue{ID: id, Title: id, ProjectID: projectID}); err != nil {
			t.Fatal(err)
		}
	}

	started := make(chan struct{})
	followUpPrompt := make(chan string, 1)
	h.generate = func(ctx context.Context, prompt string, _ domain.User) (string, error) {
		select {
		case <-started:
			followUpPrompt <- prompt
			return "已查看附件", nil
		default:
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
	}
	request, _ := json.Marshal(assistantRequest{
		SessionID: sessionID, ProjectID: projectID,
		Messages: []assistantMessage{{
			Role: "user", Content: question,
			Attachments: []domain.AssistantAttachment{first},
			References:  []domain.AssistantReference{{Type: "issue", ID: "first-issue"}},
		}},
	})
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(request))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("initial turn did not start")
	}
	generation, _ := h.repo.PeekAssistantGeneration(user.ID, sessionID)
	body, _ := json.Marshal(domain.AssistantSteer{
		Content: question, Attachments: []domain.AssistantAttachment{second},
		References: []domain.AssistantReference{{Type: "issue", ID: "second-issue"}},
	})
	path := "/api/assistant/sessions/" + sessionID + "/generation/steer?generationId=" + generation.ID
	c, rec = workbenchContext(user, "POST", path, bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: sessionID}}
	h.steerGeneration(c)
	if rec.Code != 200 {
		t.Fatalf("steer: %d %s", rec.Code, rec.Body)
	}

	for deadline := time.Now().Add(5 * time.Second); ; {
		next, _ := h.repo.PeekAssistantGeneration(user.ID, sessionID)
		if next.ID != generation.ID && next.Status == "completed" {
			break
		}
		if (next.ID != generation.ID && next.Status == "failed") || time.Now().After(deadline) {
			t.Fatalf("follow-up did not complete: %s %s", next.Status, next.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	saved, _ := h.repo.AssistantSession(user.ID, sessionID)
	if len(saved.Messages) != 3 {
		t.Fatalf("expected two user messages and a reply, got %+v", saved.Messages)
	}
	original, steered := saved.Messages[0], saved.Messages[1]
	if original.Content != question || len(original.Attachments) != 1 || original.Attachments[0].ID != first.ID ||
		len(original.References) != 1 || original.References[0].ID != "first-issue" {
		t.Fatalf("original message changed: %+v", original)
	}
	if steered.Role != "user" || steered.Content != question || steered.Trigger != "steer" || steered.TriggerID == "" ||
		len(steered.Attachments) != 1 || steered.Attachments[0].ID != second.ID ||
		len(steered.References) != 1 || steered.References[0].ID != "second-issue" {
		t.Fatalf("steered message lost its context: %+v", steered)
	}
	prompt := <-followUpPrompt
	if !strings.Contains(prompt, second.Name) || !strings.Contains(prompt, "second-issue") {
		t.Fatal("the follow-up prompt must contain the new file and reference")
	}
}

func TestCommitTurnTranscriptKeepsSameSteerOnce(t *testing.T) {
	for _, tc := range []struct{ name, id, want string }{
		{"accepted steer", "accepted-steer-id", "accepted-steer-id"},
		{"legacy steer", "", "interrupted-turn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user := workbenchFixture(t)
			session := domain.AssistantSession{ID: "steer-replay", Model: "default", Messages: []assistantMessage{
				{Role: "user", Content: "original"},
			}}
			if err := h.repo.PutAssistantSession(user.ID, session); err != nil {
				t.Fatal(err)
			}
			generation := domain.AssistantGeneration{ID: "interrupted-turn", SessionID: session.ID, UserID: user.ID,
				Stream: &domain.AssistantStreamSnapshot{Answer: "partial reply"}}
			steer := &domain.AssistantSteer{Content: "continue", TriggerID: tc.id}
			for range 2 {
				if err := h.commitTurnTranscript(generation, steer, false); err != nil {
					t.Fatal(err)
				}
			}
			stored, _ := h.repo.AssistantSession(user.ID, session.ID)
			if len(stored.Messages) != 3 ||
				stored.Messages[1].GenerationID != generation.ID ||
				stored.Messages[2].TriggerID != tc.want || stored.Messages[2].Content != steer.Content {
				t.Fatalf("replaying a steer must keep one partial reply and one follow-up: %+v", stored.Messages)
			}
		})
	}
}

func TestSteerKeepsConversationCLI(t *testing.T) {
	for _, tc := range []struct {
		name, executor, savedModel, requested, wantModel string
		runnerLogin, missingKey                          bool
		wantStatus                                       int
	}{
		{"codex login prefixed", "codex", "codex:gpt-old", "codex:gpt-selected", "codex:gpt-selected", true, false, 200},
		{"codex login unprefixed", "codex", "codex:gpt-old", "gpt-selected", "codex:gpt-selected", true, false, 200},
		{"codex login omitted", "codex", "codex:gpt-old", "", "codex:gpt-old", true, false, 200},
		{"codex legacy", "codex", "gpt-old", "gpt-selected", "codex:gpt-selected", true, false, 200},
		{"codex key", "codex", "codex:gpt-old", "gpt-selected", "codex:gpt-selected", false, false, 200},
		{"cursor prefixed", "cursor", "cursor:old", "cursor:selected", "cursor:selected", false, false, 200},
		{"cursor unprefixed", "cursor", "cursor:old", "selected", "cursor:selected", false, false, 200},
		{"cursor legacy", "cursor", "old", "selected", "cursor:selected", false, false, 200},
		{"codebuddy", "codebuddy", "default", "default", "default", false, false, 200},
		{"codex missing key", "codex", "codex:gpt-old", "gpt-selected", "", false, true, 400},
		{"cursor missing key", "cursor", "cursor:old", "selected", "", false, true, 400},
		{"codex to cursor", "codex", "codex:gpt-old", "cursor:selected", "", true, false, 409},
		{"cursor to codex", "cursor", "cursor:old", "codex:gpt-selected", "", false, false, 409},
		{"codex to codebuddy", "codex", "codex:gpt-old", "codebuddy:default", "", true, false, 409},
		{"codebuddy to codex", "codebuddy", "default", "codex:gpt-selected", "", false, false, 409},
		{"invalid codex model", "codex", "codex:gpt-old", "codex:bad model", "", true, false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user := workbenchFixture(t)
			stubCLIModelDiscovery(t)
			credentials := repo.UserCredentialUpdate{CodeBuddyAPIKeyClear: tc.executor != "codebuddy"}
			if !tc.missingKey && !tc.runnerLogin {
				credentials.CodexAPIKey = "test-codex"
				credentials.AgentCLICredentials = map[string]repo.AgentCLICredentialUpdate{"cursor": {APIKey: "test-cursor"}}
			}
			var err error
			user, err = h.repo.UpdateUserCredentials(user.ID, credentials)
			if err != nil {
				t.Fatal(err)
			}
			target := domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}
			if tc.runnerLogin {
				runner := domain.Runner{ID: "login-runner", OwnerUserID: user.ID, Runtimes: []domain.RunnerRuntime{{Provider: "codex", AuthMode: domain.RuntimeAuthLocal}}}
				if err := h.repo.PutRunner(runner); err != nil {
					t.Fatal(err)
				}
				target = domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: runner.ID}
			}
			session := domain.AssistantSession{ID: "steer-cli", Executor: tc.executor, Model: tc.savedModel, ExecutionTarget: &target, Messages: []assistantMessage{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "first reply", Model: tc.savedModel},
				{Role: "user", Content: "continue"},
			}}
			g := domain.AssistantGeneration{ID: "running-turn", SessionID: session.ID, UserID: user.ID, Status: "running", MessageCount: len(session.Messages)}
			if err := h.repo.BeginAssistantGeneration(g, session); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(domain.AssistantSteer{Content: "redirect", Model: tc.requested})
			c, rec := workbenchContext(user, "POST", "/api/assistant/sessions/steer-cli/generation/steer?generationId="+g.ID, bytes.NewBuffer(body))
			c.Params = gin.Params{{Key: "id", Value: session.ID}}
			h.steerGeneration(c)
			if rec.Code != tc.wantStatus {
				t.Fatalf("steer: %d %s, want %d", rec.Code, rec.Body, tc.wantStatus)
			}
			current, _ := h.repo.AssistantGeneration(user.ID, session.ID)
			if tc.wantStatus != 200 {
				if current.CancelRequested || current.Steer != nil || current.Status != "running" {
					t.Fatalf("rejected request interrupted the turn: %+v", current)
				}
				if tc.wantStatus == 409 && !strings.Contains(rec.Body.String(), "不能中途切换 CLI") {
					t.Fatalf("missing switch refusal: %s", rec.Body)
				}
				return
			}
			if !current.CancelRequested || current.Steer == nil || current.Steer.Model != tc.wantModel {
				t.Fatalf("steer: %+v, want model %q", current.Steer, tc.wantModel)
			}
		})
	}
}
