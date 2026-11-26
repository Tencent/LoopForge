package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"agentflow/internal/repo"
)

func TestSharedCodeBuddyModelsTrimsAndDeduplicatesInOrder(t *testing.T) {
	got, err := sharedCodeBuddyModels([]string{" glm-5.1 ", "", "kimi-k2.6", "glm-5.1", "  ", "org/model:v1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(got, ",") != "glm-5.1,kimi-k2.6,org/model:v1" {
		t.Fatalf("models = %q", got)
	}
}

func TestSharedCodeBuddyModelsRejectsInvalidID(t *testing.T) {
	for _, id := range []string{"-leading-dash", "bad id", "中文模型", strings.Repeat("a", 129)} {
		got, err := sharedCodeBuddyModels([]string{"glm-5.1", id})
		want := "模型 ID 格式不正确：" + strings.TrimSpace(id)
		if err == nil || err.Error() != want || got != nil {
			t.Fatalf("id %q: models=%q err=%v, want %q", id, got, err, want)
		}
	}
}

func TestSharedCodeBuddyModelsRequiresAtLeastOne(t *testing.T) {
	for _, raw := range [][]string{nil, {}, {"", "   "}} {
		if _, err := sharedCodeBuddyModels(raw); err == nil || err.Error() != "请至少选择一个允许使用的模型" {
			t.Fatalf("raw %q: err = %v", raw, err)
		}
	}
}

func TestSharedCodeBuddyModelsEnforcesUpperBound(t *testing.T) {
	raw := make([]string, 0, repo.MaxSharedCodeBuddyModels+1)
	for i := 0; i < repo.MaxSharedCodeBuddyModels; i++ {
		raw = append(raw, fmt.Sprintf("model-%d", i))
	}
	got, err := sharedCodeBuddyModels(append(raw, raw[0]))
	if err != nil || len(got) != repo.MaxSharedCodeBuddyModels {
		t.Fatalf("at limit (with duplicate): len=%d err=%v", len(got), err)
	}
	raw = append(raw, "one-too-many")
	want := fmt.Sprintf("最多选择 %d 个模型", repo.MaxSharedCodeBuddyModels)
	if _, err := sharedCodeBuddyModels(raw); err == nil || err.Error() != want {
		t.Fatalf("over limit: err = %v, want %q", err, want)
	}
}
