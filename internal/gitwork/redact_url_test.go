package gitwork

import (
	"strings"
	"testing"
)

func TestRedactMasksURLUserinfo(t *testing.T) {
	t.Setenv("AGENTFLOW_GIT_TOKEN", "")
	out := Redact("fatal: unable to access 'https://user:s3cret-token@github.com/acme/app.git/': 403 and https://ghp_abc123@github.com/x.git", "unrelated-git-token")
	if strings.Contains(out, "s3cret-token") || strings.Contains(out, "ghp_abc123") || strings.Contains(out, "user:") {
		t.Fatalf("URL credentials survived redaction: %s", out)
	}
	if !strings.Contains(out, "https://***@github.com/acme/app.git") {
		t.Fatalf("redacted URL lost its host: %s", out)
	}
	ssh := Redact("ssh://git@git.example.com/r.git ssh://git:pw@git.example.com/r.git")
	if ssh != "ssh://git@git.example.com/r.git ssh://git:***@git.example.com/r.git" {
		t.Fatalf("ssh redaction: %s", ssh)
	}
	if got := Redact("see https://github.com/acme/app and mail a@b.example"); got != "see https://github.com/acme/app and mail a@b.example" {
		t.Fatalf("plain text changed: %s", got)
	}
}
