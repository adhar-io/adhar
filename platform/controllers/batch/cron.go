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

// cronSchedule is a parsed 5-field cron expression, the shape the BatchJob XRD
// admits. The stepping rules are the CLI's (`cmd/batch/cron.go`) and the
// Console's (`perf-cron.ts`): `*`, lists, ranges, steps, month and weekday
// names, and day-of-month OR day-of-week when both are restricted. The observer
// needs the PREVIOUS fire as well as the next — `schedule.missed` asks whether
// the slot that just passed produced a run, and an SLA belongs to the slot whose
// run is (or should be) in flight.
//
// Fires are computed in the job's IANA zone in Go, never in PromQL, so a
// daylight-saving change moves a 01:30 job with the wall clock (§11.4).
type cronSchedule struct {
	minutes, hours, days, months, weekdays []bool
	domAny, dowAny                         bool
	loc                                    *time.Location
}

var cronMacros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dayNames   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

// parseCron reads an expression in a zone. False when it cannot be read.
func parseCron(expr string, loc *time.Location) (*cronSchedule, bool) {
	if m, ok := cronMacros[strings.TrimSpace(strings.ToLower(expr))]; ok {
		expr = m
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, false
	}
	s := &cronSchedule{loc: loc}
	var ok [5]bool
	s.minutes, ok[0] = cronField(fields[0], 0, 59, nil)
	s.hours, ok[1] = cronField(fields[1], 0, 23, nil)
	s.days, ok[2] = cronField(fields[2], 1, 31, nil)
	s.months, ok[3] = cronField(fields[3], 1, 12, monthNames)
	s.weekdays, ok[4] = cronField(fields[4], 0, 7, dayNames)
	for _, o := range ok {
		if !o {
			return nil, false
		}
	}
	s.weekdays[0] = s.weekdays[0] || s.weekdays[7]
	s.domAny, s.dowAny = fields[2] == "*", fields[4] == "*"
	if loc == nil {
		s.loc = time.UTC
	}
	return s, true
}

// next is the first fire strictly after `after`; false when none within a year.
func (s *cronSchedule) next(after time.Time) (time.Time, bool) {
	loc := s.loc
	t := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(1, 0, 1)
	for t.Before(limit) {
		if !s.months[int(t.Month())] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		dayOK, dowOK := s.days[t.Day()], s.weekdays[int(t.Weekday())]
		var dayMatches bool
		switch {
		case s.domAny && s.dowAny:
			dayMatches = true
		case s.domAny:
			dayMatches = dowOK
		case s.dowAny:
			dayMatches = dayOK
		default:
			dayMatches = dayOK || dowOK
		}
		if !dayMatches {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if !s.hours[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if !s.minutes[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

// prev is the last fire at or before `at`. It steps forward from progressively
// longer look-backs, so a minutely job costs a few hundred steps and a yearly
// one finds its fire without scanning a year minute by minute.
func (s *cronSchedule) prev(at time.Time) (time.Time, bool) {
	for _, back := range []time.Duration{2 * time.Hour, 26 * time.Hour, 8 * 24 * time.Hour, 32 * 24 * time.Hour, 367 * 24 * time.Hour} {
		var last time.Time
		t := at.Add(-back)
		for {
			n, ok := s.next(t)
			if !ok || n.After(at) {
				break
			}
			last, t = n, n
		}
		if !last.IsZero() {
			return last, true
		}
	}
	return time.Time{}, false
}

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
