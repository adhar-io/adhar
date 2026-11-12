package batch

import (
	"testing"
	"time"
)

func TestNextCronStepsStandardExpressions(t *testing.T) {
	sg, _ := time.LoadLocation("Asia/Singapore")
	after := time.Date(2026, 11, 3, 23, 30, 0, 0, time.UTC) // 07:30 on the 4th in Singapore
	cases := []struct {
		expr string
		loc  *time.Location
		want string
	}{
		{"30 1 * * *", sg, "2026-11-05T01:30:00+08:00"},
		{"*/15 * * * *", time.UTC, "2026-11-03T23:45:00Z"},
		{"0 9 * * mon-fri", time.UTC, "2026-11-04T09:00:00Z"},
		{"0 0 1 1 *", time.UTC, "2027-01-01T00:00:00Z"},
		{"0 6 15 * *", time.UTC, "2026-11-15T06:00:00Z"},
		{"@daily", time.UTC, "2026-11-04T00:00:00Z"},
		{"0 8 * * sun", time.UTC, "2026-11-08T08:00:00Z"},
		{"5,35 8-10 * * *", time.UTC, "2026-11-04T08:05:00Z"},
		// Both day fields restricted: standard cron fires when EITHER matches.
		{"0 0 1 * fri", time.UTC, "2026-11-06T00:00:00Z"},
	}
	for _, c := range cases {
		got, ok := nextCron(c.expr, after, c.loc)
		if !ok || got.Format(time.RFC3339) != c.want {
			t.Errorf("%q: next = %v %v, want %s", c.expr, got.Format(time.RFC3339), ok, c.want)
		}
	}
	for _, bad := range []string{"", "* * * *", "61 * * * *", "0 0 32 * *", "a b c d e", "*/0 * * * *"} {
		if _, ok := nextCron(bad, after, time.UTC); ok {
			t.Errorf("%q must not step", bad)
		}
	}
}
