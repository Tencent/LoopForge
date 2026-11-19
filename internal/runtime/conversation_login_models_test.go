package runtime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDiscoverRunnerLoginModels(t *testing.T) {
	tests := []struct {
		provider, configEnv, loginFile, cli string
	}{
		{"codex", "CODEX_HOME", "auth.json", `
test "$1" = app-server
test -z "${OPENAI_API_KEY:-}"
test -f "$CODEX_HOME/auth.json"
read -r request; case "$request" in *'"method":"initialize"'*) ;; *) exit 2 ;; esac
printf '%s\n' '{"id":1,"result":{}}'
read -r request; case "$request" in *'"method":"initialized"'*) ;; *) exit 3 ;; esac
read -r request; case "$request" in *'"method":"account/read"'*) ;; *) exit 4 ;; esac
printf '%s\n' '{"id":2,"result":{"account":{"email":"fixture@example.test"}}}'
read -r request; case "$request" in *'"method":"model/list"'*) ;; *) exit 5 ;; esac
printf '%s\n' '{"id":3,"result":{"data":[{"id":"display-alias","model":"fixture-model","displayName":"Fixture Model","supportedReasoningEfforts":[{"reasoningEffort":"high"},{"reasoningEffort":"xhigh"}]},{"model":"legacy"},{"model":"no-effort","supportedReasoningEfforts":[]}]}}'
`},
		{"claude-code", "CLAUDE_CONFIG_DIR", ".credentials.json", `
test -z "${ANTHROPIC_API_KEY:-}"
test -f "$CLAUDE_CONFIG_DIR/.credentials.json"
if [ "$1" = auth ] && [ "$2" = status ]; then
  printf '%s\n' '{"loggedIn":true,"email":"fixture@example.test"}'
  exit
fi
read -r request; case "$request" in *'"subtype":"initialize"'*) ;; *) exit 2 ;; esac
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"loopforge-models","response":{"models":[{"value":"fixture-model","displayName":"Fixture Model","supportsEffort":true,"supportedEffortLevels":["high","xhigh"]},{"value":"legacy"},{"value":"no-effort","supportsEffort":false}]}}}'
`},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			dir, isolated := t.TempDir(), t.TempDir()
			t.Setenv("TMPDIR", isolated)
			t.Setenv(tc.configEnv, dir)
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
			t.Setenv("OPENAI_API_KEY", "must-stay-on-host")
			t.Setenv("ANTHROPIC_API_KEY", "must-stay-on-host")
			if err := os.WriteFile(filepath.Join(dir, tc.loginFile), []byte(`{"test":true}`), 0600); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, "cli")
			script := "#!/bin/sh\nset -eu\ncase \"$HOME\" in " + isolated + "/*/home) ;; *) exit 9 ;; esac\n" + tc.cli
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			models, err := DiscoverRunnerLoginModels(context.Background(), tc.provider, bin)
			if err != nil || len(models) != 3 || models[0].ID != "fixture-model" || models[0].Name != "Fixture Model" {
				t.Fatalf("models=%+v error=%v", models, err)
			}
			if got := models[0].DiscoveredReasoningEfforts; got == nil || !slices.Equal(*got, []string{"high", "xhigh"}) {
				t.Fatalf("native effort levels were not preserved: %v", got)
			}
			if legacy, noEffort := models[1].DiscoveredReasoningEfforts, models[2].DiscoveredReasoningEfforts; legacy != nil || noEffort == nil || len(*noEffort) > 0 {
				t.Fatalf("omitted and empty capability metadata must stay distinct: legacy=%v no effort=%v", legacy, noEffort)
			}
			if dirs, err := os.ReadDir(isolated); err != nil || len(dirs) != 0 {
				t.Fatalf("lookup left its credential directory behind: %v %v", dirs, err)
			}
			if err := os.Remove(filepath.Join(dir, tc.loginFile)); err != nil {
				t.Fatal(err)
			}
			if _, err := DiscoverRunnerLoginModels(context.Background(), tc.provider, bin); err == nil || !strings.Contains(err.Error(), "登录态不可用") {
				t.Fatalf("missing login error = %v", err)
			}
		})
	}
}
