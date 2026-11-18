package domain

import (
	"reflect"
	"testing"
)

func TestDeliveryExecutionPercentilesUseNearestRank(t *testing.T) {
	items := []AnalyticsDelivery{{ExecutionDurationMs: 3000}, {ExecutionDurationMs: 1000}, {ExecutionDurationMs: 2000}}
	got := SummarizeDeliveries(items)
	if got.P50ExecutionDurationMs != 2000 || got.P95ExecutionDurationMs != 3000 {
		t.Fatalf("small-sample percentiles: P50=%d P95=%d", got.P50ExecutionDurationMs, got.P95ExecutionDurationMs)
	}
	for _, tc := range []struct {
		values     []int64
		percentile int
		want       int64
	}{
		{[]int64{10}, 95, 10},
		{[]int64{10, 1000}, 95, 1000},
		{[]int64{10, 20, 30, 40}, 75, 30},
		{[]int64{40, 30, 20, 10}, 50, 20},
	} {
		before := append([]int64(nil), tc.values...)
		if got := analyticsPercentile(tc.values, tc.percentile); got != tc.want {
			t.Errorf("P%d(%v)=%d, want %d", tc.percentile, tc.values, got, tc.want)
		}
		if !reflect.DeepEqual(tc.values, before) {
			t.Error("percentile mutated the caller's values")
		}
	}
}
