package adharplatform

// Guards for the batch DOCUMENTS (design 0025, ADR-0028, docs/BATCH.md and the
// core/adhar-batch and core/volcano READMEs) against the tree they describe. A design
// that names a PriorityClass value, a scheduler configuration or a manifest the stack
// no longer ships is worse than no design: the next lane builds to it. Each guard reads
// files only, so it runs in `make test` without a cluster, and each has been
// mutation-tested (the document or the tree changed, the test seen failing, restored).

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const (
	batchDesignDoc = "docs/design/0025-batch-platform.md"
	batchADRDoc    = "docs/adr/0028-batch-scheduler.md"
	batchGuideDoc  = "docs/BATCH.md"
)

func batchDoc(t *testing.T, rel string) string {
	t.Helper()
	return readRepoFile(t, strings.Split(rel, "/")...)
}

// batchDocSection returns the text from the heading that starts with `heading` up to
// the next heading of the same or a higher level.
func batchDocSection(t *testing.T, doc, heading string) string {
	t.Helper()
	level := strings.IndexFunc(heading, func(r rune) bool { return r != '#' })
	lines := strings.Split(doc, "\n")
	start := -1
	for i, line := range lines {
		if start < 0 {
			if strings.HasPrefix(line, heading) {
				start = i
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			l := strings.IndexFunc(line, func(r rune) bool { return r != '#' })
			if l > 0 && l <= level && strings.HasPrefix(line[l:], " ") {
				return strings.Join(lines[start:i], "\n")
			}
		}
	}
	if start < 0 {
		t.Fatalf("no heading %q", heading)
	}
	return strings.Join(lines[start:], "\n")
}

// batchDocTableRow returns the trimmed cells of the first table row whose first cell
// starts with `first`, or nil.
func batchDocTableRow(text, first string) []string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line) // a table may sit inside a list item
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if len(cells) > 0 && strings.HasPrefix(cells[0], first) {
			return cells
		}
	}
	return nil
}

// The documents write negative numbers with a real minus sign.
func batchDocNumber(v float64) string {
	if v < 0 {
		return fmt.Sprintf("−%g", -v)
	}
	return fmt.Sprintf("%g", v)
}

type batchPriorityClass struct {
	name   string
	value  string
	policy string
}

func shippedBatchPriorityClasses(t *testing.T) []batchPriorityClass {
	t.Helper()
	var out []batchPriorityClass
	for _, doc := range yamlDocs(t, filepath.Join(stackPackagesDir(t), "core/adhar-batch/manifests/priorityclasses.yaml")) {
		if doc["kind"] != "PriorityClass" {
			continue
		}
		name, _ := metaName(doc).(string)
		var value float64
		switch v := doc["value"].(type) {
		case float64:
			value = v
		case int64:
			value = float64(v)
		case int:
			value = float64(v)
		default:
			t.Fatalf("PriorityClass %s: value %v (%T) is not a number", name, doc["value"], doc["value"])
		}
		policy, _ := doc["preemptionPolicy"].(string)
		if policy == "" {
			policy = "PreemptLowerPriority" // the API default
		}
		out = append(out, batchPriorityClass{name, batchDocNumber(value), policy})
	}
	if len(out) < 2 {
		t.Fatalf("read %d batch PriorityClasses; the manifest layout changed", len(out))
	}
	return out
}

// Design §4.4 and ADR-0028 decision 6 are where a reader learns what a batch run may
// preempt. Every class the stack ships must appear in both tables with the value and
// preemptionPolicy it is shipped with (D15 changed exactly these), and the user guide
// must name it.
func TestBatchDesignDocNamesEveryShippedPriorityClass(t *testing.T) {
	design := batchDocSection(t, batchDoc(t, batchDesignDoc), "### 4.4 ")
	adr := batchDocSection(t, batchDoc(t, batchADRDoc), "## Decision")
	guide := batchDoc(t, batchGuideDoc)
	for _, pc := range shippedBatchPriorityClasses(t) {
		for _, table := range []struct{ where, text string }{{"design 0025 §4.4", design}, {"ADR-0028 decision 6", adr}} {
			row := batchDocTableRow(table.text, "`"+pc.name+"`")
			if row == nil {
				t.Errorf("%s has no row for PriorityClass %s, which core/adhar-batch ships", table.where, pc.name)
				continue
			}
			if len(row) < 3 || !strings.HasPrefix(row[1], pc.value) || row[2] != pc.policy {
				t.Errorf("%s says %s is %v; the stack ships value %s, preemptionPolicy %s", table.where, pc.name, row, pc.value, pc.policy)
			}
		}
		if !strings.Contains(guide, "`"+pc.name+"`") {
			t.Errorf("docs/BATCH.md never names PriorityClass %s", pc.name)
		}
	}
}

type batchSchedulerConf struct {
	Actions string `json:"actions"`
	Tiers   []struct {
		Plugins []map[string]any `json:"plugins"`
	} `json:"tiers"`
}

func findStringKey(v any, key string) (string, bool) {
	switch m := v.(type) {
	case map[string]any:
		for k, val := range m {
			if s, ok := val.(string); ok && k == key {
				return s, true
			}
			if s, ok := findStringKey(val, key); ok {
				return s, true
			}
		}
	case []any:
		for _, val := range m {
			if s, ok := findStringKey(val, key); ok {
				return s, true
			}
		}
	}
	return "", false
}

// Design §4.2 prints the scheduler configuration and explains it plugin by plugin
// (overcommit, D16; gang's reclaim vote). It must be the configuration core/volcano
// renders, tier by tier and argument by argument.
func TestBatchDesignDocSchedulerConfigIsTheShippedOne(t *testing.T) {
	section := batchDocSection(t, batchDoc(t, batchDesignDoc), "### 4.2 ")
	m := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindStringSubmatch(section)
	if m == nil {
		t.Fatal("design §4.2 has no yaml block")
	}
	var inDoc batchSchedulerConf
	if err := yaml.Unmarshal([]byte(m[1]), &inDoc); err != nil {
		t.Fatalf("design §4.2's configuration does not parse: %v", err)
	}

	var values any
	if err := yaml.Unmarshal([]byte(readStackFile(t, "core/volcano/values.yaml")), &values); err != nil {
		t.Fatal(err)
	}
	raw, ok := findStringKey(values, "scheduler_config_override")
	if !ok {
		t.Fatal("core/volcano/values.yaml has no scheduler_config_override")
	}
	var shipped batchSchedulerConf
	if err := yaml.Unmarshal([]byte(raw), &shipped); err != nil {
		t.Fatal(err)
	}

	norm := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, ",", " ")), ",") }
	if norm(inDoc.Actions) != norm(shipped.Actions) {
		t.Errorf("design §4.2 actions %q; core/volcano ships %q", inDoc.Actions, shipped.Actions)
	}
	if len(inDoc.Tiers) != len(shipped.Tiers) {
		t.Fatalf("design §4.2 has %d tiers; core/volcano ships %d", len(inDoc.Tiers), len(shipped.Tiers))
	}
	for i := range shipped.Tiers {
		if !reflect.DeepEqual(inDoc.Tiers[i].Plugins, shipped.Tiers[i].Plugins) {
			t.Errorf("design §4.2 tier %d is\n  %v\ncore/volcano ships\n  %v", i+1, inDoc.Tiers[i].Plugins, shipped.Tiers[i].Plugins)
		}
	}
}

// §15.1 is the contract between lanes. A decision referenced anywhere in the design
// must be a row there, the 2026-11-14 decisions must each carry the user question
// they settle, and no unfilled placeholder may survive into the document.
func TestBatchDesignDocRecordsEveryDecisionItCites(t *testing.T) {
	doc := batchDoc(t, batchDesignDoc)
	decisions := batchDocSection(t, doc, "### 15.1 ")
	if strings.Contains(doc, "@@") {
		t.Error("design 0025 still holds an @@ placeholder")
	}
	cited := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bD(\d{1,2})\b`).FindAllStringSubmatch(doc, -1) {
		cited["D"+m[1]] = true
	}
	var missing []string
	// Exact first cells: batchDocTableRow matches by prefix, so D1 would match a D15 row.
	for id := range cited {
		found := false
		for _, line := range strings.Split(decisions, "\n") {
			if strings.HasPrefix(line, "| "+id+" |") {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("design 0025 cites decisions with no row in §15.1: %v", missing)
	}
	for i, dec := range []string{"DEC-1", "DEC-2", "DEC-3", "DEC-4", "DEC-5", "DEC-6", "DEC-7", "DEC-8", "DEC-9"} {
		id := fmt.Sprintf("D%d", 15+i)
		row := batchDocTableRow(decisions, id)
		if row == nil || row[0] != id || !strings.Contains(strings.Join(row, "|"), "("+dec+",") {
			t.Errorf("§15.1 row %s does not say which user question it settles (%s)", id, dec)
		}
	}
	if !strings.Contains(decisions, "Decisions of 2026-11-14") {
		t.Error("§15.1 does not date the 2026-11-14 decisions")
	}
}

// The Quartz addendum (§5.20, D24) is what the kit, template and Console lanes build
// next: every work item and acceptance criterion it cites must be defined, and the
// contract points the user asked for must be in it.
func TestBatchDesignDocQuartzAddendumIsComplete(t *testing.T) {
	doc := batchDoc(t, batchDesignDoc)
	quartz := batchDocSection(t, doc, "### 5.20 ")
	for i := 1; i <= 10; i++ {
		if !strings.Contains(quartz, fmt.Sprintf("#### 5.20.%d ", i)) {
			t.Errorf("§5.20 has no subsection 5.20.%d", i)
		}
	}
	for _, want := range []string{
		"adhar.batch.scheduler.engine", "<schema>.QRTZ_", "org.quartz.jobStore.isClustered",
		"spring.quartz.jdbc.initialize-schema", "V2_5_2__quartz_tables.sql", "adhar_batch_quartz_schema_history",
		"BatchOperator", "JobParametersResolver", "naming v1", "adhar.scheduled-time",
		"MISFIRE_INSTRUCTION_DO_NOTHING", "MISFIRE_INSTRUCTION_FIRE_ONCE_NOW", "MISFIRE_INSTRUCTION_IGNORE_MISFIRE_POLICY",
		"AdharBusinessCalendar", "D5", "BatchScheduler", "D9", "batch-schedules", "inServiceBatch",
	} {
		if !strings.Contains(quartz, want) {
			t.Errorf("§5.20 does not mention %q", want)
		}
	}

	work := batchDocSection(t, doc, "## 16. ")
	plan := batchDocSection(t, doc, "### Addendum Q ")
	rowIn := func(text, id string) bool {
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "| "+id+" |") {
				return true
			}
		}
		return false
	}
	for _, m := range regexp.MustCompile(`\b(?:KIT|TPL|CON|CLI)-Q\.\d+\b`).FindAllString(doc, -1) {
		if !rowIn(work, m) {
			t.Errorf("design 0025 cites %s, which §16 does not define", m)
		}
	}
	for _, m := range regexp.MustCompile(`\bAQ\.\d+\b`).FindAllString(doc, -1) {
		if !rowIn(plan, m) {
			t.Errorf("design 0025 cites %s, which the Addendum Q plan does not define", m)
		}
	}
	for _, id := range []string{"KIT-Q.1", "KIT-Q.7", "TPL-Q.1", "CON-Q.1", "CON-Q.3", "CLI-Q.1"} {
		if !rowIn(work, id) {
			t.Errorf("§16 lacks %s", id)
		}
	}
}

// D23: the Queues view is a Develop tab. A stale Platform-module path in the work
// breakdown or the inventory would send the next Console lane to build it twice.
func TestBatchDesignDocPlacesTheQueuesViewInDevelop(t *testing.T) {
	doc := batchDoc(t, batchDesignDoc)
	for _, section := range []string{"## 16. ", "## 20. "} {
		text := batchDocSection(t, doc, section)
		if strings.Contains(text, "modules/platform/src/views/batch-queues.tsx") {
			t.Errorf("design %s still places the Queues view in modules/platform (D23 moved it to Develop)", strings.TrimSpace(section))
		}
		if !strings.Contains(text, "modules/develop/src/") || !strings.Contains(text, "batch-queues.tsx") {
			t.Errorf("design %s does not name the Develop module's batch-queues.tsx", strings.TrimSpace(section))
		}
	}
}

// githubAnchor is the id GitHub gives a Markdown heading.
func githubAnchor(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r > 127 && r != '→' && r != '—' && r != '–' && r != '×' && r != '…':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// The user guide links its own sections (Volcano, Quartz). A renamed heading leaves a
// link that goes nowhere; the same holds for the ADR's link to its amendments.
func TestBatchUserGuideDocLinksResolve(t *testing.T) {
	for _, rel := range []string{batchGuideDoc, batchADRDoc, batchDesignDoc} {
		doc := batchDoc(t, rel)
		anchors := map[string]bool{}
		for _, m := range regexp.MustCompile(`(?m)^#{1,6} (.+)$`).FindAllStringSubmatch(doc, -1) {
			anchors[githubAnchor(m[1])] = true
		}
		for _, m := range regexp.MustCompile(`\]\(#([^)]+)\)`).FindAllStringSubmatch(doc, -1) {
			if !anchors[m[1]] {
				t.Errorf("%s links to #%s, which no heading there produces", rel, m[1])
			}
		}
	}
	guide := batchDoc(t, batchGuideDoc)
	for _, heading := range []string{"## Volcano: queues and gangs (Phase 2)", "## In-service jobs on Quartz (planned)"} {
		if !strings.Contains(guide, "\n"+heading+"\n") {
			t.Errorf("docs/BATCH.md lacks the section %q", heading)
		}
	}
}

// readmeListsManifests fails for every file under dir that the README does not name.
func readmeListsManifests(t *testing.T, pkg string, recursive bool) {
	t.Helper()
	root := filepath.Join(stackPackagesDir(t), pkg)
	readme := readStackFile(t, filepath.Join(pkg, "README.md"))
	dir := filepath.Join(root, "manifests")
	var files []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && !recursive {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("%s/manifests is empty", pkg)
	}
	for _, f := range files {
		if !strings.Contains(readme, "`"+f+"`") && !strings.Contains(readme, "`manifests/"+f+"`") {
			t.Errorf("%s/README.md does not list manifests/%s", pkg, f)
		}
	}
}

// The package READMEs are the table of contents an operator reads before enabling a
// package; a manifest missing from it is one nobody reviews.
func TestAdharBatchReadmeListsEveryManifest(t *testing.T) {
	readmeListsManifests(t, "core/adhar-batch", true)
}

func TestVolcanoReadmeListsEveryManifest(t *testing.T) {
	readmeListsManifests(t, volcanoDir, false)
}

// ADR-0028 is amended, not rewritten: the amendment must say what changed and point at
// the design rows that carry the reasoning.
func TestBatchSchedulerADRDocRecordsTheAmendments(t *testing.T) {
	adr := batchDoc(t, batchADRDoc)
	amend := batchDocSection(t, adr, "## Amendments (2026-11-14)")
	for _, id := range []string{"D15", "D16", "D17", "D18", "D19", "D20", "D22"} {
		if !strings.Contains(amend, id) {
			t.Errorf("ADR-0028's amendments do not mention design %s", id)
		}
	}
	if !strings.Contains(batchDocSection(t, adr, "## Decision"), "Amended 2026-11-14") {
		t.Error("ADR-0028 decision 6 does not date its amendment")
	}
}
