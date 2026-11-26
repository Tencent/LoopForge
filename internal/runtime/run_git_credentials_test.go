package runtime

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

func TestSOPGitCredentialsUseAnEphemeralHostScopedRelay(t *testing.T) {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITLAB_TOKEN", "GLAB_TOKEN", "CNB_TOKEN"} {
		t.Setenv(key, "inherited-must-not-leak")
	}
	credentials := RunCredentials{GitHTTPCredentials: map[string]domain.GitHTTPExecutionCredential{
		"cnb.cool":           {GitHTTPCredential: domain.GitHTTPCredential{Host: "cnb.cool", Username: "cnb", Platform: "other"}, Token: "fake-cnb-sop-secret"},
		"hidden.example.org": {GitHTTPCredential: domain.GitHTTPCredential{Host: "hidden.example.org"}, Token: "hidden-sop-secret"},
	}}
	workspace := t.TempDir()
	script := `import os,subprocess
def fill(protocol,host):
 p=subprocess.run(['git','credential','fill'],input='protocol='+protocol+'\nhost='+host+'\n\n',text=True,capture_output=True)
 return dict(line.split('=',1) for line in p.stdout.splitlines() if '=' in line)
for key in ['GH_TOKEN','GITHUB_TOKEN','GH_ENTERPRISE_TOKEN','GITLAB_TOKEN','GLAB_TOKEN','CNB_TOKEN','AGENTFLOW_GIT_TOKEN']:
 assert not os.getenv(key),key
c=fill('https','cnb.cool')
assert c['username']=='cnb' and c['password']=='fake-cnb-sop-secret'
assert fill('https','hidden.example.org')['password']=='hidden-sop-secret'
for protocol,host in [('http','cnb.cool'),('https','cnb.cool:444'),('https','other.invalid')]:
 assert not fill(protocol,host).get('password')
print('BOUND_OK')
print(c['password'])
print('GATEWAY='+os.environ['AGENTFLOW_SESSION_GATEWAY'])
`
	command := CommandSpec{Argv: []string{"python3", "-c", script}, Provider: "script", CredentialBindings: []CredentialBinding{BindGit}}
	out, err := ExecuteLocalCommand(context.Background(), workspace, command, credentials, nil)
	if err != nil || !strings.Contains(out, "BOUND_OK") || !strings.Contains(out, "[redacted]") || strings.Contains(out, "fake-cnb-sop-secret") {
		t.Fatalf("SOP binding: %v %s", err, out)
	}
	var gateway string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "GATEWAY=") {
			gateway = strings.TrimPrefix(line, "GATEWAY=")
		}
	}
	client := http.Client{Timeout: time.Second}
	if res, err := client.Post(gateway, "application/json", strings.NewReader(`{}`)); err == nil {
		res.Body.Close()
		t.Fatal("credential relay survived the command")
	}
	filepath.WalkDir(workspace, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() != "agentflow-helper" {
			if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "fake-cnb-sop-secret") {
				t.Errorf("credential persisted in %s", path)
			}
		}
		return err
	})
}

func testHTTPSCredentials(token string) map[string]domain.GitHTTPExecutionCredential {
	username, token := gitwork.SplitToken(token)
	result := map[string]domain.GitHTTPExecutionCredential{}
	for _, host := range []string{"github.com", "git.example.com"} {
		result[host] = domain.GitHTTPExecutionCredential{GitHTTPCredential: domain.GitHTTPCredential{Host: host, Username: username, Platform: domain.GitPlatformGitHub}, Token: token}
	}
	return result
}
