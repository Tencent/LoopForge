package domain

import "testing"

// TestParseGongfengRepoURLReferenceEdgeCases pins the boundary behaviour of the
// pieces split out of ParseGongfengRepoURL (stripGitCloneCommand and
// gongfengReferencePath) that the table in TestParseGongfengRepoURL does not
// reach: outer quotes, extra whitespace after "git clone", "@" inside userinfo,
// numeric groups on https URLs, ports on https URLs, a trailing slash after the
// ".git" suffix, and a foreign host written with "@" but without a scheme.
func TestParseGongfengRepoURLReferenceEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		project string
		clone   string
		ok      bool
	}{
		{name: "double quoted url", input: `"https://git.example.com/infra/gateway"`, project: "infra/gateway", ok: true},
		{name: "single quoted scp", input: `'git@git.example.com:infra/gateway.git'`, project: "infra/gateway", clone: "git@git.example.com:infra/gateway.git", ok: true},
		{name: "extra spaces after git clone", input: "git clone   https://git.example.com/infra/gateway.git", project: "infra/gateway", ok: true},
		{name: "email userinfo keeps last at", input: "https://dev@corp.com@git.example.com/infra/gateway", project: "infra/gateway", ok: true},
		{name: "https numeric group is not a port", input: "https://git.example.com/2024/gateway", project: "2024/gateway", ok: true},
		{name: "https url with port", input: "https://git.example.com:8443/infra/gateway", project: "infra/gateway", ok: true},
		{name: "scp trailing slash after git suffix", input: "git@git.example.com:infra/gateway.git/", project: "infra/gateway", clone: "git@git.example.com:infra/gateway.git", ok: true},
		{name: "userinfo slash form on gongfeng host", input: "git@git.example.com/infra/gateway", project: "infra/gateway", clone: "git@git.example.com:infra/gateway.git", ok: true},
		{name: "userinfo slash form on foreign host", input: "git@github.com/infra/gateway", ok: false},
		{name: "foreign host with port", input: "https://github.com:443/infra/gateway", ok: false},
		{name: "host only with trailing slash", input: "https://git.example.com/", ok: false},
		{name: "ssh host only", input: "ssh://git@git.example.com", ok: false},
		{name: "git clone without url", input: "git clone   ", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := ParseGongfengRepoURL(tc.input)
			if ok != tc.ok {
				t.Fatalf("ParseGongfengRepoURL(%q) ok = %v, want %v (info=%+v)", tc.input, ok, tc.ok, info)
			}
			if !ok {
				if info != (GongfengRepoInfo{}) {
					t.Fatalf("rejected input returned non-zero info %+v", info)
				}
				return
			}
			if info.Project != tc.project {
				t.Fatalf("project = %q, want %q", info.Project, tc.project)
			}
			if want := "https://" + GongfengRepoHost + "/" + tc.project; info.WebURL != want {
				t.Fatalf("webURL = %q, want %q", info.WebURL, want)
			}
			wantClone := tc.clone
			if wantClone == "" {
				wantClone = info.WebURL + ".git"
			}
			if info.CloneURL != wantClone {
				t.Fatalf("cloneURL = %q, want %q", info.CloneURL, wantClone)
			}
		})
	}
}

// TestGongfengReferencePathDecorationBoundaries checks the helper directly for
// the branches whose mistakes ParseGongfengRepoURL could partly mask: digit
// stripping applies only after a host colon, and an "@" without a scheme still
// routes through the host check instead of the bare-path shortcut.
func TestGongfengReferencePathDecorationBoundaries(t *testing.T) {
	cases := []struct {
		name, input, want string
		ok                bool
	}{
		{name: "https numeric first segment kept", input: "https://git.example.com/2024/gateway", want: "2024/gateway", ok: true},
		{name: "https port stripped", input: "https://git.example.com:8443/infra/gateway", want: "infra/gateway", ok: true},
		{name: "scp without user", input: "git.example.com:infra/gateway.git", want: "infra/gateway.git", ok: true},
		{name: "userinfo slash form strips host", input: "git@git.example.com/infra/gateway", want: "infra/gateway", ok: true},
		{name: "userinfo slash form foreign host", input: "git@github.com/infra/gateway", ok: false},
		{name: "email userinfo", input: "https://dev@corp.com@git.example.com/infra/gateway", want: "infra/gateway", ok: true},
		{name: "scheme without at or colon still checks host", input: "https://github.com/infra/gateway", ok: false},
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
