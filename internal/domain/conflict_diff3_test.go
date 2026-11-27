package domain

import "testing"

func TestParseConflictMarkersExcludesCommonAncestor(t *testing.T) {
	for _, marker := range []string{"||||||| base", "||||||| parent of deadbeef"} {
		data := "<<<<<<< HEAD\ncurrent\n" + marker + "\noriginal\n=======\nincoming\n>>>>>>> topic\n"
		blocks := ParseConflictMarkers("file.txt", []byte(data), 4)
		if len(blocks) != 1 || blocks[0].Ours != "current" || blocks[0].Theirs != "incoming" {
			t.Fatalf("ancestor section leaked into conflict sides: %+v", blocks)
		}
	}
}

func TestParseConflictMarkersKeepsTwoWayConflicts(t *testing.T) {
	data := "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> topic\n<<<<<<< HEAD\nnext\n||||||| base\nprevious\n=======\nother\n>>>>>>> topic\n"
	blocks := ParseConflictMarkers("file.txt", []byte(data), 4)
	if len(blocks) != 2 || blocks[0].Ours != "ours" || blocks[0].Theirs != "theirs" || blocks[1].Ours != "next" || blocks[1].Theirs != "other" {
		t.Fatalf("unexpected conflict sides: %+v", blocks)
	}
}
