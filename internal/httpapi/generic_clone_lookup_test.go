package httpapi

import "testing"

type genericCloneStub struct {
	ID        string
	ProjectID string
}

func TestFindGenericProjectClone(t *testing.T) {
	items := map[string]genericCloneStub{
		"plain":      {ID: "plain", ProjectID: "p1"},
		"shared":     {ID: "shared", ProjectID: "other"},
		"shared-p1":  {ID: "shared-p1", ProjectID: "p1"},
		"foreign":    {ID: "foreign", ProjectID: "other"},
		"foreign-p1": {ID: "foreign-p1", ProjectID: "other"},
	}
	var looked []string
	lookup := func(id string) (genericCloneStub, bool) {
		looked = append(looked, id)
		item, ok := items[id]
		return item, ok
	}
	owner := func(v genericCloneStub) string { return v.ProjectID }

	cases := []struct {
		source string
		wantID string
		found  bool
		looked []string
	}{
		{source: "plain", wantID: "plain", found: true, looked: []string{"plain"}},
		{source: "shared", wantID: "shared-p1", found: true, looked: []string{"shared", "shared-p1"}},
		{source: "foreign", found: false, looked: []string{"foreign", "foreign-p1"}},
		{source: "missing", found: false, looked: []string{"missing", "missing-p1"}},
	}
	for _, tc := range cases {
		looked = nil
		got, found := findGenericProjectClone(tc.source, "p1", lookup, owner)
		if found != tc.found || got.ID != tc.wantID {
			t.Fatalf("%s: got %#v found=%v, want id=%q found=%v", tc.source, got, found, tc.wantID, tc.found)
		}
		if len(looked) != len(tc.looked) {
			t.Fatalf("%s: looked up %v, want %v", tc.source, looked, tc.looked)
		}
		for i := range looked {
			if looked[i] != tc.looked[i] {
				t.Fatalf("%s: looked up %v, want %v", tc.source, looked, tc.looked)
			}
		}
	}
}
