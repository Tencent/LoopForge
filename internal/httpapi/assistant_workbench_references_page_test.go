package httpapi

import (
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
