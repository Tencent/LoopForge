package cronexpr

import (
	"testing"
	"time"
)

func TestParseRejectsInvalidExpressions(t *testing.T) {
	for _, expr := range []string{
		"",
		"* * * *",
		"* * * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * * 13 *",
		"* * * * 8",
		"*/0 * * * *",
		"*/x * * * *",
		"5-1 * * * *",
		"1-2-3 * * * *",
		"a * * * *",
		"1,,2 * * * *",
	} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) should fail", expr)
		}
	}
}

func shanghai(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, Location)
}

func TestMatchesListsRangesAndSteps(t *testing.T) {
	sched, err := Parse("0,30 9-17/4 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		at   time.Time
		want bool
	}{
		{shanghai(2026, 9, 28, 9, 0), true},   // Monday 09:00
		{shanghai(2026, 9, 28, 13, 30), true}, // Monday 13:30
		{shanghai(2026, 9, 28, 17, 0), true},  // Monday 17:00
		{shanghai(2026, 9, 28, 11, 0), false}, // hour not on the /4 step
		{shanghai(2026, 9, 28, 9, 15), false}, // minute not listed
		{shanghai(2026, 9, 27, 9, 0), false},  // Sunday
	}
	for _, tc := range cases {
		if got := sched.Matches(tc.at); got != tc.want {
			t.Errorf("Matches(%s) = %v, want %v", tc.at, got, tc.want)
		}
	}
}

func TestDayOfWeekSevenIsSunday(t *testing.T) {
	sched, err := Parse("0 8 * * 7")
	if err != nil {
		t.Fatal(err)
	}
	if !sched.Matches(shanghai(2026, 9, 27, 8, 0)) {
		t.Fatal("7 should match Sunday")
	}
}

func TestDayOfMonthAndWeekMatchEitherWhenBothRestricted(t *testing.T) {
	cases := []struct {
		expr string
		at   time.Time
		want bool
	}{
		// Oct 1 2026 is a Thursday, Oct 5 a Monday, Oct 6 a Tuesday.
		{"0 9 1 * 1", shanghai(2026, 10, 1, 9, 0), true},
		{"0 9 1 * 1", shanghai(2026, 10, 5, 9, 0), true},
		{"0 9 1 * 1", shanghai(2026, 10, 6, 9, 0), false},
		// A * in either field leaves only the other one in charge.
		{"0 9 1 * *", shanghai(2026, 10, 5, 9, 0), false},
		{"0 9 * * 1", shanghai(2026, 10, 1, 9, 0), false},
		{"0 9 */2 * 1", shanghai(2026, 10, 3, 9, 0), false},
		{"0 9 */2 * 1", shanghai(2026, 10, 5, 9, 0), true},
		{"0 9 1 * */3", shanghai(2026, 10, 1, 9, 0), false},
	}
	for _, tc := range cases {
		sched, err := Parse(tc.expr)
		if err != nil {
			t.Fatal(err)
		}
		if got := sched.Matches(tc.at); got != tc.want {
			t.Errorf("%q Matches(%s) = %v, want %v", tc.expr, tc.at.Format("2006-01-02 Mon"), got, tc.want)
		}
	}
	next, err := Next("0 9 1 * 1", shanghai(2026, 10, 1, 9, 0))
	if err != nil || !next.Equal(shanghai(2026, 10, 5, 9, 0)) {
		t.Fatalf("Next after the 1st should be the following Monday, got %s (%v)", next, err)
	}
}

func TestMatchesUsesShanghaiTime(t *testing.T) {
	sched, err := Parse("0 9 * * *")
	if err != nil {
		t.Fatal(err)
	}
	if !sched.Matches(time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC)) {
		t.Fatal("01:00 UTC is 09:00 in Asia/Shanghai")
	}
}

func TestNextIsStrictlyAfterAndUTC(t *testing.T) {
	after := shanghai(2026, 9, 30, 9, 0).Add(20 * time.Second)
	next, err := Next("0 9 * * *", after)
	if err != nil {
		t.Fatal(err)
	}
	want := shanghai(2026, 10, 1, 9, 0).UTC()
	if !next.Equal(want) || next.Location() != time.UTC {
		t.Fatalf("Next = %s, want %s in UTC", next, want)
	}
	next, _ = Next("*/15 * * * *", shanghai(2026, 9, 30, 9, 14))
	if !next.Equal(shanghai(2026, 9, 30, 9, 15)) {
		t.Fatalf("Next */15 = %s", next)
	}
}

func TestNextReportsImpossibleSchedules(t *testing.T) {
	if _, err := Next("0 0 30 2 *", time.Now()); err == nil {
		t.Fatal("Feb 30 never occurs and should report an error")
	}
	if _, err := Next("bad", time.Now()); err == nil {
		t.Fatal("parse errors should propagate")
	}
}
