package domain

import "testing"

// A URL already carried by the picked MR (for example the web URL returned by
// the work-branch lookup) is authoritative; only a missing URL is derived from
// the project and IID.
func TestPickRepositoryDeliveryMRKeepsProvidedURL(t *testing.T) {
	rc := RepositoryContext{RepositoryID: "repo-a", Name: "a", GongfengProjectID: "g/a", WorkBranch: "feature/x"}
	const webURL = "https://git.internal.example.com/g/a/-/merge_requests/12"
	picked, ok := pickRepositoryDeliveryMR(rc, nil, func(RepositoryContext) (DeliveryMR, bool) {
		return DeliveryMR{Project: "G/A", IID: 12, URL: webURL}, true
	})
	if !ok || picked.URL != webURL || picked.Project != "G/A" || picked.RepositoryID != "repo-a" || picked.Repository != "a" {
		t.Fatalf("provided URL and project must be kept: %+v ok=%v", picked, ok)
	}

	candidates := []artifactMRCandidate{{mr: DeliveryMR{Project: "g/a", IID: 4, URL: webURL}}}
	picked, ok = pickRepositoryDeliveryMR(rc, candidates, nil)
	if !ok || picked.IID != 4 || picked.URL != webURL {
		t.Fatalf("candidate URL must be kept: %+v ok=%v", picked, ok)
	}
}

// Runs with Gongfeng repositories but no matching MR resolve to an empty,
// non-nil list, the same shape as the fallback path.
func TestResolveRunDeliveryMRsWithoutMatchReturnsEmptySlice(t *testing.T) {
	run := Run{
		Repositories: []RepositoryContext{{RepositoryID: "repo-a", Name: "a", GongfengProjectID: "g/a", WorkBranch: "feature/x"}},
		Artifacts:    []Artifact{{Name: "mr-body", Content: "https://git.example.com/g/other/-/merge_requests/3"}},
	}
	got := ResolveRunDeliveryMRs(run, func(RepositoryContext) (DeliveryMR, bool) { return DeliveryMR{}, false })
	if got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", got)
	}
}
