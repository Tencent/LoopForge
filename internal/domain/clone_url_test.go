package domain

import "testing"

func TestStripURLCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:s3cret-token@github.com/acme/app.git": "https://github.com/acme/app.git",
		"https://ghp_tokenAsUser@github.com/acme/app.git":   "https://github.com/acme/app.git",
		"http://oauth2:abc@git.example.com/g/r.git":         "http://git.example.com/g/r.git",
		"git://u:p@git.example.com/r.git":                   "git://git.example.com/r.git",
		"ssh://git:pw@git.example.com/g/r.git":              "ssh://git@git.example.com/g/r.git",
		"ssh://git@git.example.com:2222/g/r.git":            "ssh://git@git.example.com:2222/g/r.git",
		"git@github.com:acme/app.git":                       "git@github.com:acme/app.git",
		"https://github.com/acme/app.git":                   "https://github.com/acme/app.git",
		" https://github.com/acme/a@b.git ":                 "https://github.com/acme/a@b.git",
		"/srv/repos/app":                                    "/srv/repos/app",
		"":                                                  "",
	} {
		if got := StripURLCredentials(in); got != want {
			t.Errorf("StripURLCredentials(%q) = %q, want %q", in, got, want)
		}
	}
	if !URLHasCredentials("https://x:y@h/r.git") || URLHasCredentials("ssh://git@h/r.git") || URLHasCredentials("git@h:r.git") {
		t.Fatal("URLHasCredentials classification")
	}
	public := PublicRepository(Repository{CloneURL: "https://user:s3cret-token@github.com/acme/app.git", GitToken: "t"})
	if public.CloneURL != "https://github.com/acme/app.git" || public.GitToken != "" {
		t.Fatalf("public repository leaks credentials: %+v", public)
	}
}
