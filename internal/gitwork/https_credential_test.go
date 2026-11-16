package gitwork

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestScopedHTTPSCredential(t *testing.T) {
	const token = "fixture-gitlab-pat"
	ctx := WithHTTPSCredential(context.Background(), HTTPSCredential{BaseURL: "https://gitlab.example.test:8443/code", Token: token})
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://gitlab.example.test:8443/code/team/project.git", true},
		{"https://gitlab.example.test:8443/other/team/project.git", false},
		{"https://gitlab.example.test:8443/code-other/team/project.git", false},
		{"https://gitlab.example.test/code/team/project.git", false},
		{"http://gitlab.example.test:8443/code/team/project.git", false},
		{"https://elsewhere.example.test:8443/code/team/project.git", false},
	} {
		cmd := command(ctx, t.TempDir(), "", nil, []string{"credential", "fill"})
		cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		cmd.Stdin = strings.NewReader("url=" + tc.url + "\n\n")
		out, err := cmd.CombinedOutput()
		if found := strings.Contains(string(out), "password="+token); found != tc.want || (err == nil) != tc.want {
			t.Errorf("credential fill for %s returned authorized=%v, err=%v", tc.url, found, err)
		}
	}
	for _, arg := range command(ctx, t.TempDir(), "", nil, []string{"fetch"}).Args {
		if strings.Contains(arg, token) {
			t.Fatal("PAT found in Git arguments")
		}
	}
}
