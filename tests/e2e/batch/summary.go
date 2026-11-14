package batch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Summary is the run's termination summary v1 (design 0025 §5.13, decision
// D13), as the kubelet hands it back in the batch container's termination
// message. Every field is optional: instanceId, executionId and status are
// absent when no execution exists (an invalid request), and the kubelet keeps
// only 4096 bytes. Nil pointers mean "not written", never zero.
type Summary struct {
	V                                *int64
	Job, BatchJob                    string
	InstanceID, ExecutionID          *int64
	Status                           string
	ExitCode                         *int64
	Outcome, Reason                  string
	Trigger, BusinessDate            string
	Read, Written, Skipped, Filtered *int64
	DeadLettered                     *int64
	StepsTruncated                   bool
	Steps                            []StepSummary
	// Truncated says the text was cut mid-document; the fields before the cut were recovered.
	Truncated bool
}

// StepSummary is one entry of `steps`, the detail dropped first.
type StepSummary struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Read    int64  `json:"read"`
	Written int64  `json:"written"`
	Skipped int64  `json:"skipped"`
}

// D13's reasons, by outcome.
var (
	SkipReasons    = []string{"already-complete", "duplicate", "calendar"}
	InvalidReasons = []string{"parameters", "unknown-job", "incrementer", "resourceless-repository", "migration", "business-date"}
)

var topLevelScalar = regexp.MustCompile(`"([A-Za-z]+)"\s*:\s*("(?:[^"\\]|\\.)*"|-?\d+|true|false)\s*[,}]`)

// ParseSummary reads a termination message; nil when it is not a summary at
// all (a JVM that died before the runner wrote one leaves something else, or
// nothing). A document cut by the kubelet still yields its top-level scalars,
// which come before `steps`.
func ParseSummary(text string) *Summary {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "{") {
		return nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(t), &doc); err == nil {
		return summaryOf(doc, false)
	}
	head := t
	if i := strings.Index(t, `"steps":`); i >= 0 {
		head = t[:i]
	}
	doc = map[string]json.RawMessage{}
	for _, m := range topLevelScalar.FindAllStringSubmatch(head, -1) {
		doc[m[1]] = json.RawMessage(m[2])
	}
	return summaryOf(doc, true)
}

func summaryOf(doc map[string]json.RawMessage, truncated bool) *Summary {
	_, v := doc["v"]
	_, outcome := doc["outcome"]
	if !v && !outcome {
		return nil
	}
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(doc[k], &s)
		return s
	}
	num := func(k string) *int64 {
		var n int64
		if raw, ok := doc[k]; !ok || json.Unmarshal(raw, &n) != nil {
			return nil
		}
		return &n
	}
	s := &Summary{
		V: num("v"), Job: str("job"), BatchJob: str("batchJob"),
		InstanceID: num("instanceId"), ExecutionID: num("executionId"), Status: str("status"),
		ExitCode: num("exitCode"), Outcome: str("outcome"), Reason: str("reason"),
		Trigger: str("trigger"), BusinessDate: str("businessDate"),
		Read: num("read"), Written: num("written"), Skipped: num("skipped"), Filtered: num("filtered"),
		DeadLettered: num("deadLettered"), Truncated: truncated,
	}
	_ = json.Unmarshal(doc["stepsTruncated"], &s.StepsTruncated)
	_ = json.Unmarshal(doc["steps"], &s.Steps)
	return s
}

// CheckSkipped is nil when the summary says the run was skipped for one of the
// given D13 reasons (any skip reason when none is given).
func (s *Summary) CheckSkipped(reasons ...string) error {
	return s.check("skipped", SkipReasons, reasons)
}

// CheckInvalid is nil when the summary says the request was refused as invalid
// for one of the given D13 reasons, with no execution behind it.
func (s *Summary) CheckInvalid(reasons ...string) error {
	if err := s.check("invalid", InvalidReasons, reasons); err != nil {
		return err
	}
	if s.ExecutionID != nil || s.InstanceID != nil || s.Status != "" {
		return fmt.Errorf("an invalid request created no execution, yet the summary names execution %v / instance %v / status %q (D13)",
			deref(s.ExecutionID), deref(s.InstanceID), s.Status)
	}
	return nil
}

func (s *Summary) check(outcome string, known, want []string) error {
	if s == nil {
		return fmt.Errorf("no termination summary: D13 writes one for a %s run too", outcome)
	}
	if s.Outcome != outcome {
		// The template's stand-in launcher writes "skipped(duplicate)"; D13 v1
		// moved the reason into its own field so exit 0 can be told apart.
		return fmt.Errorf("outcome %q, want %q with a `reason` (D13 v1)", s.Outcome, outcome)
	}
	if !slices.Contains(known, s.Reason) {
		return fmt.Errorf("%s with reason %q, which D13 does not define (%v)", outcome, s.Reason, known)
	}
	if len(want) > 0 && !slices.Contains(want, s.Reason) {
		return fmt.Errorf("%s for %q, want one of %v", outcome, s.Reason, want)
	}
	return nil
}

func deref(p *int64) interface{} {
	if p == nil {
		return "-"
	}
	return *p
}
