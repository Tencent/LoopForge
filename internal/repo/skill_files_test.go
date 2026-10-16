package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func putTestSkill(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.PutSkill(domain.SkillDefinition{ID: id, Name: "Skill " + id, ProjectID: "platform", Content: "# " + id}); err != nil {
		t.Fatal(err)
	}
}

func TestSkillFilesRoundTripAndManifest(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putTestSkill(t, s, "bin-skill")

	if err := s.PutSkillFiles("bin-skill", []SkillFileInput{
		{Path: "assets/logo.png", Content: []byte{0x89, 0x50, 0x4e, 0x47}, Mode: 0o644},
		{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0o755},
	}); err != nil {
		t.Fatal(err)
	}

	refs := s.SkillFileRefs("bin-skill")
	if len(refs) != 2 || refs[0].Path != "assets/logo.png" || refs[1].Path != "scripts/run.sh" {
		t.Fatalf("unexpected refs: %#v", refs)
	}
	if refs[1].Mode != 0o755 || refs[0].Size != 4 {
		t.Fatalf("unexpected ref metadata: %#v", refs)
	}

	// The payload manifest must mirror the table rows.
	skill, ok := s.Skill("bin-skill")
	if !ok || len(skill.FileRefs) != 2 {
		t.Fatalf("payload manifest not refreshed: %#v", skill)
	}

	contents := s.SkillFileContents("bin-skill")
	if len(contents) != 2 || string(contents["scripts/run.sh"]) != "#!/bin/sh\n" {
		t.Fatalf("unexpected contents: %#v", contents)
	}
	if content, ok := s.SkillFileContent("bin-skill", "assets/logo.png"); !ok || len(content) != 4 {
		t.Fatalf("unexpected single-file load: %d %v", len(content), ok)
	}
	if got := s.SkillFileTotalBytes("bin-skill"); got != int64(4+len("#!/bin/sh\n")) {
		t.Fatalf("unexpected total bytes: %d", got)
	}
}

func TestSkillFileDeleteAndUpsert(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putTestSkill(t, s, "mut-skill")

	if err := s.PutSkillFiles("mut-skill", []SkillFileInput{{Path: "a.txt", Content: []byte("one"), Mode: 0o644}}); err != nil {
		t.Fatal(err)
	}
	// Upsert replaces content and refreshes the manifest.
	if err := s.PutSkillFiles("mut-skill", []SkillFileInput{{Path: "a.txt", Content: []byte("two"), Mode: 0o644}}); err != nil {
		t.Fatal(err)
	}
	if content, _ := s.SkillFileContent("mut-skill", "a.txt"); string(content) != "two" {
		t.Fatalf("upsert did not replace content: %q", content)
	}
	if refs := s.SkillFileRefs("mut-skill"); len(refs) != 1 {
		t.Fatalf("unexpected refs after upsert: %#v", refs)
	}

	if err := s.DeleteSkillFile("mut-skill", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if refs := s.SkillFileRefs("mut-skill"); len(refs) != 0 {
		t.Fatalf("unexpected refs after delete: %#v", refs)
	}
	if skill, _ := s.Skill("mut-skill"); len(skill.FileRefs) != 0 {
		t.Fatalf("manifest not refreshed after delete: %#v", skill.FileRefs)
	}
	if err := s.DeleteSkillFiles("mut-skill"); err != nil {
		t.Fatal(err)
	}
}

func TestSkillFilesRejectInvalidPathsAndOversize(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putTestSkill(t, s, "guard-skill")

	for _, input := range []SkillFileInput{
		{Path: "../escape", Content: []byte("x")},
		{Path: "/abs", Content: []byte("x")},
		{Path: "ok.txt", Content: make([]byte, domain.SkillFileMaxBytes+1)},
	} {
		if err := s.PutSkillFiles("guard-skill", []SkillFileInput{input}); err == nil {
			t.Fatalf("expected rejection for %s", input.Path)
		}
	}
	// Nothing may be written for a failed batch.
	if refs := s.SkillFileRefs("guard-skill"); len(refs) != 0 {
		t.Fatalf("partial write leaked: %#v", refs)
	}
	// Unknown skill is rejected.
	if err := s.PutSkillFiles("missing", []SkillFileInput{{Path: "a", Content: []byte("x")}}); err == nil {
		t.Fatal("expected missing skill rejection")
	}
}

func TestSkillFileModeNormalization(t *testing.T) {
	if got := domain.NormalizeSkillFileMode(0); got != 0o644 {
		t.Fatalf("zero mode should default to 0644, got %o", got)
	}
	if got := domain.NormalizeSkillFileMode(0o1777); got != 0o777 { // setuid bits outside 0777 are masked
		t.Fatalf("unexpected mask result: %o", got)
	}
	if got := domain.NormalizeSkillFileMode(0o755); got != 0o755 {
		t.Fatalf("unexpected mode: %o", got)
	}
}
