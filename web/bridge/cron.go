package bridge

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field schedule in UTC: minute, hour, day of month,
// month, day of week.
type Cron struct {
	min, hour, dom, month, dow uint64
	// domAny and dowAny record a field that was `*`, for the POSIX rule:
	// when both day fields are restricted a day matches either.
	domAny, dowAny bool
}

var cronAliases = map[string]string{
	"@hourly": "0 * * * *",
	"@daily":  "0 0 * * *",
	"@weekly": "0 0 * * 0",
}

// ParseCron parses a five-field cron expression. Each field supports `*`,
// lists, ranges and `/step`; `@hourly`, `@daily` and `@weekly` are aliases.
// Day of week is 0-6 with 0 as Sunday (7 also means Sunday).
func ParseCron(expr string) (Cron, error) {
	expr = strings.TrimSpace(expr)
	if a, ok := cronAliases[expr]; ok {
		expr = a
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Cron{}, fmt.Errorf("cron: want 5 fields, got %d", len(fields))
	}
	var c Cron
	var err error
	if c.min, _, err = parseCronField(fields[0], 0, 59, "minute"); err != nil {
		return Cron{}, err
	}
	if c.hour, _, err = parseCronField(fields[1], 0, 23, "hour"); err != nil {
		return Cron{}, err
	}
	if c.dom, c.domAny, err = parseCronField(fields[2], 1, 31, "day of month"); err != nil {
		return Cron{}, err
	}
	if c.month, _, err = parseCronField(fields[3], 1, 12, "month"); err != nil {
		return Cron{}, err
	}
	if c.dow, c.dowAny, err = parseCronField(fields[4], 0, 7, "day of week"); err != nil {
		return Cron{}, err
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday
		c.dow = c.dow&^(1<<7) | 1
	}
	return c, nil
}

// parseCronField returns the bitset of allowed values and whether the
// field was an unrestricted `*`.
func parseCronField(s string, lo, hi int, name string) (uint64, bool, error) {
	var set uint64
	star := false
	for _, part := range strings.Split(s, ",") {
		rng, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return 0, false, fmt.Errorf("cron: bad step %q in %s", stepText, name)
			}
			step = n
		}
		a, b := lo, hi
		switch {
		case rng == "*":
			if !hasStep {
				star = true
			}
		case strings.Contains(rng, "-"):
			from, to, _ := strings.Cut(rng, "-")
			var err1, err2 error
			a, err1 = strconv.Atoi(from)
			b, err2 = strconv.Atoi(to)
			if err1 != nil || err2 != nil {
				return 0, false, fmt.Errorf("cron: bad range %q in %s", rng, name)
			}
		default:
			n, err := strconv.Atoi(rng)
			if err != nil {
				return 0, false, fmt.Errorf("cron: bad value %q in %s", rng, name)
			}
			a, b = n, n
			if hasStep {
				b = hi // "5/15" means 5 through the end, every 15
			}
		}
		if a < lo || b > hi || a > b {
			return 0, false, fmt.Errorf("cron: %s must be within %d-%d", name, lo, hi)
		}
		for v := a; v <= b; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, star, nil
}

func (c Cron) dayMatches(t time.Time) bool {
	domOK := c.dom&(1<<uint(t.Day())) != 0
	dowOK := c.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dowOK
	case c.dowAny:
		return domOK
	default:
		return domOK || dowOK
	}
}

// Next returns the first matching minute strictly after `after`, in UTC.
// It returns the zero time when nothing matches within five years (an
// expression like February 30th).
func (c Cron) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if c.month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !c.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if c.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if c.min&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}
