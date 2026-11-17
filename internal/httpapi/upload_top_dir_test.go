package httpapi

import (
	"reflect"
	"testing"
)

func TestStripUploadTopDirKeepsPathsWithoutSharedTopDir(t *testing.T) {
	in := []string{"a/one.txt", "b/two.txt"}
	got, err := stripUploadTopDir(in)
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("stripUploadTopDir(%v) = %v, %v", in, got, err)
	}
	flat := []string{"one.txt"}
	if got, err := stripUploadTopDir(flat); err != nil || !reflect.DeepEqual(got, flat) {
		t.Fatalf("stripUploadTopDir(%v) = %v, %v", flat, got, err)
	}
}

func TestStripUploadTopDirRemovesSharedTopDir(t *testing.T) {
	got, err := stripUploadTopDir([]string{"pkg/SKILL.md", "pkg/scripts/run.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"SKILL.md", "scripts/run.sh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestStripUploadTopDirRejectsInvalidShortenedPath(t *testing.T) {
	if _, err := stripUploadTopDir([]string{"pkg/../x", "pkg/ok.txt"}); err == nil {
		t.Fatal("expected invalid shortened path to be rejected")
	}
}
