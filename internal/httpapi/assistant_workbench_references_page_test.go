package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestSortAssistantReferencesOrdersByNameTypeID(t *testing.T) {
	refs := []domain.AssistantReference{
		{Type: "skill", ID: "b", Name: "deploy"},
		{Type: "agent", ID: "z", Name: "deploy"},
		{Type: "skill", ID: "a", Name: "deploy"},
		{Type: "agent", ID: "x", Name: "build"},
	}
	sortAssistantReferences(refs)
	got := []string{}
	for _, ref := range refs {
		got = append(got, ref.Name+"/"+ref.Type+"/"+ref.ID)
	}
	want := []string{"build/agent/x", "deploy/agent/z", "deploy/skill/a", "deploy/skill/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestPageAssistantReferencesClampsOffsetAndReportsNext(t *testing.T) {
	refs := make([]domain.AssistantReference, 5)
	for i := range refs {
		refs[i].ID = string(rune('a' + i))
	}
	cases := []struct {
		offset, size int
		ids          string
		next         int
	}{
		{offset: 0, size: 2, ids: "ab", next: 2},
		{offset: 2, size: 2, ids: "cd", next: 4},
		{offset: 4, size: 2, ids: "e", next: 0},
		{offset: -3, size: 2, ids: "ab", next: 2},
		{offset: 9, size: 2, ids: "", next: 0},
		{offset: 0, size: 5, ids: "abcde", next: 0},
	}
	for _, tc := range cases {
		page, next := pageAssistantReferences(refs, tc.offset, tc.size)
		ids := ""
		for _, ref := range page {
			ids += ref.ID
		}
		if page == nil || ids != tc.ids || next != tc.next {
			t.Fatalf("offset=%d size=%d: ids=%q next=%d nil=%v, want ids=%q next=%d", tc.offset, tc.size, ids, next, page == nil, tc.ids, tc.next)
		}
	}
}

// The global picker must return sorted pages of workbenchReferencePageSize
// items and hand out the next offset until the last page.
func TestWorkbenchReferencesGlobalScopeReturnsSortedPages(t *testing.T) {
	h, u := workbenchFixture(t)
	const total = workbenchReferencePageSize + 5
	// Agents and skills interleave by name, so the merged list is only in
	// order if the handler sorts it after collecting every type.
	for i := total - 1; i >= 0; i-- {
		name := fmt.Sprintf("pagecase-%02d", i)
		var err error
		if i%2 == 0 {
			err = h.repo.PutAgent(domain.AgentDefinition{ID: name, ProjectID: "workbench", Name: name})
		} else {
			err = h.repo.PutSkill(domain.SkillDefinition{ID: name, ProjectID: "workbench", Name: name})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	page := func(offset int) ([]domain.AssistantReference, int) {
		t.Helper()
		c, rec := workbenchContext(u, "GET", fmt.Sprintf("/api/assistant/references?scope=all&q=pagecase&offset=%d", offset), bytes.NewBuffer(nil))
		h.workbenchReferences(c)
		var result struct {
			Items      []domain.AssistantReference `json:"items"`
			NextOffset int                         `json:"nextOffset"`
		}
		if rec.Code != 200 {
			t.Fatalf("offset=%d status=%d %s", offset, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Items, result.NextOffset
	}
	ids := func(refs []domain.AssistantReference) []string {
		out := []string{}
		for _, ref := range refs {
			out = append(out, ref.ID)
		}
		return out
	}
	want := func(from, to int) []string {
		out := []string{}
		for i := from; i < to; i++ {
			out = append(out, fmt.Sprintf("pagecase-%02d", i))
		}
		return out
	}
	items, next := page(0)
	if got := ids(items); !reflect.DeepEqual(got, want(0, workbenchReferencePageSize)) || next != workbenchReferencePageSize {
		t.Fatalf("first page = %v next=%d", got, next)
	}
	items, next = page(next)
	if got := ids(items); !reflect.DeepEqual(got, want(workbenchReferencePageSize, total)) || next != 0 {
		t.Fatalf("last page = %v next=%d", got, next)
	}
}
