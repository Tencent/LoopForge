package httpapi

import (
	"agentflow/internal/cronexpr"
	"time"
)

var assistantCronLocation = cronexpr.Location

func parseAssistantCron(expr string) (cronexpr.Schedule, error) {
	return cronexpr.Parse(expr)
}

func nextAssistantCron(expr string, after time.Time) (time.Time, error) {
	return cronexpr.Next(expr, after)
}
