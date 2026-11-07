package managedcli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDefaultProvidersIgnoreLegacyConfiguration(t *testing.T) {
	for _, action := range []string{"check", "update"} {
		for _, input := range []string{"", "invalid-json", `{"enabled":false,"providers":{"codex":{"enabled":false},"codebuddy":{"enabled":false},"claude-code":{"enabled":false}},"registry":"https://not-the-registry.invalid","minimumAgeHours":0}`} {
			t.Run(action+"/"+input, func(t *testing.T) {
				f := newUpdaterFixture(t)
				path := filepath.Join(f.manager.Root, "config.json")
				envPath := filepath.Join(t.TempDir(), "legacy.json")
				t.Setenv("AGENTFLOW_MANAGED_CLI_CONFIG", envPath)
				if input != "" {
					writeTestFile(t, path, input)
					writeTestFile(t, envPath, input)
				}
				var report Report
				var err error
				if action == "check" {
					report, err = f.manager.Check(context.Background())
				} else {
					report, err = f.manager.Update(context.Background())
				}
				must(t, err)
				if len(report.Results) != 3 || len(f.state(t).Current) != 3 || len(f.installs) != 3 {
					t.Fatalf("default providers missing: %+v", report)
				}
				for _, result := range report.Results {
					if result.Status != "updated" {
						t.Fatalf("legacy policy affected %s: %+v", action, result)
					}
				}
				for _, legacy := range []string{path, envPath} {
					data, err := os.ReadFile(legacy)
					if input == "" {
						if !os.IsNotExist(err) {
							t.Fatal("updater created a configuration file")
						}
					} else if err != nil || string(data) != input {
						t.Fatal("updater migrated, deleted or rewrote legacy configuration")
					}
				}
			})
		}
	}
}

func TestFixedProvidersAndSafetyBounds(t *testing.T) {
	if npmRegistry != "https://registry.npmjs.org" || observationWindow != 24*time.Hour || maintenanceTimeout != 30*time.Minute || commandTimeout != 10*time.Minute {
		t.Fatal("fixed update safety bounds changed")
	}
	want := map[string]providerSpec{
		"codex": {
			Package: "@openai/codex", Binary: "codex", Help: []string{"exec", "--help"},
			Flags: []string{"--json", "--sandbox", "--skip-git-repo-check", "--model"},
		},
		"codebuddy": {
			Package: "@tencent-ai/codebuddy-code", Binary: "codebuddy", Help: []string{"--help"},
			Flags: []string{"--print", "--output-format", "--permission-mode", "--setting-sources", "--system-prompt-file", "--mcp-config", "--strict-mcp-config", "--settings", "--resume", "--plugin-dir"},
		},
		"claude-code": {
			Package: "@anthropic-ai/claude-code", Binary: "claude", Help: []string{"--help"},
			Flags: []string{"--print", "--output-format", "--verbose", "--permission-mode", "--setting-sources", "--system-prompt-file", "--mcp-config", "--settings", "--resume", "--plugin-dir"},
		},
	}
	if !reflect.DeepEqual(providerSpecs, want) || !reflect.DeepEqual(providerNames(), []string{"claude-code", "codebuddy", "codex"}) {
		t.Fatal("official packages or required compatibility arguments changed")
	}
	for _, root := range []string{"", "/", "relative", "/tmp/../tmp/cli"} {
		if _, err := NewManager(root); err == nil {
			t.Fatalf("accepted unsafe root %q", root)
		}
	}
}
