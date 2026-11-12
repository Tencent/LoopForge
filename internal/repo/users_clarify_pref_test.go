package repo

import (
	"testing"

	"agentflow/internal/domain"
)

// Task 5: 个人默认框架。偏好存 user_configs.clarify_model，走的是凭据那条更新通道，
// 所以这些测试要证明：只有偏好没有 Key 的新用户也能落库，更新别的字段不会抹掉它，
// 指针缺席保持原值、空串清除。

func TestUserConfigPersistsClarifyModel(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutUser(domain.User{LoginName: "pref-only"}); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.UserByLoginName("pref-only")
	if !ok {
		t.Fatal("test user missing")
	}

	value := "codex:gpt-5.5"
	if _, err := s.UpdateUserCredentials(stored.ID, UserCredentialUpdate{ClarifyModel: &value}); err != nil {
		t.Fatal(err)
	}
	if loaded, _ := s.User(stored.ID); loaded.ClarifyModel != value {
		t.Fatalf("只有偏好、没有任何 Key 的用户没有落库：%+v", loaded)
	}

	if _, err := s.UpdateUserCredentials(stored.ID, UserCredentialUpdate{CodexAPIKey: "sk-x"}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := s.User(stored.ID)
	if loaded.ClarifyModel != value {
		t.Fatalf("更新凭据抹掉了个人默认：%q", loaded.ClarifyModel)
	}
	if loaded.CodexAPIKey != "sk-x" {
		t.Fatalf("凭据没有一起写入：%+v", loaded)
	}

	if _, err := s.UpdateUserCredentials(stored.ID, UserCredentialUpdate{}); err != nil {
		t.Fatal(err)
	}
	if loaded, _ = s.User(stored.ID); loaded.ClarifyModel != value {
		t.Fatalf("指针缺席应当保持原值，却是 %q", loaded.ClarifyModel)
	}

	empty := ""
	if _, err := s.UpdateUserCredentials(stored.ID, UserCredentialUpdate{ClarifyModel: &empty}); err != nil {
		t.Fatal(err)
	}
	if loaded, _ = s.User(stored.ID); loaded.ClarifyModel != "" {
		t.Fatalf("空串应当清除个人默认，却是 %q", loaded.ClarifyModel)
	}
}
