package httpapi

import (
	"testing"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func newSyncAuthUserHandlers(t *testing.T) (*authHandlers, *repo.Store) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &authHandlers{deps: &deps{repo: st}}, st
}

func TestSyncAuthUserRecordMergesExistingRow(t *testing.T) {
	h, st := newSyncAuthUserHandlers(t)
	if err := st.PutUser(domain.User{LoginName: "alice", StaffID: 42, ChineseName: "爱丽丝", DeptID: 7, DeptName: "平台部"}); err != nil {
		t.Fatal(err)
	}
	stored, ok := st.UserByLoginName("alice")
	if !ok {
		t.Fatal("seeded user missing")
	}

	user := &auth.User{LoginName: "alice", ChineseName: "alice"}
	h.syncAuthUserRecord(user, "gateway")
	if user.ID != stored.ID || user.ChineseName != "爱丽丝" || user.DeptID != 7 || user.DeptName != "平台部" || user.StaffID != 42 {
		t.Fatalf("merged user = %+v, stored = %+v", user, stored)
	}

	// A StaffID already carried by the request wins over the stored one.
	withStaff := &auth.User{LoginName: "alice", StaffID: 99}
	h.syncAuthUserRecord(withStaff, "cookie")
	if withStaff.StaffID != 99 || withStaff.ID != stored.ID {
		t.Fatalf("request StaffID should be kept: %+v", withStaff)
	}
}

func TestSyncAuthUserRecordKeepsFieldsWhenStoredEmpty(t *testing.T) {
	h, st := newSyncAuthUserHandlers(t)
	if err := st.PutUser(domain.User{LoginName: "bob"}); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.UserByLoginName("bob")
	if stored.ChineseName != "" || stored.DeptName != "" || stored.DeptID != 0 || stored.StaffID != 0 {
		t.Fatalf("seeded profile should be empty: %+v", stored)
	}

	user := &auth.User{LoginName: "bob", ChineseName: "鲍勃", DeptID: 3, DeptName: "研发", StaffID: 5}
	h.syncAuthUserRecord(user, "cookie")
	if user.ID != stored.ID || user.ChineseName != "鲍勃" || user.DeptID != 3 || user.DeptName != "研发" || user.StaffID != 5 {
		t.Fatalf("empty stored fields must not overwrite request values: %+v", user)
	}
}

func TestSyncAuthUserRecordPersistsMissingUser(t *testing.T) {
	h, st := newSyncAuthUserHandlers(t)
	user := &auth.User{LoginName: "carol", StaffID: 11, ChineseName: "卡罗尔", DeptID: 2, DeptName: "运营"}
	h.syncAuthUserRecord(user, "gateway")

	saved, ok := st.UserByLoginName("carol")
	if !ok {
		t.Fatal("missing user should be persisted")
	}
	if user.ID == 0 || user.ID != saved.ID {
		t.Fatalf("persisted ID not read back: user=%d saved=%d", user.ID, saved.ID)
	}
	if saved.StaffID != 11 || saved.ChineseName != "卡罗尔" || saved.DeptID != 2 || saved.DeptName != "运营" {
		t.Fatalf("persisted row = %+v", saved)
	}
}

func TestSyncAuthUserRecordWithoutRepoIsNoop(t *testing.T) {
	h := &authHandlers{deps: &deps{}}
	user := &auth.User{LoginName: "dave", ChineseName: "dave"}
	h.syncAuthUserRecord(user, "cookie")
	if user.ID != 0 || user.ChineseName != "dave" {
		t.Fatalf("nil repo should leave user untouched: %+v", user)
	}
}
