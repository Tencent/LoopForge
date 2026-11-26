package repo

import (
	"strings"
	"testing"
)

func TestClipKnowledgeLabelKeepsShortValuesAndTruncatesLongOnes(t *testing.T) {
	if got := clipKnowledgeLabel("普通标题"); got != "普通标题" {
		t.Fatalf("short label changed: %q", got)
	}
	long := strings.Repeat("长", maxKnowledgeLabelRunes+20)
	got := clipKnowledgeLabel(long)
	if len([]rune(got)) != maxKnowledgeLabelRunes {
		t.Fatalf("clipped runes = %d, want %d", len([]rune(got)), maxKnowledgeLabelRunes)
	}
	if !strings.HasPrefix(long, got) {
		t.Fatal("clipped label is not a prefix of the original")
	}
}
