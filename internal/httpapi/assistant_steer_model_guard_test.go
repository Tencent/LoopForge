package httpapi

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// steerGuardCatalog pins the chat catalog to a default that accepts "max"
// effort and a second listed model that only accepts "high".
func steerGuardCatalog(t *testing.T) {
	t.Helper()
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "glm-5.3-ioa")
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "hy4std")
}

// A shared-key steer without an explicit model runs on the first allowed
// model, not on the catalog default.
func TestResolveSteerModelSharedKeyUsesAllowedModel(t *testing.T) {
	steerGuardCatalog(t)
	h, user := workbenchFixture(t)
	ctx := context.Background()
	if def := h.chatCatalog(ctx, user).Default; def != "glm-5.3-ioa" {
		t.Fatalf("catalog default = %q, want glm-5.3-ioa", def)
	}
	for _, requested := range []string{"", "hy4std"} {
		model, err := h.resolveSteerModel(ctx, user, []string{"hy4std"}, requested, "")
		if err != nil {
			t.Fatalf("requested %q: %v", requested, err)
		}
		if model != "hy4std" {
			t.Fatalf("requested %q: model = %q, want allowed model hy4std", requested, model)
		}
	}
}

// The effort is checked against the resolved model: an empty or unlisted
// request falls back to the default, so the default's efforts apply.
func TestResolveSteerModelChecksEffortAgainstResolvedModel(t *testing.T) {
	steerGuardCatalog(t)
	h, user := workbenchFixture(t)
	ctx := context.Background()
	for _, requested := range []string{"", "not-listed-model"} {
		model, err := h.resolveSteerModel(ctx, user, nil, requested, "max")
		if err != nil {
			t.Fatalf("requested %q: %v", requested, err)
		}
		if model != "glm-5.3-ioa" {
			t.Fatalf("requested %q: model = %q, want default glm-5.3-ioa", requested, model)
		}
	}
	model, err := h.resolveSteerModel(ctx, user, nil, "hy4std", "max")
	if err == nil || !strings.Contains(err.Error(), "hy4std") {
		t.Fatalf("hy4std with max effort: got (%q, %v), want unsupported effort", model, err)
	}
}

// steerGeneration answers 400 when the steer model cannot be resolved and
// leaves the running turn untouched: no steer is queued and it keeps running.
func TestSteerGenerationRejectsUnresolvableModelWithoutQueueing(t *testing.T) {
	h, user := workbenchFixture(t)
	entered := make(chan struct{}, 1)
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"steer-reject-chat","messages":[{"role":"user","content":"先查项目"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	<-entered
	g, _ := h.repo.AssistantGeneration(user.ID, "steer-reject-chat")
	t.Cleanup(func() {
		_ = h.repo.CancelAssistantGeneration(user.ID, "steer-reject-chat", g.ID)
		waitGeneration(t, h, user.ID, "steer-reject-chat", func(gen domain.AssistantGeneration) bool { return gen.Status != "running" })
	})
	c, rec = workbenchContext(user, "POST", "/api/assistant/sessions/steer-reject-chat/generation/steer?generationId="+g.ID, bytes.NewBufferString(`{"content":"改查仓库","effort":"bogus"}`))
	c.Params = gin.Params{{Key: "id", Value: "steer-reject-chat"}}
	h.steerGeneration(c)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "不支持思考强度") {
		t.Fatalf("steer with bogus effort: %d %s", rec.Code, rec.Body.String())
	}
	current, ok := h.repo.AssistantGeneration(user.ID, "steer-reject-chat")
	if !ok || current.ID != g.ID || current.Status != "running" || current.Steer != nil {
		t.Fatalf("rejected steer changed the running turn: ok=%v %+v", ok, current)
	}
}
