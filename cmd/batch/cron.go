/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"strconv"
	"strings"
	"time"
)

// nextCron steps a 5-field cron expression (the shape the BatchJob XRD admits)
// to its first fire strictly after `after`, in loc. False when the expression
// cannot be read or never fires within a year.
//
// Written here rather than taken from a library because nothing else in the
// CLI needs one, and the Console computes the same "next run" with its own
// stepper (`perf-cron.ts`): the two must agree on the ordinary expressions a
// job declares, and the Kubernetes CronJob controller's own answer, when
// kube-state-metrics has it, is what the Console prefers. Standard semantics:
// `*`, lists, ranges, steps, month and weekday names, and day-of-month OR
// day-of-week when both are restricted.
func nextCron(expr string, after time.Time, loc *time.Location) (time.Time, bool) {
	fields := strings.Fields(expandMacro(expr))
	if len(fields) != 5 {
		return time.Time{}, false
	}
	minutes, ok1 := cronField(fields[0], 0, 59, nil)
	hours, ok2 := cronField(fields[1], 0, 23, nil)
	days, ok3 := cronField(fields[2], 1, 31, nil)
	months, ok4 := cronField(fields[3], 1, 12, monthNames)
	weekdays, ok5 := cronField(fields[4], 0, 7, dayNames)
	if !(ok1 && ok2 && ok3 && ok4 && ok5) {
		return time.Time{}, false
	}
	weekdays[0] = weekdays[0] || weekdays[7] // 7 is Sunday too
	domAny, dowAny := fields[2] == "*", fields[4] == "*"

	t := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(1, 0, 1)
	for t.Before(limit) {
		if !months[int(t.Month())] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		dayOK := days[t.Day()]
		dowOK := weekdays[int(t.Weekday())]
		var dayMatches bool
		switch {
		case domAny && dowAny:
			dayMatches = true
		case domAny:
			dayMatches = dowOK
		case dowAny:
			dayMatches = dayOK
		default:
			dayMatches = dayOK || dowOK
		}
		if !dayMatches {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if !hours[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if !minutes[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

var cronMacros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

func expandMacro(expr string) string {
	if m, ok := cronMacros[strings.TrimSpace(strings.ToLower(expr))]; ok {
		return m
	}
	return expr
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dayNames   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

// cronField expands one field into the set of values it admits.
func cronField(field string, lo, hi int, names map[string]int) ([]bool, bool) {
	set := make([]bool, hi+1)
	for _, part := range strings.Split(field, ",") {
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n < 1 {
				return nil, false
			}
			step = n
		}
		from, to := lo, hi
		if rangePart != "*" {
			a, b, isRange := strings.Cut(rangePart, "-")
			var ok bool
			if from, ok = cronValue(a, names); !ok {
				return nil, false
			}
			to = from
			if isRange {
				if to, ok = cronValue(b, names); !ok {
					return nil, false
				}
			} else if hasStep {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return nil, false
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return set, true
}

func cronValue(s string, names map[string]int) (int, bool) {
	if n, ok := names[strings.ToLower(s)]; ok {
		return n, true
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}
