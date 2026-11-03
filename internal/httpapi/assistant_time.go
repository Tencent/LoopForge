package httpapi

import (
	"regexp"
	"time"
)

// assistantClock is the user-facing time zone. The platform stores and returns
// UTC, but people read replies in Beijing time; a model quoting a raw UTC
// timestamp tells them a run started at 06:16 when it was 14:16.
var assistantClock = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("Asia/Shanghai", 8*3600)
}()

var assistantNow = time.Now

var utcTimestampPattern = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z\b`)

// beijingTimestamps rewrites RFC 3339 UTC timestamps in a platform tool result
// to the same instant at +08:00. Fractional seconds are kept, so a value the
// model passes back (for example an expectedUpdatedAt guard) still compares
// equal on the server.
func beijingTimestamps(text string) string {
	return utcTimestampPattern.ReplaceAllStringFunc(text, func(raw string) string {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return raw
		}
		return t.In(assistantClock).Format(time.RFC3339Nano)
	})
}

// assistantPlatformResultForModel prepares a platform tool result for the
// model. Artifact bodies are documents the model may quote or edit verbatim,
// so their text is left untouched.
func assistantPlatformResultForModel(tool, text string) string {
	if tool == "runs_artifact_get" {
		return text
	}
	return beijingTimestamps(text)
}

var assistantWeekdays = [...]string{"日", "一", "二", "三", "四", "五", "六"}

// assistantClockNote tells the model the current Beijing time and how to state
// times, so "today", "just now" and run timings match the user's clock.
func assistantClockNote() string {
	now := assistantNow().In(assistantClock)
	return "\n当前北京时间：" + now.Format("2006-01-02 15:04") + "（星期" + assistantWeekdays[now.Weekday()] + "）。回复里的时间一律用北京时间（UTC+8）表述，不要写 UTC；平台工具结果里的时间戳已换算为 +08:00，直接照读即可。"
}
