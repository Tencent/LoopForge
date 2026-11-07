package repo

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestBusinessCompletionToMerge(t *testing.T) {
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	run := func(id string, start int) domain.Run {
		return domain.Run{ID: id, CreatedAt: at(start), Status: "succeeded"}
	}
	key := func(i int) domain.MRDeliveryKey { return domain.MRDeliveryKey{Project: "team/repo", IID: i} }
	mr := func(i int) domain.DeliveryMR { return domain.DeliveryMR{Project: "team/repo", IID: i} }
	event := func(h int) []domain.RunEvent { return []domain.RunEvent{{Type: "run.succeeded", Timestamp: at(h)}} }
	for _, tc := range []struct {
		name            string
		runs            []domain.Run
		refs            map[string][]domain.DeliveryMR
		events          map[string][]domain.RunEvent
		filter          domain.AnalyticsFilter
		stateIDs        []int
		want            []int64
		missing, before int
	}{
		{name: "latest completion", runs: []domain.Run{run("a", 0), run("b", 1)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1)}, "b": {mr(1)}}, events: map[string][]domain.RunEvent{"a": event(2), "b": event(4)}, want: []int64{6}},
		{name: "missing success cannot fall back to older round", runs: []domain.Run{run("a", 0), run("b", 1)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1)}, "b": {mr(1)}}, events: map[string][]domain.RunEvent{"a": event(2)}, missing: 1},
		{name: "completion order wins over creation order", runs: []domain.Run{run("a", 0), run("b", 1)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1)}, "b": {mr(1)}}, events: map[string][]domain.RunEvent{"a": event(5), "b": event(4)}, want: []int64{5}},
		{name: "later unrelated rerun cannot rewrite cohort", runs: []domain.Run{run("a", 0), run("later", 11)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1)}, "later": {mr(1)}}, events: map[string][]domain.RunEvent{"a": event(4), "later": event(12)}, want: []int64{6}},
		{name: "same Run later retry preserves historical completion", runs: []domain.Run{run("a", 0)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1)}}, events: map[string][]domain.RunEvent{"a": append(event(4), event(12)...)}, want: []int64{6}},
		{name: "two MRs have two independent samples", runs: []domain.Run{run("a", 0)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1), mr(2)}}, events: map[string][]domain.RunEvent{"a": event(4)}, stateIDs: []int{1, 2}, want: []int64{6, 8}},
		{name: "another MR still open does not block merged MR", runs: []domain.Run{run("a", 0)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1), mr(3)}}, events: map[string][]domain.RunEvent{"a": event(4)}, want: []int64{6}},
		{name: "each MR uses its own merge window", runs: []domain.Run{run("a", 0)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1), mr(2)}}, events: map[string][]domain.RunEvent{"a": event(4)}, stateIDs: []int{1, 2}, filter: domain.AnalyticsFilter{From: at(10), To: at(11)}, want: []int64{6}},
		{name: "missing association does not discard the other MR", runs: []domain.Run{run("a", 0)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1)}}, events: map[string][]domain.RunEvent{"a": event(4)}, stateIDs: []int{1, 2}, want: []int64{6}, missing: 1},
		{name: "before completion classification is per MR", runs: []domain.Run{run("a", 0)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1), mr(2)}}, events: map[string][]domain.RunEvent{"a": event(11)}, stateIDs: []int{1, 2}, want: []int64{1}, before: 1},
		{name: "observed zero is valid", runs: []domain.Run{run("a", 0)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1)}}, events: map[string][]domain.RunEvent{"a": event(10)}, want: []int64{0}},
		{name: "missing completion is unknown", runs: []domain.Run{run("a", 0)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1)}}, missing: 1},
		{name: "missing artifact association is unknown", runs: []domain.Run{run("a", 0)}, events: map[string][]domain.RunEvent{"a": event(4)}, missing: 1},
		{name: "child SOP cannot complete a requirement", runs: []domain.Run{{ID: "child", CreatedAt: base, ExecutionContext: domain.TelemetryContext{ParentRunID: "parent"}}}, refs: map[string][]domain.DeliveryMR{"child": {mr(1)}}, events: map[string][]domain.RunEvent{"child": event(4)}, missing: 1},
		{name: "outside own merge window has no sample", runs: []domain.Run{run("a", 0)}, refs: map[string][]domain.DeliveryMR{"a": {mr(1)}}, events: map[string][]domain.RunEvent{"a": event(4)}, filter: domain.AnalyticsFilter{From: at(11), To: at(12)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := tc.stateIDs
			if ids == nil {
				ids = []int{1}
			}
			states := map[domain.MRDeliveryKey]domain.MRDeliveryState{}
			for _, id := range ids {
				states[key(id)] = domain.MRDeliveryState{IssueID: "issue", State: "merged", MergedAt: at(8 + 2*id)}
			}
			got := businessCompletionToMerge(businessIssueTiming{issue: domain.Issue{ID: "issue"}, runs: tc.runs}, tc.refs, states, tc.events, tc.filter)
			var durations []int64
			missing, before := 0, 0
			for _, sample := range got {
				switch {
				case sample.missing:
					missing++
				case sample.beforeCompletion:
					before++
				default:
					durations = append(durations, sample.duration/int64(time.Hour/time.Millisecond))
				}
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			if !reflect.DeepEqual(durations, tc.want) || missing != tc.missing || before != tc.before {
				t.Fatalf("got %+v / missing %d / before %d; want %+v / %d / %d", durations, missing, before, tc.want, tc.missing, tc.before)
			}
		})
	}
}
