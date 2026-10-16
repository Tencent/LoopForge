//go:build live

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/mcpclient"
	"agentflow/internal/repo"
)

func TestLiveTaihuMCPConnectivity(t *testing.T) {
	loadLiveDotEnv()
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_MYSQL_DSN"))
	if dsn == "" {
		t.Fatal("AGENTFLOW_MYSQL_DSN is empty")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	var login string
	var pat string
	for _, user := range st.Users() {
		if strings.TrimSpace(user.TaihuPAT) == "" {
			continue
		}
		if user.ChineseName == "胡锦康" || strings.EqualFold(user.LoginName, "example-maintainer") || strings.Contains(strings.ToLower(user.LoginName), "crispin") {
			login, pat = user.LoginName, user.TaihuPAT
			break
		}
		if login == "" {
			login, pat = user.LoginName, user.TaihuPAT
		}
	}
	if pat == "" {
		t.Fatal("no user with Taihu PAT in mysql")
	}
	t.Logf("using login=%s pat_len=%d", login, len(pat))

	headers := map[string]string{"Authorization": "Bearer " + strings.TrimSpace(pat)}
	targets := []struct {
		name string
		url  string
	}{
		{name: "iwiki", url: "https://prod.mcp.it.example.com/app_iwiki_mcp/mcp3"},
		{name: "rainbow", url: "https://mcpgw.knot.example.com/rainbow"},
		{name: "gongfeng", url: "https://mcpgw.knot.example.com/gongfeng"},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			client := mcpclient.New(target.url, headers).WithTimeout(45 * time.Second)
			tools, err := client.ListTools()
			if err != nil {
				t.Fatalf("%s tools/list: %v", target.name, err)
			}
			if len(tools) == 0 {
				t.Fatalf("%s returned no tools", target.name)
			}
			names := make([]string, 0, len(tools))
			for i, tool := range tools {
				if i >= 8 {
					break
				}
				names = append(names, tool.Name)
			}
			fmt.Printf("%s ok tools=%d sample=%s\n", target.name, len(tools), strings.Join(names, ", "))
		})
	}
}

func loadLiveDotEnv() {
	var candidates []string
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for i := 0; i < 6; i++ {
			candidates = append(candidates, filepath.Join(dir, ".env"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if k == "" || os.Getenv(k) != "" {
				continue
			}
			switch strings.ToUpper(k) {
			case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
				continue
			}
			os.Setenv(k, v)
		}
		return
	}
}
