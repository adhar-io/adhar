package batch

import (
	"strconv"
	"testing"
	"time"
)

func TestBatchScheduledBusinessDateIsTheScheduledTimeInTheJobsZone(t *testing.T) {
	sg, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-03T17:30:00Z is 01:30 on the 4th in Singapore: the zone decides the day.
	at := time.Date(2026, 11, 3, 17, 30, 0, 0, time.UTC)
	name := "importer-" + itoa(at.Unix()/60)
	ann := map[string]string{annotationScheduled: at.Format(time.RFC3339)}
	for _, tc := range []struct {
		what string
		name string
		ann  map[string]string
		zone *time.Location
		want string
		ok   bool
	}{
		{"both, Singapore", name, ann, sg, "2026-11-04", true},
		{"both, UTC", name, ann, time.UTC, "2026-11-03", true},
		{"annotation only (a run renamed)", "importer-manual", ann, sg, "2026-11-04", true},
		{"name only", name, nil, sg, "2026-11-04", true},
		{"they disagree", "importer-" + itoa(at.Unix()/60+1440), ann, sg, "", false},
		{"another CronJob's name", "exporter-" + itoa(at.Unix()/60), nil, sg, "", false},
		{"a manual run", "importer-09932cb3", nil, sg, "", false},
		{"a malformed annotation", name, map[string]string{annotationScheduled: "yesterday"}, sg, "", false},
	} {
		got, ok := ScheduledBusinessDate("importer", tc.name, tc.ann, tc.zone)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %q %v, want %q %v", tc.what, got, ok, tc.want, tc.ok)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
