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

func TestBranchMRsIntoKeepOnlyTheTargetAndCarryHeads(t *testing.T) {
	raw := `{"data":[
		{"id":101,"iid":7,"state":"merged","source_branch":"feature/x","target_branch":"master","sha":"old","web_url":"https://git.example.com/g/r/merge_requests/7"},
		{"id":102,"iid":9,"state":"opened","source_branch":"feature/x","target_branch":"release","sha":"new"},
		{"id":103,"iid":11,"state":"opened","source_branch":"feature/other","target_branch":"master"},
		{"id":104,"iid":12,"state":"opened","target_branch":"master","sha":"tip"}
	]}`
	got := gongfengMRsInto(parseGongfengMRRecords(raw), "g/r", "feature/x", "master")
	if len(got) != 2 || got[0].IID != 7 || got[0].HeadSHA != "old" || got[0].GongfengState != "merged" || got[1].IID != 12 || got[1].HeadSHA != "tip" {
		t.Fatalf("gongfeng MRs into master = %+v", got)
	}
	if got := gongfengMRsInto(parseGongfengMRRecords(raw), "g/r", "feature/x", "main"); len(got) != 0 {
		t.Fatalf("no MR targets main, got %+v", got)
	}

	var pulls []githubPull
	for _, p := range []struct{ head, sha, base, state string }{{"feat", "old", "main", "closed"}, {"feat", "new", "release", "open"}, {"other", "x", "main", "open"}, {"feat", "tip", "main", "open"}} {
		var pull githubPull
		pull.Number, pull.State = len(pulls)+1, p.state
		pull.Head.Ref, pull.Head.SHA, pull.Base.Ref = p.head, p.sha, p.base
		pulls = append(pulls, pull)
	}
	prs := githubPullsInto(pulls, "github:o/r", "feat", "main")
	if len(prs) != 2 || prs[0].State != "closed" || prs[0].HeadSHA != "old" || prs[1].State != "opened" || prs[1].HeadSHA != "tip" || prs[1].TargetBranch != "main" {
		t.Fatalf("github pulls into main = %+v", prs)
	}
}
