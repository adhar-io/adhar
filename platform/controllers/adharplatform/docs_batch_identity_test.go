package adharplatform

// Guards for two contracts the batch design hands to the kit lane, added when its
// verification found each one left to guessing (2026-11-14): what identity every kind
// of Quartz trigger gives a run (§5.20.5), and which kit property every ADHAR_BATCH_*
// signal the design names binds to (§5.17). Both were mutation-tested (the design
// reverted to the text the verifier found, the test seen failing, restored).

import (
	"regexp"
	"strings"
	"testing"
)

// Every trigger kind in §5.20.6 must have a row in §5.20.5's identity table, saying what
// nominal time the launch resolves it with. A kind with no nominal time cannot carry
// adhar.scheduled-time: that is how fireNow(file=X) twice became two runs. And the
// interval grid must be anchored on the configured `start`, because Quartz's own
// startTime is moved to the handling time by a RESCHEDULE_NOW misfire.
func TestBatchDesignDocDefinesTheIdentityOfEveryTriggerKind(t *testing.T) {
	doc := batchDoc(t, batchDesignDoc)
	launch := batchDocSection(t, doc, "#### 5.20.5 ")
	triggers := batchDocSection(t, doc, "#### 5.20.6 ")

	var kinds []string
	inKinds := false
	for _, line := range strings.Split(triggers, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "| `kind` |") {
			inKinds = true
			continue
		}
		if !inKinds || strings.HasPrefix(line, "|---") {
			continue
		}
		if !strings.HasPrefix(line, "|") {
			break
		}
		first := strings.TrimSpace(strings.Split(strings.Trim(line, "|"), "|")[0])
		names := regexp.MustCompile("`([A-Za-z]+)`").FindAllStringSubmatch(first, -1)
		if len(names) == 0 { // a kit-internal kind is written without backticks
			kinds = append(kinds, strings.Fields(first)[0])
		}
		for _, n := range names {
			kinds = append(kinds, n[1])
		}
	}
	if len(kinds) < 5 {
		t.Fatalf("§5.20.6's trigger-kind table yields %v; its layout changed", kinds)
	}

	for _, kind := range kinds {
		var row []string
		for _, first := range []string{"`" + kind + "` |", kind + " |"} {
			for _, line := range strings.Split(launch, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "| "+first) {
					row = batchDocTableRow(line, strings.TrimSuffix(first, " |"))
				}
			}
		}
		if len(row) < 5 {
			t.Errorf("§5.20.5's identity table has no row for the trigger kind %s (kind, nominal time, adhar.scheduled-time, identity)", kind)
			continue
		}
		if row[2] == "none" && row[3] != "never" {
			t.Errorf("§5.20.5: the %s trigger has no nominal time but its adhar.scheduled-time cell is %q, not \"never\"; two calls with the same parameters would be two runs (AQ.6)", kind, row[3])
		}
		if kind == "interval" && (!strings.Contains(row[2], "grid") || !strings.Contains(row[2], "startTime")) {
			t.Errorf("§5.20.5: the interval trigger's nominal time (%q) must be the kit's grid, not Quartz's startTime", row[2])
		}
	}
	for _, want := range []string{"`adhar.grid.start`", "Unix epoch", "re-anchors", "`adhar.nominal`", "`adhar.identity`"} {
		if !strings.Contains(launch, want) {
			t.Errorf("§5.20.5 does not state %s, which the slot and runtime-trigger identity rest on", want)
		}
	}
}

// An ADHAR_BATCH_* variable is read by the kit through Spring's relaxed binding, so it
// IS the kit property of the same name: ADHAR_BATCH_MODE=chunked binds to
// adhar.batch.mode, whose values are auto|kubernetes|embedded, and the run fails to
// bind. Every `ADHAR_BATCH_X=value` the design names must therefore be a value its
// property's §5.17 row allows, whenever §5.17 lists that property.
func TestBatchDesignDocEnvSignalsAreValuesOfTheirKitProperty(t *testing.T) {
	doc := batchDoc(t, batchDesignDoc)
	props := batchDocSection(t, doc, "### 5.17 ")

	canonical := func(s string) string { return strings.ReplaceAll(strings.ToLower(s), "-", "") }
	rows := map[string]string{}
	token := regexp.MustCompile("`([a-z.][a-z0-9.-]*)`")
	for _, line := range strings.Split(props, "\n") {
		if !strings.HasPrefix(line, "| `adhar.") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		parent := ""
		for _, m := range token.FindAllStringSubmatch(cells[0], -1) {
			name := m[1]
			if strings.HasPrefix(name, ".") && parent != "" {
				name = parent + name
			} else if i := strings.LastIndex(name, "."); i > 0 {
				parent = name[:i]
			}
			rows[canonical(name)] = strings.Join(cells[1:], "|")
		}
	}
	if len(rows) < 10 {
		t.Fatalf("§5.17 yields only %d properties; its layout changed", len(rows))
	}

	checked := 0
	for _, m := range regexp.MustCompile(`\b(ADHAR_BATCH_[A-Z_]+)=([a-z][a-z0-9-]*)\b`).FindAllStringSubmatch(doc, -1) {
		prop := strings.ToLower(strings.ReplaceAll(m[1], "_", "."))
		row, ok := rows[canonical(prop)]
		if !ok {
			continue // a runner input with no property of its own (ADHAR_BATCH_TRIGGER, …)
		}
		checked++
		if !strings.Contains(row, "`"+m[2]+"`") {
			t.Errorf("design 0025 sets %s=%s, but Spring binds it to %s, whose §5.17 row does not allow `%s`", m[1], m[2], prop, m[2])
		}
	}
	if checked < 3 {
		t.Errorf("only %d ADHAR_BATCH_*=value signals matched a §5.17 property; the guard is no longer looking at anything", checked)
	}
}
