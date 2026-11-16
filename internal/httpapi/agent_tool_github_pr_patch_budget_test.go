package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A patch of exactly githubPRFilePatchRunes runes is within the per-file limit
// and must be kept verbatim, without the truncation notice.
func TestGitHubPRFilePatchKeepsPatchAtRuneLimit(t *testing.T) {
	exact := strings.Repeat("改", githubPRFilePatchRunes)
	kept, omitted, remaining := githubPRFilePatch(exact, githubPRFilesPatchBudget)
	if kept != exact || omitted || remaining != githubPRFilesPatchBudget-githubPRFilePatchRunes {
		t.Fatalf("patch at limit: kept %d runes (truncated=%t) omitted=%t remaining=%d", len([]rune(kept)), strings.Contains(kept, "已截断"), omitted, remaining)
	}
}

// githubPRFiles must thread the budget returned by githubPRFilePatch through
// every file: once full-size patches use it up, the next one is omitted
// without being charged, and a later small patch still fits the remainder.
func TestGitHubPRFilesCarriesPatchBudgetAcrossFiles(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	full := strings.Repeat("x", githubPRFilePatchRunes)
	fitting := githubPRFilesPatchBudget / githubPRFilePatchRunes
	small := strings.Repeat("y", githubPRFilesPatchBudget-fitting*githubPRFilePatchRunes)
	items := make([]string, 0, fitting+2)
	for i := 0; i <= fitting; i++ {
		items = append(items, fmt.Sprintf(`{"filename":"f%02d.go","status":"modified","patch":%q}`, i, full))
	}
	items = append(items, fmt.Sprintf(`{"filename":"small.go","status":"modified","patch":%q}`, small))
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/pulls/7/files": githubJSON(http.StatusOK, "["+strings.Join(items, ",")+"]"),
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubPRFiles, `{"repository":"app","number":7}`)
	if isErr {
		t.Fatalf("pr files error: %.400s", text)
	}
	var got struct {
		Files []struct {
			Filename     string `json:"filename"`
			Patch        string `json:"patch"`
			PatchOmitted bool   `json:"patchOmitted"`
		} `json:"files"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode %.400s: %v", text, err)
	}
	if got.Count != fitting+2 || len(got.Files) != fitting+2 {
		t.Fatalf("count = %d files = %d, want %d", got.Count, len(got.Files), fitting+2)
	}
	for i := 0; i < fitting; i++ {
		if file := got.Files[i]; file.Patch != full || file.PatchOmitted {
			t.Fatalf("%s: kept %d runes omitted=%t, want full patch", file.Filename, len(file.Patch), file.PatchOmitted)
		}
	}
	if over := got.Files[fitting]; over.Patch != "" || !over.PatchOmitted {
		t.Fatalf("%s over budget: kept %d runes omitted=%t, want omitted", over.Filename, len(over.Patch), over.PatchOmitted)
	}
	if last := got.Files[fitting+1]; last.Filename != "small.go" || last.Patch != small || last.PatchOmitted {
		t.Fatalf("%s: kept %d runes omitted=%t, want remaining budget used", last.Filename, len(last.Patch), last.PatchOmitted)
	}
}
