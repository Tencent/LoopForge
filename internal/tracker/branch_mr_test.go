package tracker

import "testing"

func TestPickBranchMR(t *testing.T) {
	raw := `{"data":[
		{"id":101,"iid":7,"state":"closed","source_branch":"feature/x","target_branch":"master"},
		{"id":102,"iid":9,"state":"merged","source_branch":"feature/x","target_branch":"integrate"},
		{"id":103,"iid":11,"state":"opened","source_branch":"feature/other","target_branch":"master"},
		{"id":104,"iid":8,"state":"opened","source_branch":"feature/x","target_branch":"master"}
	]}`
	got, ok := pickBranchMR(parseGongfengMRRecords(raw), "feature/x")
	if !ok || got.IID != 9 {
		t.Fatalf("want newest non-closed MR of the branch (!9), got %+v ok=%v", got, ok)
	}
	closedOnly := `[{"id":1,"iid":3,"state":"closed","source_branch":"feature/x"},{"id":2,"iid":5,"state":"closed","source_branch":"feature/x"}]`
	if got, ok := pickBranchMR(parseGongfengMRRecords(closedOnly), "feature/x"); !ok || got.IID != 5 {
		t.Fatalf("want newest closed MR when nothing is live, got %+v ok=%v", got, ok)
	}
	if _, ok := pickBranchMR(parseGongfengMRRecords(`{"data":[]}`), "feature/x"); ok {
		t.Fatal("empty search must not resolve an MR")
	}
}
