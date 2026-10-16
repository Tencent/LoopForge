package domain

import (
	"strings"
	"testing"
)

func TestParseGongfengMR(t *testing.T) {
	project, iid, url, ok := ParseGongfengMR("MR URL: https://git.example.com/example/team-project/-/merge_requests/138\nHEAD abc")
	if !ok || project != "example/team-project" || iid != 138 || !strings.Contains(url, "merge_requests/138") {
		t.Fatalf("got project=%q iid=%d url=%q ok=%v", project, iid, url, ok)
	}
	project, iid, _, ok = ParseGongfengMR("https://git.example.com/x/y/merge_requests/1")
	if !ok || project != "x/y" || iid != 1 {
		t.Fatalf("short form project=%q iid=%d ok=%v", project, iid, ok)
	}
	if _, _, _, ok = ParseGongfengMR("no mr here"); ok {
		t.Fatal("expected no match")
	}
}

func TestParseGongfengMRAcceptsHTTPAndCanonicalizes(t *testing.T) {
	raw := "创建结果：http://git.example.com/dnspod/vpcdns/vpcdns_oss/merge_requests/557"
	project, iid, url, ok := ParseGongfengMR(raw)
	want := "https://git.example.com/dnspod/vpcdns/vpcdns_oss/-/merge_requests/557"
	if !ok || project != "dnspod/vpcdns/vpcdns_oss" || iid != 557 || url != want {
		t.Fatalf("got project=%q iid=%d url=%q ok=%v", project, iid, url, ok)
	}
	if !SameGongfengMR(raw, "https://git.example.com/dnspod/vpcdns/vpcdns_oss/-/merge_requests/557") {
		t.Fatal("http and https forms of the same MR must match")
	}
}

func TestSameGongfengMRIgnoresURLForm(t *testing.T) {
	a := "https://git.example.com/example/team-project/merge_requests/248"
	b := "https://git.example.com/example/team-project/-/merge_requests/248"
	if !SameGongfengMR(a, b) {
		t.Fatal("expected /- and non-/- forms to match")
	}
	if SameGongfengMR(a, "https://git.example.com/example/team-project/merge_requests/183") {
		t.Fatal("different IIDs must not match")
	}
}

func TestParseGongfengMRsKeepsOrderAndDedups(t *testing.T) {
	refs := ParseGongfengMRs("-https://git.example.com/org/repo/merge_requests/282\n+https://git.example.com/org/repo/-/merge_requests/286\nhttps://git.example.com/org/repo/merge_requests/282")
	if len(refs) != 2 || refs[0].IID != 282 || refs[1].IID != 286 {
		t.Fatalf("refs=%+v", refs)
	}
}

func TestMRArtifactPriorityIgnoresPatches(t *testing.T) {
	if got := MRArtifactPriority("repository-patch", "", "mr"); got != 0 {
		t.Fatalf("patch priority=%d", got)
	}
	if got := MRArtifactPriority("mr-body", "", "mr"); got <= MRArtifactPriority("closeout", "", "close") {
		t.Fatalf("mr-body should outrank closeout: %d", got)
	}
	if got := MRArtifactPriority("notes", "", "verify"); got != 0 {
		t.Fatalf("unrelated artifact priority=%d", got)
	}
}
