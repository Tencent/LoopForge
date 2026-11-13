package repo

import (
	"reflect"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestDefaultSandboxWithoutSeedDoesNotWriteRows(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.DB.Exec("DELETE FROM sandboxes").Error; err != nil {
		t.Fatal(err)
	}
	profile, ok := s.Sandbox(DefaultSandboxProfileID)
	if !ok || profile.ID != "local" || profile.Driver != domain.SandboxDriverLocal || !profile.Shared || profile.ProjectID != "" {
		t.Fatalf("missing shared default: %+v, found=%v", profile, ok)
	}
	profiles := s.Sandboxes()
	if len(profiles) != 1 || !reflect.DeepEqual(profiles[0], profile) {
		t.Fatalf("list and lookup disagree: %+v / %+v", profiles, profile)
	}
	var count int64
	if err := s.DB.Table("sandboxes").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("default lookup wrote catalog data: rows=%d, err=%v", count, err)
	}
	if _, ok := s.Sandbox("not-configured"); ok {
		t.Fatal("an unknown sandbox must not use the default")
	}
}

func TestStoredDefaultSandboxWinsWithoutDuplicatesAndCannotBeDeleted(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	configured := domain.SandboxProfile{ID: "local", Name: "Operator configured", ProjectID: "platform", Shared: true, Driver: "native", Environment: map[string]string{"PRESERVE": "yes"}}
	if err := s.PutSandbox(configured); err != nil {
		t.Fatal(err)
	}
	profile, ok := s.Sandbox(configured.ID)
	if !ok || !reflect.DeepEqual(profile, configured) {
		t.Fatalf("operator configuration changed: %+v", profile)
	}
	count := 0
	for _, candidate := range s.Sandboxes() {
		if candidate.ID == configured.ID {
			count++
			if !reflect.DeepEqual(candidate, configured) {
				t.Fatalf("list replaced operator configuration: %+v", candidate)
			}
		}
	}
	if count != 1 {
		t.Fatalf("default appears %d times", count)
	}
	if err := s.DeleteSandbox(configured.ID); err == nil || !strings.Contains(err.Error(), "不能删除") {
		t.Fatalf("default deletion must be explicit: %v", err)
	}
	profile, _ = s.Sandbox(configured.ID)
	if !reflect.DeepEqual(profile, configured) {
		t.Fatal("rejected deletion changed the configured execution policy")
	}
}

func TestDefaultSandboxDoesNotMaskCorruptConfigurationOrDatabaseFailure(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.DB.Table("sandboxes").Where("id = ?", DefaultSandboxProfileID).Update("payload", `"invalid profile"`).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Sandbox(DefaultSandboxProfileID); ok {
		t.Fatal("invalid stored configuration must not fall back to local execution")
	}
	if err := s.DB.Exec("DELETE FROM sandboxes").Error; err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, ok := s.Sandbox(DefaultSandboxProfileID); ok {
		t.Fatal("database failure must not fall back to local execution")
	}
	if profiles := s.Sandboxes(); len(profiles) != 0 {
		t.Fatalf("database failure reported available sandboxes: %+v", profiles)
	}
}

// The public catalog seed stored generic-docker without the shared flag; it is
// still the platform fallback for conversation workspaces in every project.
func TestSeededGenericDockerSandboxIsSharedForConversations(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	seeded := `{"id":"generic-docker","name":"Docker 隔离沙箱","driver":"docker","dockerfileId":"generic-backend","readOnlyRootFS":false}`
	if err := s.DB.Table("sandboxes").Where("id = ?", GenericDockerSandboxID).Update("payload", seeded).Error; err != nil {
		t.Fatal(err)
	}
	profile, ok := s.Sandbox(GenericDockerSandboxID)
	if !ok || !profile.Shared {
		t.Fatalf("seeded generic-docker is not shared: %+v", profile)
	}
	if got, err := s.ResolveConversationSandbox("any-project", ""); err != nil || got.ID != GenericDockerSandboxID {
		t.Fatalf("default conversation sandbox = %+v, %v", got, err)
	}
	found := false
	for _, candidate := range s.ConversationSandboxes("any-project") {
		found = found || candidate.ID == GenericDockerSandboxID
	}
	if !found {
		t.Fatal("generic-docker missing from the project's conversation sandboxes")
	}
}
