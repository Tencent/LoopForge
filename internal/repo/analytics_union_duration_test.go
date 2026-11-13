package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestUnionDuration(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	span := func(from, to int) domain.RunExecutionInterval {
		return domain.RunExecutionInterval{
			StartedAt:  base.Add(time.Duration(from) * time.Minute),
			FinishedAt: base.Add(time.Duration(to) * time.Minute),
		}
	}
	cases := []struct {
		name      string
		intervals []domain.RunExecutionInterval
		want      time.Duration
	}{
		{"empty", nil, 0},
		{"single", []domain.RunExecutionInterval{span(0, 10)}, 10 * time.Minute},
		{"disjoint", []domain.RunExecutionInterval{span(0, 10), span(20, 25)}, 15 * time.Minute},
		{"overlapping", []domain.RunExecutionInterval{span(0, 10), span(5, 15)}, 15 * time.Minute},
		{"contained", []domain.RunExecutionInterval{span(0, 30), span(5, 10)}, 30 * time.Minute},
		{"touching", []domain.RunExecutionInterval{span(0, 10), span(10, 20)}, 20 * time.Minute},
		{"unsorted", []domain.RunExecutionInterval{span(40, 50), span(0, 10), span(8, 12)}, 22 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unionDuration(tc.intervals); got != tc.want {
				t.Fatalf("unionDuration() = %v, want %v", got, tc.want)
			}
		})
	}
}
