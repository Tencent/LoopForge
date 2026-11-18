package repo

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestScheduleExecutionSearchUsesDecodedText(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	cases := []struct {
		text, query string
	}{
		{`Review "delivery"`, `"delivery"`},
		{"R&D complete", "r&d"},
		{"<ready>", "<ready>"},
		{`C:\work\output`, `c:\work`},
		{"ÉCOLE deployed", "école"},
		{"first\nsecond", "first\nsecond"},
	}
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i, tc := range cases {
		trace := fmt.Sprintf("decoded-%d", i)
		if err := st.PutScheduleJobExecution(domain.ScheduleJobExecution{
			JobID: "search-job", TraceID: trace, Status: "success",
			AssistantMessage: tc.text, StartedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			items, total := st.ScheduleJobExecutionPage(ScheduleJobExecutionPageQuery{JobIDs: []string{"search-job"}, Search: tc.query})
			if total != 1 || len(items) != 1 || items[0].TraceID != fmt.Sprintf("decoded-%d", i) {
				t.Fatalf("search %q: total=%d items=%+v", tc.query, total, items)
			}
		})
	}
	// Filtering and pagination must operate on the matching decoded records.
	for i := range 3 {
		if err := st.PutScheduleJobExecution(domain.ScheduleJobExecution{
			JobID: "page-job", TraceID: fmt.Sprintf("page-%d", i), Status: "failed",
			Error: "<failed>", StartedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, total := st.ScheduleJobExecutionPage(ScheduleJobExecutionPageQuery{
		JobIDs: []string{"page-job"}, Search: "<failed>", Status: "failed",
		Since: base.Add(time.Minute), Offset: 1, Limit: 1,
	})
	if total != 2 || len(items) != 1 || items[0].TraceID != "page-1" {
		t.Fatalf("filtered page: total=%d items=%+v", total, items)
	}
}
