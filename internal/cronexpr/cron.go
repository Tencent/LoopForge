package cronexpr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var Location = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Local
	}
	return loc
}()

type Schedule struct {
	min, hour, day, month, dow uint64
	// Standard cron: when both day fields are restricted a time matches if
	// either does; a field starting with * defers to the other one.
	dayStar, dowStar bool
}

// fieldSpecs lists the five cron fields in expression order with their
// display name (used to prefix parse errors) and inclusive value range.
var fieldSpecs = [5]struct {
	name     string
	min, max int
}{
	{"分钟", 0, 59},
	{"小时", 0, 23},
	{"日", 1, 31},
	{"月", 1, 12},
	{"周", 0, 7},
}

func Parse(expr string) (Schedule, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != len(fieldSpecs) {
		return Schedule{}, fmt.Errorf("cron 需为 5 段（分 时 日 月 周），例如 0 * * * *")
	}
	var bits [len(fieldSpecs)]uint64
	for i, spec := range fieldSpecs {
		b, err := parseField(fields[i], spec.min, spec.max)
		if err != nil {
			return Schedule{}, fmt.Errorf("%s: %w", spec.name, err)
		}
		bits[i] = b
	}
	dow := bits[4]
	if dow&(1<<7) != 0 {
		dow |= 1 << 0
	}
	return Schedule{
		min: bits[0], hour: bits[1], day: bits[2], month: bits[3], dow: dow,
		dayStar: strings.HasPrefix(fields[2], "*"),
		dowStar: strings.HasPrefix(fields[4], "*"),
	}, nil
}

func parseField(raw string, min, max int) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("为空")
	}
	var bits uint64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, fmt.Errorf("无效分段")
		}
		step := 1
		rangePart := part
		if i := strings.IndexByte(part, '/'); i >= 0 {
			rangePart = part[:i]
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("步长无效")
			}
			step = n
		}
		start, end := min, max
		if rangePart != "*" {
			bounds := strings.Split(rangePart, "-")
			if len(bounds) == 1 {
				n, err := strconv.Atoi(bounds[0])
				if err != nil {
					return 0, fmt.Errorf("不是数字")
				}
				start, end = n, n
			} else if len(bounds) == 2 {
				a, err1 := strconv.Atoi(bounds[0])
				b, err2 := strconv.Atoi(bounds[1])
				if err1 != nil || err2 != nil {
					return 0, fmt.Errorf("范围无效")
				}
				start, end = a, b
			} else {
				return 0, fmt.Errorf("范围无效")
			}
		}
		if start < min || end > max || start > end {
			return 0, fmt.Errorf("超出 %d-%d", min, max)
		}
		for n := start; n <= end; n += step {
			bits |= 1 << uint(n)
		}
	}
	if bits == 0 {
		return 0, fmt.Errorf("没有有效取值")
	}
	return bits, nil
}

func (c Schedule) Matches(t time.Time) bool {
	t = t.In(Location)
	if c.min&(1<<uint(t.Minute())) == 0 {
		return false
	}
	if c.hour&(1<<uint(t.Hour())) == 0 {
		return false
	}
	if c.month&(1<<uint(t.Month())) == 0 {
		return false
	}
	dayOK := c.day&(1<<uint(t.Day())) != 0
	dowOK := c.dow&(1<<uint(t.Weekday())) != 0
	if c.dayStar || c.dowStar {
		return dayOK && dowOK
	}
	return dayOK || dowOK
}

func Next(expr string, after time.Time) (time.Time, error) {
	sched, err := Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	cursor := after.In(Location).Truncate(time.Minute).Add(time.Minute)
	limit := cursor.Add(366 * 24 * time.Hour)
	for t := cursor; t.Before(limit); t = t.Add(time.Minute) {
		if sched.Matches(t) {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cron 在一年内没有下次执行时间")
}
