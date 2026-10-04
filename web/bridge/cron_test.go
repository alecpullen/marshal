package bridge

import (
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCronNext(t *testing.T) {
	cases := []struct{ expr, after, want string }{
		{"* * * * *", "2026-01-01 00:00", "2026-01-01 00:01"},
		{"*/15 * * * *", "2026-01-01 00:14", "2026-01-01 00:15"},
		{"*/15 * * * *", "2026-01-01 00:45", "2026-01-01 01:00"},
		{"5 4 * * *", "2026-01-01 04:05", "2026-01-02 04:05"},
		{"0 9-17/4 * * *", "2026-01-01 10:00", "2026-01-01 13:00"},
		{"0,30 8 * * *", "2026-01-01 08:00", "2026-01-01 08:30"},
		{"0 0 1 * *", "2026-01-15 12:00", "2026-02-01 00:00"},
		{"0 0 * 3 *", "2026-04-01 00:00", "2027-03-01 00:00"},
		{"30 23 31 12 *", "2026-12-31 23:30", "2027-12-31 23:30"},
		// 2026-01-04 is a Sunday.
		{"0 0 * * 0", "2026-01-01 00:00", "2026-01-04 00:00"},
		{"0 0 * * 7", "2026-01-01 00:00", "2026-01-04 00:00"},
		{"@hourly", "2026-01-01 00:10", "2026-01-01 01:00"},
		{"@daily", "2026-01-01 00:10", "2026-01-02 00:00"},
		{"@weekly", "2026-01-01 00:10", "2026-01-04 00:00"},
		// Both day fields restricted: the 15th OR a Monday (POSIX).
		{"0 0 15 * 1", "2026-01-01 00:00", "2026-01-05 00:00"},
		{"0 0 15 * 1", "2026-01-12 00:00", "2026-01-15 00:00"},
		// Leap day rollover.
		{"0 0 29 2 *", "2026-01-01 00:00", "2028-02-29 00:00"},
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Errorf("%q: %v", c.expr, err)
			continue
		}
		if got := cr.Next(utc(c.after)); !got.Equal(utc(c.want)) {
			t.Errorf("%q after %s = %s, want %s", c.expr, c.after, got.Format("2006-01-02 15:04"), c.want)
		}
	}
}

func TestCronNextIsStrictlyAfter(t *testing.T) {
	cr, _ := ParseCron("0 * * * *")
	at := utc("2026-01-01 05:00")
	if got := cr.Next(at); !got.Equal(utc("2026-01-01 06:00")) {
		t.Fatalf("Next(05:00) = %v", got)
	}
}

func TestCronNextImpossible(t *testing.T) {
	cr, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := cr.Next(utc("2026-01-01 00:00")); !got.IsZero() {
		t.Fatalf("Feb 30 matched %v", got)
	}
}

func TestParseCronInvalid(t *testing.T) {
	for _, expr := range []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *",
		"* * 32 * *", "* * * 13 *", "* * * * 8", "a * * * *", "*/0 * * * *",
		"5-1 * * * *", "1- * * * *", "*/x * * * *", "@yearly",
	} {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("%q parsed, want an error", expr)
		}
	}
}
