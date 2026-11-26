package domain

import (
	"strings"
	"testing"
)

func TestValidateResourceID(t *testing.T) {
	for _, id := range []string{"wa", "proj-ab12", "E2E_auto.v2", "需求-评审", strings.Repeat("a", 64)} {
		if err := ValidateResourceID(id); err != nil {
			t.Errorf("%q rejected: %v", id, err)
		}
	}
	for _, id := range []string{"", "wa bad", "a/b", `a\b`, "a?b", "a#b", "a%2Fb", "tab\tid", "nl\nid", strings.Repeat("a", 65)} {
		if err := ValidateResourceID(id); err == nil {
			t.Errorf("%q accepted", id)
		}
	}
}
