package batch

import (
	"strconv"
	"strings"
	"time"
)

const annotationScheduled = "batch.kubernetes.io/cronjob-scheduled-timestamp"

// ScheduledBusinessDate is the business date of a run the CronJob controller
// created: its scheduled time in the job's zone, truncated to a date (§5.5 —
// "never now() for a scheduled run"). The controller states the time twice, as
// the scheduled-timestamp annotation and as the minutes in the Job's name
// (`<cronjob>-<minutes since the epoch>`); the suite reads both, and two that
// disagree are no answer at all.
func ScheduledBusinessDate(cronJob, jobName string, annotations map[string]string, zone *time.Location) (string, bool) {
	var times []time.Time
	if raw := annotations[annotationScheduled]; raw != "" {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return "", false
		}
		times = append(times, at)
	}
	if minutes, ok := strings.CutPrefix(jobName, cronJob+"-"); ok {
		if n, err := strconv.ParseInt(minutes, 10, 64); err == nil && n > 0 {
			times = append(times, time.Unix(n*60, 0))
		}
	}
	if len(times) == 0 || (len(times) == 2 && !times[0].Equal(times[1])) {
		return "", false
	}
	return times[0].In(zone).Format(time.DateOnly), true
}
