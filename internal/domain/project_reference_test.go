package domain

import "testing"

func TestStripGitCloneCommand(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{name: "plain url unchanged", input: "https://git.example.com/infra/gateway", want: "https://git.example.com/infra/gateway"},
		{name: "clone prefix", input: "git clone https://git.example.com/infra/gateway.git", want: "https://git.example.com/infra/gateway.git"},
		{name: "case insensitive prefix", input: "GIT CLONE git@git.example.com:infra/gateway.git", want: "git@git.example.com:infra/gateway.git"},
		{name: "drops target dir", input: "git clone git@git.example.com:infra/gateway.git local-dir", want: "git@git.example.com:infra/gateway.git"},
		{name: "drops tab separated dir", input: "git clone https://git.example.com/infra/gateway\tdir", want: "https://git.example.com/infra/gateway"},
		{name: "quoted url", input: `git clone "https://git.example.com/infra/gateway"`, want: "https://git.example.com/infra/gateway"},
		{name: "short input", input: "git", want: "git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripGitCloneCommand(tc.input); got != tc.want {
				t.Fatalf("stripGitCloneCommand(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestGongfengReferencePath(t *testing.T) {
	cases := []struct {
		name, input, want string
		ok                bool
	}{
		{name: "https url", input: "https://git.example.com/infra/gateway", want: "infra/gateway", ok: true},
		// Without scheme, userinfo or colon the reference is treated as a bare
		// path; normalizeGongfengProjectPath strips the host afterwards.
		{name: "host without scheme kept as bare path", input: "git.example.com/infra/gateway/-/tree/master", want: "git.example.com/infra/gateway/-/tree/master", ok: true},
		{name: "http url subpage", input: "http://git.example.com/infra/gateway/-/tree/master", want: "infra/gateway/-/tree/master", ok: true},
		{name: "host case insensitive", input: "https://GIT.EXAMPLE.COM/infra/gateway", want: "infra/gateway", ok: true},
		{name: "scp form", input: "git@git.example.com:infra/gateway.git", want: "infra/gateway.git", ok: true},
		{name: "ssh url with port", input: "ssh://git@git.example.com:22/infra/gateway.git", want: "infra/gateway.git", ok: true},
		{name: "scp numeric-free group kept", input: "git@git.example.com:v2/gateway.git", want: "v2/gateway.git", ok: true},
		{name: "bare project path", input: "tencent/infra/gateway", want: "tencent/infra/gateway", ok: true},
		{name: "bare single segment", input: "gateway", want: "gateway", ok: true},
		{name: "wrong host", input: "https://github.com/infra/gateway", ok: false},
		{name: "wrong scp host", input: "git@github.com:infra/gateway.git", ok: false},
		{name: "host only", input: "https://git.example.com", ok: false},
		{name: "scp host only", input: "git@git.example.com:", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := gongfengReferencePath(tc.input)
			if ok != tc.ok {
				t.Fatalf("gongfengReferencePath(%q) ok = %v, want %v (path=%q)", tc.input, ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Fatalf("gongfengReferencePath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
