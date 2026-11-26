package repo

import (
	"agentflow/internal/domain"
	"testing"
)

// The settings are one row read on use, so a change applies without a
// restart. An unsaved store reads as the zero value rather than an error,
// because callers must stay able to fall back to their environment.
func TestPlatformSettingsRoundTrip(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if got := s.PlatformSettings(); got != (domain.PlatformSettings{}) {
		t.Fatalf("unsaved settings = %+v", got)
	}
	if err := s.PutPlatformSettings(domain.PlatformSettings{PublicURL: "  https://loopforge.example.com  ", UpdatedBy: " platform-admin "}); err != nil {
		t.Fatal(err)
	}
	saved := s.PlatformSettings()
	if saved.PublicURL != "https://loopforge.example.com" || saved.UpdatedBy != "platform-admin" {
		t.Fatalf("saved = %+v", saved)
	}
	if saved.UpdatedAt.IsZero() {
		t.Fatal("saved settings carry no timestamp")
	}

	// A second write replaces the row instead of adding one.
	if err := s.PutPlatformSettings(domain.PlatformSettings{PublicURL: "https://other.example.com", UpdatedBy: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := s.DB.Model(&platformSettingsRow{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d", rows)
	}
	if again := s.PlatformSettings(); again.PublicURL != "https://other.example.com" || again.UpdatedBy != "someone-else" {
		t.Fatalf("replaced = %+v", again)
	}

	// Clearing the address keeps the row but makes callers fall back.
	if err := s.PutPlatformSettings(domain.PlatformSettings{}); err != nil {
		t.Fatal(err)
	}
	if cleared := s.PlatformSettings(); cleared.PublicURL != "" || cleared.UpdatedBy != "" {
		t.Fatalf("cleared = %+v", cleared)
	}
}
