package gitwork

import "testing"

func TestParseSSHRemoteNormalizesAcceptedAddresses(t *testing.T) {
	cases := []struct {
		raw  string
		want SSHRemote
	}{
		{"git@GitLab.com:group/repo.git", SSHRemote{URL: "git@gitlab.com:group/repo.git", User: "git", Host: "gitlab.com", Path: "group/repo.git", Port: "22"}},
		{` git clone "git@gitlab.com:group/repo" `, SSHRemote{URL: "git@gitlab.com:group/repo", User: "git", Host: "gitlab.com", Path: "group/repo", Port: "22"}},
		{"ssh://git@git.example.org:2222/group/repo.git", SSHRemote{URL: "ssh://git@git.example.org:2222/group/repo.git", User: "git", Host: "git.example.org", Path: "/group/repo.git", Port: "2222"}},
		{"ssh://git@git.example.org:02222/repo", SSHRemote{URL: "ssh://git@git.example.org:2222/repo", User: "git", Host: "git.example.org", Path: "/repo", Port: "2222"}},
		{"ssh://git@[::1]/repo.git", SSHRemote{URL: "ssh://git@[::1]:22/repo.git", User: "git", Host: "::1", Path: "/repo.git", Port: "22"}},
		{"git@[0:0::1]:repo.git", SSHRemote{URL: "git@[::1]:repo.git", User: "git", Host: "::1", Path: "repo.git", Port: "22"}},
	}
	for _, tc := range cases {
		got, err := ParseSSHRemote(tc.raw)
		if err != nil {
			t.Fatalf("ParseSSHRemote(%q) error = %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ParseSSHRemote(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

func TestParseSSHRemoteRejectsInvalidAddresses(t *testing.T) {
	for _, raw := range []string{
		"",
		"https://gitlab.com/group/repo.git",
		"gitlab.com:group/repo.git",
		"git@gitlab.com:group/repo.git?x=1",
		"git@gitlab.com:group/re po.git",
		"ssh://gitlab.com/group/repo.git",
		"ssh://git:secret@gitlab.com/group/repo.git",
		"ssh://git@gitlab.com:/group/repo.git",
		"ssh://git@gitlab.com:0/group/repo.git",
		"ssh://git@gitlab.com:65536/group/repo.git",
		"git@-gitlab.com:group/repo.git",
		"git@gitlab..com:group/repo.git",
		"-git@gitlab.com:group/repo.git",
		"git@gitlab.com:group/../repo.git",
		"git@gitlab.com:group//repo.git",
		"git@gitlab.com:group/.git",
		"git@gitlab.com:group/repo$.git",
	} {
		if got, err := ParseSSHRemote(raw); err == nil {
			t.Fatalf("ParseSSHRemote(%q) = %+v, want error", raw, got)
		}
	}
}

func TestSplitSSHRemote(t *testing.T) {
	user, host, path, port, ok := splitSSHRemote("ssh://git@Host:2200/a/b.git")
	if !ok || user != "git" || host != "Host" || path != "/a/b.git" || port != "2200" {
		t.Fatalf("ssh url split = %q %q %q %q %v", user, host, path, port, ok)
	}
	user, host, path, port, ok = splitSSHRemote("git@[::1]:a/b")
	if !ok || user != "git" || host != "::1" || path != "a/b" || port != "22" {
		t.Fatalf("scp split = %q %q %q %q %v", user, host, path, port, ok)
	}
	for _, s := range []string{"ssh://host/a", "ssh://u:p@host/a", "ssh://u@host:99999/a", "host:a/b"} {
		if _, _, _, _, ok := splitSSHRemote(s); ok {
			t.Fatalf("splitSSHRemote(%q) ok, want rejection", s)
		}
	}
}

func TestValidSSHRepoPath(t *testing.T) {
	for path, want := range map[string]bool{
		"group/repo.git":  true,
		"/group/repo":     true,
		"~user/repo.git":  true,
		"":                false,
		"/":               false,
		"group/":          false,
		"group/.git":      false,
		"group/./repo":    false,
		"group/../repo":   false,
		"group/re:po.git": false,
	} {
		if got := validSSHRepoPath(path); got != want {
			t.Fatalf("validSSHRepoPath(%q) = %v, want %v", path, got, want)
		}
	}
}
