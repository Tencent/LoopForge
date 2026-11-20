package managedcli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func providerNames() []string {
	names := make([]string, 0, len(providerSpecs))
	for name := range providerSpecs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestDefaultProvidersIgnoreLegacyConfiguration(t *testing.T) {
	wantProviders := []string{"claude-code", "codebuddy", "codex"}
	for _, action := range []string{"check", "update"} {
		for _, input := range []string{"", "invalid-json", `{"enabled":false,"providers":{"codex":{"enabled":false},"codebuddy":{"enabled":false},"claude-code":{"enabled":false}},"registry":"https://not-the-registry.invalid","minimumAgeHours":0}`} {
			t.Run(action+"/"+input, func(t *testing.T) {
				f := newUpdaterFixture(t)
				t.Setenv(providerAllowlistEnv, "")
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
				if len(report.Results) != len(wantProviders) || len(f.state(t).Current) != len(wantProviders) || len(f.installs) != len(wantProviders) {
					t.Fatalf("default providers missing: %+v", report)
				}
				for i, result := range report.Results {
					if result.Provider != wantProviders[i] || result.Status != "updated" {
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

func TestEnabledProvidersSkipsEmptyEntries(t *testing.T) {
	for setting, want := range map[string][]string{
		"codex,": {"codex"},
		", ,":    {"claude-code", "codebuddy", "codex"},
	} {
		t.Setenv(providerAllowlistEnv, setting)
		got, err := EnabledProviders()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s=%q: got %v and %v, want %v", providerAllowlistEnv, setting, got, err, want)
		}
	}
}

func TestEnabledProvidersRestrictsScheduledAndManualUpdates(t *testing.T) {
	f := newUpdaterFixture(t)
	ctx := context.Background()
	t.Setenv(providerAllowlistEnv, " ,pi,,opencode,pi, ")
	report, err := f.manager.Check(ctx)
	must(t, err)
	if len(report.Results) != 2 || report.Results[0].Provider != "opencode" || report.Results[1].Provider != "pi" {
		t.Fatalf("unexpected scheduled providers: %+v", report.Results)
	}
	if _, err := f.manager.UpdateProvider(ctx, "cursor"); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("manual update bypassed the allowlist: %v", err)
	}
	f.setVersion("2.0.0")
	_, err = f.manager.UpdateProvider(ctx, "pi")
	must(t, err)
	t.Setenv(providerAllowlistEnv, "opencode")
	report, err = f.manager.Update(ctx)
	must(t, err)
	if len(report.Results) != 1 || report.Results[0].Provider != "opencode" || f.state(t).Current["pi"].Version != "2.0.0" {
		t.Fatalf("bulk update altered the excluded installation: %+v", report.Results)
	}
	_, err = f.manager.Rollback(ctx, "pi")
	must(t, err)
	if f.state(t).Current["pi"].Version != "1.0.0" {
		t.Fatal("the allowlist blocked rollback of an existing installation")
	}
}

func TestInvalidProviderAllowlistRejectsUpdates(t *testing.T) {
	f := newUpdaterFixture(t)
	t.Setenv(providerAllowlistEnv, "pi,,zcode,")
	if _, err := f.manager.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "zcode") {
		t.Fatalf("unexpected configuration error: %v", err)
	}
	if len(f.installs) != 0 || len(f.state(t).Current) != 0 {
		t.Fatal("invalid allowlist applied a partial update")
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
	for provider, spec := range want {
		if !reflect.DeepEqual(providerSpecs[provider], spec) {
			t.Fatalf("official package or compatibility arguments changed for %s", provider)
		}
	}
	all := []string{"agy", "claude-code", "codebuddy", "codex", "cursor", "dsh", "kimi", "opencode", "pi"}
	if !reflect.DeepEqual(providerNames(), all) || Supports("zcode") {
		t.Fatal("managed CLI scope changed")
	}
	for _, root := range []string{"", "/", "relative", "/tmp/../tmp/cli"} {
		if _, err := NewManager(root); err == nil {
			t.Fatalf("accepted unsafe root %q", root)
		}
	}
}
