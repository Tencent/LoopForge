package preview

import (
	"errors"
	"strings"
	"testing"
)

func TestTarBudgetAdmitUnlimited(t *testing.T) {
	b := tarBudget{}
	for i := 0; i < 5; i++ {
		if err := b.admit(1 << 30); err != nil {
			t.Fatalf("admit %d with no limits: %v", i, err)
		}
	}
	if b.files != 5 || b.expanded != 5<<30 {
		t.Fatalf("budget = %d files / %d bytes, want 5 / %d", b.files, b.expanded, int64(5<<30))
	}
}

func TestTarBudgetAdmitAtLimits(t *testing.T) {
	b := tarBudget{limits: Limits{MaxFiles: 2, MaxExpanded: 10}}
	if err := b.admit(4); err != nil {
		t.Fatalf("first admit: %v", err)
	}
	if err := b.admit(6); err != nil {
		t.Fatalf("admit exactly at limits: %v", err)
	}
	if b.files != 2 || b.expanded != 10 {
		t.Fatalf("budget = %d files / %d bytes, want 2 / 10", b.files, b.expanded)
	}
}

func TestTarBudgetAdmitFileCountExceeded(t *testing.T) {
	b := tarBudget{limits: Limits{MaxFiles: 1, MaxExpanded: 1}}
	if err := b.admit(0); err != nil {
		t.Fatalf("first admit: %v", err)
	}
	// Both limits would be exceeded; the file count is reported first.
	err := b.admit(5)
	if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "more than 1 files") {
		t.Fatalf("err = %v, want file count limit", err)
	}
}

func TestTarBudgetAdmitExpandedExceeded(t *testing.T) {
	b := tarBudget{limits: Limits{MaxFiles: 10, MaxExpanded: 10}}
	if err := b.admit(8); err != nil {
		t.Fatalf("first admit: %v", err)
	}
	err := b.admit(3)
	if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "expanded size") {
		t.Fatalf("err = %v, want expanded size limit", err)
	}
}
