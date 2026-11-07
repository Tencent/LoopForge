package httpapi

import (
	"testing"
	"time"
)

func TestParseAssistantCron(t *testing.T) {
	if _, err := parseAssistantCron("0 * * * *"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseAssistantCron("*/15 9-18 * * 1-5"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseAssistantCron("0 * * *"); err == nil {
		t.Fatal("need 5 fields")
	}
	if _, err := parseAssistantCron("60 * * * *"); err == nil {
		t.Fatal("minute out of range")
	}
}

func TestNextAssistantCronHourly(t *testing.T) {
	loc := assistantCronLocation
	after := time.Date(2026, 9, 14, 10, 20, 30, 0, loc)
	next, err := nextAssistantCron("0 * * * *", after)
	if err != nil {
		t.Fatal(err)
	}
	got := next.In(loc)
	if got.Hour() != 11 || got.Minute() != 0 {
		t.Fatalf("got %s", got)
	}
}

func TestAssistantCronDue(t *testing.T) {
	loc := assistantCronLocation
	task := assistantTask{Enabled: true, Cron: "0 * * * *", LastRunAt: time.Date(2026, 9, 14, 10, 0, 0, 0, loc).UTC()}
	if assistantTaskDue(task, time.Date(2026, 9, 14, 10, 30, 0, 0, loc).UTC()) {
		t.Fatal("should wait until the next hour")
	}
	if !assistantTaskDue(task, time.Date(2026, 9, 14, 11, 0, 0, 0, loc).UTC()) {
		t.Fatal("hourly cron should fire at minute 0")
	}
}
