package tracker

import (
	"reflect"
	"testing"
)

func visitedIIDs(raw string, stopAt int) [][]int {
	var out [][]int
	visitGongfengMRPayload(raw, func(items []gongfengListedMR) bool {
		iids := make([]int, 0, len(items))
		for _, item := range items {
			iids = append(iids, item.IID)
		}
		out = append(out, iids)
		return len(out) == stopAt
	})
	return out
}

func TestVisitGongfengMRPayloadOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want [][]int
	}{
		// A wrapped list also decodes as a bare object (iid 0) with empty items.
		{"wrapped list", `{"data":[{"iid":1},{"iid":2}]}`, [][]int{{1, 2}, {}, {0}}},
		{"wrapped object", `{"data":{"iid":3}}`, [][]int{{3}, {}, {0}}},
		{"items", `{"items":[{"iid":4}]}`, [][]int{{4}, {0}}},
		{"bare list", `[{"iid":5},{"iid":6}]`, [][]int{{5, 6}}},
		{"bare object", `{"iid":7}`, [][]int{{}, {7}}},
		{"invalid", `not json`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := visitedIIDs(tc.raw, -1); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("visited %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVisitGongfengMRPayloadStops(t *testing.T) {
	if got := visitedIIDs(`{"data":[{"iid":1}]}`, 1); !reflect.DeepEqual(got, [][]int{{1}}) {
		t.Fatalf("visited %v, want stop after first list", got)
	}
}

func TestGongfengMRPayloadCallers(t *testing.T) {
	for _, raw := range []string{
		`{"data":[{"id":90,"iid":9,"state":"opened","target_branch":"main"}]}`,
		`{"data":{"id":90,"iid":9,"state":"opened","target_branch":"main"}}`,
		`{"items":[{"id":90,"iid":9,"state":"opened","target_branch":"main"}]}`,
		`[{"id":90,"iid":9,"state":"opened","target_branch":"main"}]`,
		`{"id":90,"iid":9,"state":"opened","target_branch":"main"}`,
	} {
		if state, target, _ := parseGongfengMRDetails(raw, 9); state != "opened" || target != "main" {
			t.Fatalf("%s: details state=%q target=%q", raw, state, target)
		}
		if id, iid, ok := parseGongfengOpenMR(raw, "main"); !ok || id != 90 || iid != 9 {
			t.Fatalf("%s: open MR id=%d iid=%d ok=%v", raw, id, iid, ok)
		}
		if _, _, ok := parseGongfengOpenMR(raw, "release"); ok {
			t.Fatalf("%s: open MR should skip other target branch", raw)
		}
		if records := parseGongfengMRRecords(raw); len(records) != 1 || records[0].IID != 9 {
			t.Fatalf("%s: records=%+v", raw, records)
		}
	}
	if state, _, _ := parseGongfengMRDetails(`{"data":[{"iid":1,"state":"merged"}]}`, 2); state != "" {
		t.Fatalf("other iid should not match, got %q", state)
	}
	if _, _, ok := parseGongfengOpenMR(`{"data":[{"iid":1,"state":"merged"}]}`, ""); ok {
		t.Fatal("merged MR is not open")
	}
}
