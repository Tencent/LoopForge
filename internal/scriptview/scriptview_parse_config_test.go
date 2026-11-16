package scriptview

import (
	"slices"
	"strings"
	"testing"
)

func TestMaskOrderSortsByLengthThenLexically(t *testing.T) {
	input := []string{"/workspace/b/CLAUDE.md", "/workspace/.claude", "/workspace/a/CLAUDE.md", "/workspace/AGENTS.md"}

	longest := slices.Clone(input)
	slices.SortFunc(longest, maskOrder(true))
	wantLongest := []string{"/workspace/a/CLAUDE.md", "/workspace/b/CLAUDE.md", "/workspace/AGENTS.md", "/workspace/.claude"}
	if !slices.Equal(longest, wantLongest) {
		t.Fatalf("longest first = %v, want %v", longest, wantLongest)
	}

	shortest := slices.Clone(input)
	slices.SortFunc(shortest, maskOrder(false))
	wantShortest := []string{"/workspace/.claude", "/workspace/AGENTS.md", "/workspace/a/CLAUDE.md", "/workspace/b/CLAUDE.md"}
	if !slices.Equal(shortest, wantShortest) {
		t.Fatalf("shortest first = %v, want %v", shortest, wantShortest)
	}
}

func TestValidateMaskListsChecksEachListIndependently(t *testing.T) {
	if err := validateMaskLists([]string{"/workspace/CLAUDE.md"}, []string{"/workspace/CLAUDE.md"}); err != nil {
		t.Fatalf("same mask in different lists should be accepted: %v", err)
	}
	if err := validateMaskLists(nil, nil); err != nil {
		t.Fatalf("empty lists should be accepted: %v", err)
	}
	for _, list := range [][]string{
		{"/workspace/CLAUDE.md", "/workspace/CLAUDE.md"},
		{"/workspace/README.md"},
		{"/workspace/.git/CLAUDE.md"},
	} {
		err := validateMaskLists(list)
		if err == nil || !strings.Contains(err.Error(), "invalid or duplicate workspace instruction mask") {
			t.Fatalf("validateMaskLists(%v) = %v, want invalid/duplicate error", list, err)
		}
	}
}

func TestValidateConfigModeMessages(t *testing.T) {
	cases := []struct {
		config Config
		want   string
	}{
		{Config{Refresh: true, Argv: []string{"sh"}}, "instruction mask refresh cannot execute commands or detach masks"},
		{Config{Refresh: true, Masks: []string{"/workspace/CLAUDE.md"}}, "instruction mask refresh cannot execute commands or detach masks"},
		{Config{Argv: []string{"sh"}, Hide: []string{"/workspace/CLAUDE.md"}}, "workspace script commands cannot modify canonical instruction masks"},
		{Config{}, "workspace script view command is empty"},
		{Config{Argv: []string{""}}, "workspace script view command is empty"},
		{Config{Argv: []string{"sh", "a\x00b"}}, "workspace script view command contains a null byte"},
	}
	for _, tc := range cases {
		err := validateConfigMode(tc.config)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("validateConfigMode(%+v) = %v, want %q", tc.config, err, tc.want)
		}
	}
	for _, ok := range []Config{{Refresh: true, Hide: []string{"/workspace/CLAUDE.md"}}, {Argv: []string{"sh", "-c", "true"}}} {
		if err := validateConfigMode(ok); err != nil {
			t.Fatalf("validateConfigMode(%+v) = %v, want nil", ok, err)
		}
	}
}
