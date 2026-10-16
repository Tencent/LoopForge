package scriptview

import (
	"reflect"
	"testing"
)

func TestValidMaskRejectsBroaderMountRemoval(t *testing.T) {
	for _, target := range []string{
		"/workspace/CLAUDE.md", "/workspace/vendor/phpstan/phpstan/CLAUDE.md",
		"/workspace/repository/AGENTS.override.md", "/workspace/repository/.claude", "/workspace/.mcp.json",
	} {
		if !ValidMask(target) {
			t.Errorf("platform mask rejected: %q", target)
		}
	}
	for _, target := range []string{
		"/", "/workspace", "/workspace/", "/workspace/vendor", "/etc/CLAUDE.md",
		"/workspace/../etc/CLAUDE.md", "/workspace/repository/../CLAUDE.md", "/workspace//CLAUDE.md",
		"/workspace/.agentflow/config/CLAUDE.md", "/workspace/repo/.git/CLAUDE.md",
		"workspace/CLAUDE.md", "/workspace/CLAUDE.custom.md", "/workspace/CLAUDE.md\n",
		"/workspace/CODEBUDDY.md\x00", "/workspace/.claude/settings.json",
	} {
		if ValidMask(target) {
			t.Errorf("non-platform mount accepted: %q", target)
		}
	}
}

func TestConfigRejectsInvalidExecutionAndMaskRequests(t *testing.T) {
	for _, raw := range []string{
		`{`, `null`, `{}`, `{"argv":[]}`, `{"argv":[""]}`,
		`{"argv":["sh","hello\u0000there"]}`,
		`{"argv":["sh"],"privileged":true}`,
		`{"argv":["sh"]} {"argv":["other"]}`,
		`{"argv":["sh"]} false`,
		`{"argv":["sh"],"masks":["/workspace"]}`,
		`{"argv":["sh"],"masks":["/workspace/CLAUDE.md","/workspace/CLAUDE.md"]}`,
		`{"argv":["sh"],"hide":["/workspace"]}`,
		`{"argv":["sh"],"hide":["/workspace/CLAUDE.md","/workspace/CLAUDE.md"]}`,
	} {
		if _, err := parseConfig(raw); err == nil {
			t.Errorf("invalid configuration accepted: %s", raw)
		}
	}
}

func TestConfigRefreshesMasksParentFirst(t *testing.T) {
	config, err := parseConfig(`{"refresh":true,"hide":["/workspace/.claude/CLAUDE.md","/workspace/.claude"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Hide[0]; got != "/workspace/.claude" {
		t.Fatalf("parent mask must precede its children: %#v", config.Hide)
	}
	if !config.Refresh || len(config.Argv) != 0 || len(config.Masks) != 0 {
		t.Fatalf("mask refresh must not execute or detach: %#v", config)
	}
}

func TestRefreshCanOnlyAddCanonicalReadOnlyMasks(t *testing.T) {
	for _, raw := range []string{
		`{"refresh":true,"argv":["sh"],"hide":["/workspace/CLAUDE.md"]}`,
		`{"refresh":true,"masks":["/workspace/CLAUDE.md"]}`,
		`{"argv":["sh"],"hide":["/workspace/CLAUDE.md"]}`,
		`{"refresh":true,"hide":["/etc/CLAUDE.md"]}`,
	} {
		if _, err := parseConfig(raw); err == nil {
			t.Errorf("unsafe refresh/script combination accepted: %s", raw)
		}
	}
}

func TestConfigPreservesArgvAndDetachesChildMasksFirst(t *testing.T) {
	config, err := parseConfig(`{"argv":["sh","-c","printf '%s' \"$VALUE\""],"masks":["/workspace/.claude","/workspace/.claude/CLAUDE.md","/workspace/CLAUDE.md"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.Argv, []string{"sh", "-c", `printf '%s' "$VALUE"`}) {
		t.Fatalf("command arguments changed: %#v", config.Argv)
	}
	if got := config.Masks[0]; got != "/workspace/.claude/CLAUDE.md" {
		t.Fatalf("child mount must precede parent: %#v", config.Masks)
	}
}

func TestRunDispatchFailsClosedOnInvalidMode(t *testing.T) {
	for _, args := range [][]string{nil, {"--other-mode"}} {
		if handled, err := Run(args); handled || err != nil {
			t.Fatalf("ordinary gate invocation was intercepted: handled=%v err=%v", handled, err)
		}
	}
	for _, args := range [][]string{{Argument}, {Argument, `{}`, "unexpected"}, {Argument, `{"argv":["sh"],"masks":["/"]}`}, {ProbeArgument, "unexpected"}} {
		if handled, err := Run(args); !handled || err == nil {
			t.Fatalf("invalid private invocation did not fail closed: handled=%v err=%v", handled, err)
		}
	}
}
