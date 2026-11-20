package domain

import "testing"

func TestRecordDeliveryMRsDedupsAndKeepsBestState(t *testing.T) {
	t.Parallel()
	mrs := map[string]string{}
	opened := DeliveryMR{Project: "team/frontend", IID: 1, URL: GongfengMRURL("team/frontend", 1), State: "opened"}
	if recordDeliveryMRs(mrs, []DeliveryMR{opened}) {
		t.Fatal("opened MR must not report merged")
	}
	// Same MR through another URL form, now merged: one entry, upgraded state.
	alias := opened
	alias.URL = "http://git.example.com/team/frontend/merge_requests/1"
	alias.State = "merged"
	if !recordDeliveryMRs(mrs, []DeliveryMR{alias}) {
		t.Fatal("merged MR must report merged")
	}
	if len(mrs) != 1 || mrs["team/frontend#1"] != "merged" {
		t.Fatalf("mrs=%v, want single merged entry", mrs)
	}
	// A lower-ranked state never downgrades the recorded one.
	integrated := opened
	integrated.State = "integrated"
	if recordDeliveryMRs(mrs, []DeliveryMR{integrated}) {
		t.Fatal("integrated MR must not report merged")
	}
	if mrs["team/frontend#1"] != "merged" {
		t.Fatalf("state downgraded: %v", mrs)
	}
}

func TestRecordDeliveryMRsSkipsMRsWithoutIdentity(t *testing.T) {
	t.Parallel()
	mrs := map[string]string{}
	if recordDeliveryMRs(mrs, []DeliveryMR{{State: "merged"}}) {
		t.Fatal("MR without identity must not count as merged")
	}
	if len(mrs) != 0 {
		t.Fatalf("mrs=%v, want empty", mrs)
	}
	if recordDeliveryMRs(mrs, nil) {
		t.Fatal("empty list must not report merged")
	}
}

func TestCountDeliveryMRStates(t *testing.T) {
	t.Parallel()
	merged, integrated := countDeliveryMRStates(map[string]string{
		"a#1": "merged",
		"b#1": "merged",
		"c#1": "integrated",
		"d#1": "opened",
		"e#1": "",
	})
	if merged != 2 || integrated != 1 {
		t.Fatalf("merged=%d integrated=%d, want 2 and 1", merged, integrated)
	}
	if merged, integrated := countDeliveryMRStates(nil); merged != 0 || integrated != 0 {
		t.Fatalf("nil table: merged=%d integrated=%d", merged, integrated)
	}
}
