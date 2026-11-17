package httpapi

import (
	"net/url"
	"testing"
	"time"
)

func TestBusinessReportFilterDefaults(t *testing.T) {
	filter, err := businessReportFilter(url.Values{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filter.TimeZone != "UTC" || filter.Grain != "day" {
		t.Fatalf("defaults = %q/%q, want UTC/day", filter.TimeZone, filter.Grain)
	}
	if filter.ProjectIDs == nil || len(filter.ProjectIDs) != 0 {
		t.Fatalf("ProjectIDs = %#v, want empty non-nil slice", filter.ProjectIDs)
	}
	if !filter.From.IsZero() || !filter.To.IsZero() {
		t.Fatalf("from/to should stay zero, got %v/%v", filter.From, filter.To)
	}
}

func TestBusinessReportFilterParsesValues(t *testing.T) {
	filter, err := businessReportFilter(url.Values{
		"timeZone": {" Asia/Shanghai "},
		"grain":    {" week "},
		"from":     {"2026-01-01T00:00:00Z"},
		"to":       {"2026-01-31T00:00:00+08:00"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filter.TimeZone != "Asia/Shanghai" || filter.Grain != "week" {
		t.Fatalf("got %q/%q, want Asia/Shanghai/week", filter.TimeZone, filter.Grain)
	}
	if want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); !filter.From.Equal(want) {
		t.Fatalf("from = %v, want %v", filter.From, want)
	}
	if want := time.Date(2026, 1, 30, 16, 0, 0, 0, time.UTC); !filter.To.Equal(want) {
		t.Fatalf("to = %v, want %v", filter.To, want)
	}
}

func TestBusinessReportFilterSameFromTo(t *testing.T) {
	at := "2026-03-01T00:00:00Z"
	if _, err := businessReportFilter(url.Values{"from": {at}, "to": {at}, "grain": {"month"}}); err != nil {
		t.Fatalf("equal from/to should be accepted: %v", err)
	}
}

func TestBusinessReportFilterRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name  string
		query url.Values
		want  string
	}{
		{"unknown time zone", url.Values{"timeZone": {"invalid"}}, "timeZone 须为有效的 IANA 时区"},
		{"local time zone", url.Values{"timeZone": {"Local"}}, "timeZone 须为有效的 IANA 时区"},
		{"time zone checked before grain", url.Values{"timeZone": {"invalid"}, "grain": {"year"}}, "timeZone 须为有效的 IANA 时区"},
		{"unknown grain", url.Values{"grain": {"year"}}, "grain 必须为 day、week 或 month"},
		{"bad from", url.Values{"from": {"2026-01-01"}}, "时间须使用 RFC3339 格式"},
		{"bad to", url.Values{"to": {"yesterday"}}, "时间须使用 RFC3339 格式"},
		{"from after to", url.Values{"from": {"2026-02-01T00:00:00Z"}, "to": {"2026-01-01T00:00:00Z"}}, "开始时间不能晚于结束时间"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := businessReportFilter(tc.query)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
