package batch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

var repoStack = filepath.Join("..", "..", "..", "platform", "stack")

/* ─────────────── knobs ─────────────── */

func TestVolcanoCanaryIsOptInAndReadsItsKnobs(t *testing.T) {
	base, err := LoadConfig(envOf(nil), configNow)
	if err != nil {
		t.Fatal(err)
	}
	for v, want := range map[string]bool{"": false, "0": false, "no": false, "1": true, "true": true} {
		c, err := LoadVolcanoConfig(envOf(map[string]string{"ADHAR_E2E_VOLCANO": v}), base)
		if err != nil || c.Enabled != want {
			t.Errorf("ADHAR_E2E_VOLCANO=%q: enabled %v (%v), want %v", v, c.Enabled, err, want)
		}
	}
	c, _ := LoadVolcanoConfig(envOf(nil), base)
	if c.Gang != 3 || c.MinKubeMinor != 37 || c.Templates[0] != "springboot-batch-partitioned" ||
		c.Hold != 90*time.Second || c.CompleteWithin != 30*time.Second {
		t.Errorf("defaults: %+v", c)
	}
	if c.App == base.App || !strings.HasSuffix(c.App, base.Tag) || !batchJobName.MatchString(c.App) {
		t.Errorf("the partitioned app %q must be a BatchJob name of its own, tagged like %q", c.App, base.App)
	}
	for k, v := range map[string]string{
		"ADHAR_E2E_VOLCANO_GANG": "0", "ADHAR_E2E_VOLCANO_SCENARIO_TIMEOUT": "x", "ADHAR_E2E_VOLCANO_HOLD": "soon",
		"ADHAR_E2E_VOLCANO_APP": "Not_A_Name", "ADHAR_E2E_VOLCANO_KUBE_MINOR": "3",
	} {
		if _, err := LoadVolcanoConfig(envOf(map[string]string{k: v}), base); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%q was accepted (%v)", k, v, err)
		}
	}
	if _, err := LoadVolcanoConfig(envOf(map[string]string{"ADHAR_E2E_VOLCANO_APP": base.App}), base); err == nil {
		t.Error("the partitioned app may not be the Phase-1 app")
	}
	skip, _ := LoadConfig(envOf(map[string]string{"ADHAR_E2E_SKIP_UP": "1"}), configNow)
	if _, err := LoadVolcanoConfig(envOf(map[string]string{"ADHAR_E2E_VOLCANO_STACK": "/x"}), skip); err == nil {
		t.Error("a staged stack with SKIP_UP was accepted: nothing would seed it")
	}
	if _, err := LoadVolcanoConfig(envOf(map[string]string{"ADHAR_E2E_VOLCANO_COMPLETE_WITHIN": "30m"}), base); err == nil {
		t.Error("a manager-ends-workers bound as long as the scenario was accepted")
	}

	pinned, _ := LoadVolcanoConfig(envOf(map[string]string{"ADHAR_E2E_KUBE_VERSION": "v1.37.2"}), base)
	got := strings.Join(pinned.UpArgs(20*time.Minute, "/tmp/stack"), " ")
	if got != "up --recreate --apps-timeout 20m0s -p /tmp/stack --kube-version v1.37.2" {
		t.Errorf("up args: %s", got)
	}
	if got := strings.Join(c.UpArgs(20*time.Minute, ""), " "); got != "up --recreate --apps-timeout 20m0s" {
		t.Errorf("without Volcano the up args are Phase 1's: %s", got)
	}
}

/* ─────────────── the staged stack (A2.9) ─────────────── */

func copyInto(t *testing.T, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func treeHash(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		h.Write([]byte(rel))
		if d.Type()&fs.ModeSymlink != 0 {
			l, _ := os.Readlink(p)
			h.Write([]byte(l))
			return nil
		}
		raw, err := os.ReadFile(p)
		h.Write(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fu2Element is the element stage 2's FU-2 adds after "volcano". Until it is in
// the shipped appset, the fixture gains it, so the rest of the staging is
// tested on today's real files either way.
const fu2Element = `          - name: "adhar-batch-volcano"
            enabled: "false"
            namespace: "adhar-system"
            category: "core"
            manifestPath: "core/adhar-batch/manifests/volcano"
`

// fixtureStack is a stack of the real profile files and contracts plus a
// nested file and a symlink, which a copy must carry.
func fixtureStack(t *testing.T) (string, bool) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "stack")
	for _, f := range append(localProfileFiles,
		filepath.Join("packages", "core", "volcano", "adhar-package.yaml"),
		filepath.Join("packages", "core", "adhar-batch", "adhar-package.yaml"),
		filepath.Join("packages", "security", "cert-manager", "adhar-package.yaml"),
		filepath.Join("packages", "security", "kyverno", "adhar-package.yaml"),
		filepath.Join("packages", "core", "volcano", "manifests", "root-queue.yaml.tmpl"),
	) {
		copyInto(t, filepath.Join(repoStack, f), filepath.Join(src, f))
	}
	if err := os.Symlink("root-queue.yaml.tmpl", filepath.Join(src, "packages", "core", "volcano", "manifests", "link.yaml")); err != nil {
		t.Fatal(err)
	}
	shipped := true
	for _, f := range localProfileFiles {
		raw, _ := os.ReadFile(filepath.Join(src, f))
		if strings.Contains(string(raw), `name: "adhar-batch-volcano"`) {
			continue
		}
		shipped = false
		indent := fu2Element
		if f != localProfileFiles[0] {
			indent = strings.ReplaceAll(fu2Element, "          - name", "  - name")
			indent = strings.ReplaceAll(indent, "            ", "    ")
		}
		anchor := regexp.MustCompile(`(?m)^\s*- name: "Kamaji"\n`)
		loc := anchor.FindIndex(raw)
		if loc == nil {
			t.Fatalf("%s: no anchor for the FU-2 element", f)
		}
		_ = os.WriteFile(filepath.Join(src, f), append(append(append([]byte{}, raw[:loc[0]]...), indent...), raw[loc[0]:]...), 0o644)
	}
	return src, shipped
}

// A2.9 runs on a COPY: the staging switches the Volcano elements on in the
// copy's local profile, both files, and never writes to the stack it copies.
func TestVolcanoCanaryStagesACopyAndNeverTheShippedProfile(t *testing.T) {
	src, shipped := fixtureStack(t)
	if !shipped {
		t.Log("PENDING FU-2: the shipped appsets do not wire adhar-batch-volcano yet; the fixture adds it")
	}
	names, err := VolcanoStackElements(src)
	if err != nil {
		t.Fatal(err)
	}
	before := treeHash(t, src)
	dst := filepath.Join(t.TempDir(), "staged")
	changed, err := StageVolcanoStack(src, dst, names)
	if err != nil {
		t.Fatal(err)
	}
	if after := treeHash(t, src); after != before {
		t.Fatal("staging wrote to the stack it copies")
	}
	if strings.Join(changed, ",") != "volcano,adhar-batch-volcano,cert-manager" {
		t.Errorf("switched on %v, want volcano, adhar-batch-volcano and cert-manager (kyverno is on already)", changed)
	}
	staged, _ := os.ReadFile(filepath.Join(dst, localProfileFiles[0]))
	problems, err := VolcanoProfileProblems(staged, names)
	if err != nil || len(problems) > 0 {
		t.Errorf("the staged profile: %v %v", problems, err)
	}
	// Every other line of both files is untouched: one line per switched element.
	for _, f := range localProfileFiles {
		a, _ := os.ReadFile(filepath.Join(src, f))
		b, _ := os.ReadFile(filepath.Join(dst, f))
		al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
		if len(al) != len(bl) {
			t.Fatalf("%s: %d lines became %d", f, len(al), len(bl))
		}
		diff := 0
		for i := range al {
			if al[i] != bl[i] {
				diff++
				if strings.TrimSpace(bl[i]) != `enabled: "true"` {
					t.Errorf("%s:%d changed to %q", f, i+1, bl[i])
				}
			}
		}
		if diff != len(changed) {
			t.Errorf("%s: %d lines changed, want %d", f, diff, len(changed))
		}
	}
	// Both files of the local profile, named here rather than through the list
	// the staging uses, so a staging that forgot one is caught.
	for _, f := range []string{"adhar-appset-local.yaml", "environments/local/config.yaml"} {
		for _, n := range names {
			if !enabledIn(t, filepath.Join(dst, f), n) {
				t.Errorf("%s: %s is not enabled in the staged copy", f, n)
			}
		}
	}
	if l, err := os.Readlink(filepath.Join(dst, "packages", "core", "volcano", "manifests", "link.yaml")); err != nil || l != "root-queue.yaml.tmpl" {
		t.Errorf("the symlink was not carried: %q %v", l, err)
	}
	// The shipped profile still runs Phase 1 without Volcano (A1.7).
	if p, _ := LocalProfileProblems(readLocalAppSet(t)); len(p) > 0 {
		t.Errorf("the shipped profile: %v", p)
	}
	// Staged twice is still the same profile, and nothing more changes.
	again := filepath.Join(t.TempDir(), "again")
	if _, err := StageVolcanoStack(dst, again, names); err != nil {
		t.Fatal(err)
	}
	if treeHash(t, again) != treeHash(t, dst) {
		t.Error("restaging a staged stack changed it")
	}
}

// enabledIn reads one element's enabled line, independently of enableElement.
func enabledIn(t *testing.T, path, name string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	in := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- name:") {
			in = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:")), `"`) == name
			continue
		}
		if in && strings.HasPrefix(trimmed, "enabled:") {
			return strings.Contains(trimmed, `"true"`)
		}
	}
	return false
}

func TestVolcanoStagingRefusesWhatWouldGoWrong(t *testing.T) {
	src, _ := fixtureStack(t)
	before := treeHash(t, src)
	if _, err := StageVolcanoStack(src, filepath.Join(src, "inside"), VolcanoElements); err == nil || !strings.Contains(err.Error(), "must be outside") {
		t.Errorf("a staged stack inside its source: %v", err)
	}
	if treeHash(t, src) != before {
		t.Fatal("a refused staging wrote into the stack")
	}
	full := t.TempDir()
	_ = os.WriteFile(filepath.Join(full, "x"), nil, 0o644)
	if _, err := StageVolcanoStack(src, full, VolcanoElements); err == nil {
		t.Error("a non-empty destination was accepted")
	}
	if _, err := StageVolcanoStack(t.TempDir(), filepath.Join(t.TempDir(), "d"), VolcanoElements); err == nil {
		t.Error("a directory that is not a stack was accepted")
	}
	if _, err := StageVolcanoStack(src, filepath.Join(t.TempDir(), "d"), []string{"no-such-package"}); err == nil || !strings.Contains(err.Error(), "no-such-package") {
		t.Errorf("an unwired element: %v", err)
	}
	// The FU-2 element missing gives the reason, not just "not found".
	bare := filepath.Join(t.TempDir(), "bare")
	for _, f := range localProfileFiles {
		copyInto(t, filepath.Join(src, f), filepath.Join(bare, f))
		raw, _ := os.ReadFile(filepath.Join(bare, f))
		_ = os.WriteFile(filepath.Join(bare, f), []byte(strings.ReplaceAll(string(raw), `"adhar-batch-volcano"`, `"renamed"`)), 0o644)
	}
	if _, err := StageVolcanoStack(bare, filepath.Join(t.TempDir(), "d"), VolcanoElements); err == nil || !strings.Contains(err.Error(), "FU-2") {
		t.Errorf("without the adhar-batch-volcano element: %v", err)
	}
	// An element without an enabled line never takes its neighbour's.
	one := filepath.Join(t.TempDir(), "one.yaml")
	_ = os.WriteFile(one, []byte("- name: \"a\"\n  namespace: x\n- name: \"b\"\n  enabled: \"false\"\n"), 0o644)
	if _, err := enableElement(one, "a"); err == nil {
		t.Error("element a took element b's enabled line")
	}
	if raw, _ := os.ReadFile(one); strings.Contains(string(raw), `"true"`) {
		t.Errorf("the file changed: %s", raw)
	}
}

// What the canary switches on follows the packages' own contracts: volcano
// needs cert-manager (its webhook certificate); the optional kube-prometheus
// is left as the profile has it.
func TestVolcanoStackElementsFollowTheContracts(t *testing.T) {
	names, err := VolcanoStackElements(repoStack)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	for _, n := range []string{"volcano", "adhar-batch-volcano", "cert-manager", "kyverno"} {
		if !got[n] {
			t.Errorf("%s is not switched on for the canary: %v", n, names)
		}
	}
	if got["kube-prometheus"] {
		t.Error("an optional dependency was made required")
	}
}

/* ─────────────── queues and Volcano's objects ─────────────── */

// The suite computes a namespace's leaf itself; it must be the join the
// binding policy admits, or every check would pass on the wrong queue.
func TestLeafQueueIsTheBindingPolicysJoin(t *testing.T) {
	policy, err := os.ReadFile(filepath.Join(repoStack, "packages", "core", "adhar-batch", "manifests", "volcano", "kyverno-queue.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const join = `join('-', [ns."adhar.io/organisation", ns."adhar.io/team", ns."adhar.io/tier"])`
	if strings.Count(string(policy), join) < 2 {
		t.Fatalf("the binding policy no longer derives the leaf as %s", join)
	}
	leaf, err := LeafQueue(map[string]string{labelOrganisation: "adhar", labelTeam: "platform", labelTier: "dev", "adhar.io/environment": "dev"})
	if err != nil || leaf != "adhar-platform-dev" {
		t.Errorf("leaf %q %v", leaf, err)
	}
	staging, err := LeafQueue(map[string]string{labelOrganisation: "acme", labelTeam: "pay", labelTier: "test", "adhar.io/environment": "staging"})
	if err != nil || staging != "acme-pay-test" {
		t.Errorf("an environment named staging of tier test: %q %v", staging, err)
	}
	for _, l := range []map[string]string{
		{labelOrganisation: "a", labelTeam: "b"},
		{labelOrganisation: "a", labelTeam: "b", labelTier: "staging"},
		{labelTeam: "b", labelTier: "dev"},
	} {
		if q, err := LeafQueue(l); err == nil {
			t.Errorf("%v gave leaf %q", l, q)
		}
	}
}

func obj(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var m map[string]interface{}
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestVolcanoObjectsAreReadAsVolcanoWritesThem(t *testing.T) {
	q := ParseQueue(obj(t, `{apiVersion: scheduling.volcano.sh/v1beta1, kind: Queue, metadata: {name: adhar-platform-prod, labels: {adhar.io/team: platform}},
	  spec: {parent: adhar-batch, reclaimable: false, capability: {cpu: "4", memory: 8Gi}, deserved: {cpu: 2500m, memory: 5Gi}},
	  status: {state: Open, allocated: {cpu: "1", memory: 1Gi}}}`))
	if q.Parent != RootQueue || q.State != "Open" || q.Reclaimable == nil || *q.Reclaimable || roomOf(q.Deserved).MilliCPU != 2500 {
		t.Errorf("queue %+v", q)
	}
	if f := QueueFree(q); f.MilliCPU != 3000 || f.Memory != 7<<30 {
		t.Errorf("queue free %s", f)
	}
	if f := QueueFree(ParseQueue(obj(t, `{apiVersion: v1, kind: Queue, metadata: {name: x}}`))); f != (Room{}) {
		t.Errorf("a queue without capability is unbounded, got %s", f)
	}

	pg := ParsePodGroup(obj(t, `{apiVersion: scheduling.volcano.sh/v1beta1, kind: PodGroup,
	  metadata: {name: settlement-0a1b-5f3c, namespace: prod, ownerReferences: [{apiVersion: batch.volcano.sh/v1alpha1, kind: Job, name: settlement-0a1b, uid: u-1, controller: true}]},
	  spec: {queue: adhar-platform-prod, minMember: 5, minResources: {cpu: "4500m"}},
	  status: {phase: Inqueue, conditions: [{type: Unschedulable, status: "True", reason: NotEnoughResources, message: "5/5 tasks in gang unschedulable: pod group is not ready, 5 Pending, 5 minAvailable; Pending: 5 Unschedulable; 0/1 nodes are unavailable: 1 Insufficient cpu."}]}}`))
	msg, unsched := pg.Unschedulable()
	if pg.OwnerUID != "u-1" || pg.MinMember != 5 || !pg.Unstarted() || !unsched || !MentionsCapacity(msg) {
		t.Errorf("podgroup %+v", pg)
	}
	j := ParseVolcanoJob(obj(t, `{apiVersion: batch.volcano.sh/v1alpha1, kind: Job, metadata: {name: settlement-0a1b, namespace: prod, uid: u-1},
	  spec: {minAvailable: 5, queue: adhar-platform-prod}, status: {state: {phase: Restarting}, retryCount: 1, running: 2}}`))
	if j.RetryCount != 1 || j.Phase != "Restarting" || j.Finished() {
		t.Errorf("job %+v", j)
	}
	if got, ok := PodGroupOf(j, []PodGroup{{Name: "other", Namespace: "prod"}, pg}); !ok || got.Name != pg.Name {
		t.Error("the gang was not found by its owner")
	}
	byName := pg
	byName.OwnerUID = ""
	if got, ok := PodGroupOf(j, []PodGroup{byName}); !ok || got.Name != pg.Name {
		t.Error("the gang was not found by Volcano's <job>-<uid> name")
	}
	if _, ok := PodGroupOf(VolcanoJob{Name: "settlement", Namespace: "dev", UID: "u-2"}, []PodGroup{byName}); ok {
		t.Error("another namespace's gang was taken")
	}

	worker := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelTaskSpec: "worker"}, Annotations: map[string]string{keyTaskIndex: "2"}}}
	if v, where, ok := TaskIndex(worker); !ok || v != "2" || where != "annotation" || RoleOf(worker) != "worker" {
		t.Errorf("task index %q %s %v, role %s", v, where, ok, RoleOf(worker))
	}
	worker.Labels[labelBatchRole] = "manager"
	if RoleOf(worker) != "manager" {
		t.Error("the composition's role label must win over Volcano's task name")
	}
}

// The parser is held to the configuration the stack ships, not to a sample.
func TestSchedulerConfIsReadFromTheShippedConfigMap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoStack, "packages", "core", "volcano", "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var conf string
	for _, doc := range strings.Split(string(raw), "\n---") {
		var cm corev1.ConfigMap
		if yaml.Unmarshal([]byte(doc), &cm) == nil && cm.Kind == "ConfigMap" && cm.Name == SchedulerConfigMap {
			conf = cm.Data[schedulerConfKey]
		}
	}
	c, err := ParseSchedulerConf(conf)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"enqueue", "allocate", "reclaim", "preempt"} {
		if !strings.Contains(strings.Join(c.Actions, ","), a) {
			t.Errorf("the shipped scheduler lacks %s: %v", a, c.Actions)
		}
	}
	for _, p := range []string{"gang", "capacity", "priority", "predicates"} {
		if _, ok := c.Plugins[p]; !ok {
			t.Errorf("the shipped scheduler lacks the %s plugin", p)
		}
	}
	if f, ok := c.OvercommitFactor(); ok {
		t.Logf("DEC-2 is in the shipped config: overcommit factor %.2f in tier %d", f, c.Tier["overcommit"])
	} else {
		t.Log("DEC-2 not in the shipped config yet: no overcommit plugin")
	}
	oc, _ := ParseSchedulerConf("actions: enqueue\ntiers:\n- plugins:\n  - name: overcommit\n    arguments: {overcommit-factor: 1.5}\n")
	if f, ok := oc.OvercommitFactor(); !ok || f != 1.5 {
		t.Errorf("overcommit factor %v %v", f, ok)
	}
	def, _ := ParseSchedulerConf("actions: enqueue\ntiers:\n- plugins:\n  - name: overcommit\n")
	if f, ok := def.OvercommitFactor(); !ok || f != 1.2 {
		t.Errorf("Volcano's default overcommit factor is 1.2, got %v %v", f, ok)
	}
	if _, err := ParseSchedulerConf("kind: ConfigMap"); err == nil {
		t.Error("a document that is not a scheduler conf was read")
	}
}

/* ─────────────── room ─────────────── */

func node(name string, cpu, mem string, ready bool, taint bool) corev1.Node {
	n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	n.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}}
	if taint {
		n.Spec.Taints = []corev1.Taint{{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule}}
	}
	return n
}

func pod(nodeName, cpu, mem string, phase corev1.PodPhase) corev1.Pod {
	p := corev1.Pod{Spec: corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}}}}}}
	p.Status.Phase = phase
	return p
}

func TestClusterRoomIsWhatTheSchedulersSee(t *testing.T) {
	nodes := []corev1.Node{node("a", "4", "8Gi", true, false), node("b", "8", "16Gi", true, true), node("c", "8", "16Gi", false, false)}
	init := pod("a", "100m", "64Mi", corev1.PodRunning)
	init.Spec.InitContainers = []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}}}
	pods := []corev1.Pod{pod("a", "500m", "1Gi", corev1.PodRunning), pod("a", "2", "1Gi", corev1.PodSucceeded), init,
		pod("b", "1", "1Gi", corev1.PodRunning), pod("", "1", "1Gi", corev1.PodPending)}
	total, idle := ClusterRoom(nodes, pods)
	if total.MilliCPU != 4000 || len(idle) != 1 {
		t.Fatalf("total %s idle %v: only a Ready, untainted node counts", total, idle)
	}
	// 4000 - 500 - max(100, 1000 init) = 2500; the finished and unbound pods hold nothing.
	if idle["a"].MilliCPU != 2500 || idle["a"].Memory != 8<<30-1<<30-64<<20 {
		t.Errorf("idle on a %s", idle["a"])
	}
	n, r, ok := RoomiestNode(map[string]Room{"x": {MilliCPU: 10}, "y": {MilliCPU: 30}, "z": {MilliCPU: 30}})
	if !ok || n != "y" || r.MilliCPU != 30 {
		t.Errorf("roomiest %s %s", n, r)
	}
	b, err := BallastFor(map[string]Room{"x": {MilliCPU: 900}, "y": {MilliCPU: 2000}}, "y", Room{MilliCPU: 700})
	if err != nil || b["x"].MilliCPU != 900 || b["y"].MilliCPU != 1300 {
		t.Errorf("ballast %v %v: every other node is filled, the kept one down to the room", b, err)
	}
	if _, err := BallastFor(map[string]Room{"y": {MilliCPU: 100}}, "y", Room{MilliCPU: 700}); err == nil {
		t.Error("a room larger than the node was planned")
	}
}

// A2.1's arithmetic: in the room the suite leaves, n members fit and n+1 do
// not, each by half a member — never by a rounding error.
func TestGangFitLeavesRoomForNAndNotNPlusOne(t *testing.T) {
	for _, tc := range []struct {
		idle, queue Room
		n           int
	}{
		{Room{MilliCPU: 2000, Memory: 4 << 30}, Room{}, 3},
		{Room{MilliCPU: 7999, Memory: 4 << 30}, Room{MilliCPU: 3000}, 3},
		{Room{MilliCPU: 900, Memory: 1 << 30}, Room{}, 1},
		{Room{MilliCPU: 16000, Memory: 1 << 30}, Room{MilliCPU: 22000}, 8},
	} {
		g, err := PlanGangFit(tc.idle, tc.queue, tc.n)
		if err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		fit, over := int64(g.Fits)*g.Member.MilliCPU, int64(g.Fits+1)*g.Member.MilliCPU
		if g.Fits != tc.n || fit > g.Room.MilliCPU-g.Member.MilliCPU/2 || over < g.Room.MilliCPU+g.Member.MilliCPU/2 {
			t.Errorf("%+v: room %s member %s: %d fit in %dm, %d need %dm", tc, g.Room, g.Member, g.Fits, fit, g.Fits+1, over)
		}
		if g.Room.MilliCPU > tc.idle.MilliCPU*9/10 || (tc.queue.MilliCPU > 0 && over > tc.queue.MilliCPU) {
			t.Errorf("%+v: the gang is kept out by the queue or by churn, not only by the room: %+v", tc, g)
		}
	}
	if _, err := PlanGangFit(Room{MilliCPU: 150, Memory: 1 << 30}, Room{}, 3); err == nil {
		t.Error("members below the churn floor were planned")
	}
	if _, err := PlanGangFit(Room{MilliCPU: 4000, Memory: 64 << 20}, Room{}, 3); err == nil {
		t.Error("members that cannot fit in memory were planned")
	}
}

// A2.4's arithmetic: prod fits only if Volcano takes the filler AND the dev
// run, and never asks more than prod's deserved share.
func TestReclaimNeedsTheDevRunAndStaysInProdsShare(t *testing.T) {
	for _, tc := range []struct{ idle, app, deserved Room }{
		{Room{MilliCPU: 3000, Memory: 8 << 30}, Room{MilliCPU: 1000, Memory: 1536 << 20}, Room{MilliCPU: 2500, Memory: 5 << 30}},
		{Room{MilliCPU: 12000, Memory: 8 << 30}, Room{MilliCPU: 1000, Memory: 1536 << 20}, Room{MilliCPU: 6600, Memory: 7 << 30}},
		{Room{MilliCPU: 1400, Memory: 8 << 30}, Room{MilliCPU: 500, Memory: 1 << 30}, Room{MilliCPU: 22000}},
		// A 4-vCPU runner: little idle beside a 1-CPU run.
		{Room{MilliCPU: 400, Memory: 4 << 30}, Room{MilliCPU: 1000, Memory: 1536 << 20}, Room{MilliCPU: 2500, Memory: 5 << 30}},
	} {
		p, err := PlanReclaim(tc.idle, tc.app, tc.deserved)
		if err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		prod := 2 * p.ProdMember.MilliCPU
		freeWithoutRun := p.Spare.MilliCPU + p.Filler.MilliCPU
		if prod <= freeWithoutRun || prod > freeWithoutRun+p.App.MilliCPU || prod > tc.deserved.MilliCPU {
			t.Errorf("%+v: prod %dm vs spare+filler %dm and the run %dm, deserved %s", tc, prod, freeWithoutRun, p.App.MilliCPU, tc.deserved)
		}
		if p.App.MilliCPU+p.Filler.MilliCPU+p.Spare.MilliCPU != p.Room.MilliCPU || p.Room.MilliCPU > tc.idle.MilliCPU+tc.app.MilliCPU {
			t.Errorf("%+v: the room %+v is not what the node has", tc, p)
		}
	}
	if _, err := PlanReclaim(Room{MilliCPU: 4000, Memory: 1 << 30}, Room{MilliCPU: 1000}, Room{}); err == nil {
		t.Error("a prod leaf with no deserved share was planned to reclaim")
	}
	if _, err := PlanReclaim(Room{MilliCPU: 4000, Memory: 1 << 30}, Room{MilliCPU: 1000}, Room{MilliCPU: 1200}); err == nil {
		t.Error("a run that leaves no room for a filler within prod's share was planned")
	}
}

func TestQuotaFreeIsTheTightestQuota(t *testing.T) {
	rq := func(hard, used string) corev1.ResourceQuota {
		var q corev1.ResourceQuota
		q.Status.Hard = corev1.ResourceList{"requests.cpu": resource.MustParse(hard), "limits.memory": resource.MustParse("4Gi")}
		q.Status.Used = corev1.ResourceList{"requests.cpu": resource.MustParse(used), "limits.memory": resource.MustParse("1Gi")}
		return q
	}
	if f := QuotaFree([]corev1.ResourceQuota{rq("4", "1"), rq("2", "1500m")}); f.MilliCPU != 500 || f.Memory != 3<<30 {
		t.Errorf("quota free %s", f)
	}
	if f := QuotaFree(nil); f != (Room{}) {
		t.Errorf("no quota is no limit, got %s", f)
	}
	if f := Tighter(Room{MilliCPU: 500}, Room{MilliCPU: 300, Memory: 1}); f.MilliCPU != 300 || f.Memory != 1 {
		t.Errorf("tighter %s", f)
	}
}

/* ─────────────── A2.7 ─────────────── */

func TestOnlyPodsVolcanoScheduledMayBeItsVictims(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "gang-0", UID: "1"}, Spec: corev1.PodSpec{SchedulerName: "volcano"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "sentinel", UID: "2"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "adhar-system", Name: "argocd", UID: "3"}, Spec: corev1.PodSpec{SchedulerName: "default-scheduler"}},
	}
	before := Snapshot(pods)
	if before["dev/sentinel"].Scheduler != corev1.DefaultSchedulerName {
		t.Errorf("an unnamed scheduler is kube-scheduler: %+v", before["dev/sentinel"])
	}
	ev := func(ns, name, kind, by, reason, msg string) corev1.Event {
		return corev1.Event{InvolvedObject: corev1.ObjectReference{Kind: kind, Namespace: ns, Name: name}, Source: corev1.EventSource{Component: by}, Reason: reason, Message: msg}
	}
	clean := []corev1.Event{
		ev("dev", "gang-0", "Pod", "volcano", "Evict", "Pod is evicted, because of reclaim"),
		ev("dev", "gang-0", "Pod", "volcano", "Evict", "again"),
		ev("dev", "sentinel", "Pod", "default-scheduler", "Preempted", "by a higher priority pod"),
		ev("dev", "settlement", "PodGroup", "volcano", "Evict", "a gang member was evicted"),
		ev("dev", "sentinel", "Pod", "volcano", "Scheduled", "bound"),
	}
	if bad := EvictedNotScheduled(clean, before); len(bad) > 0 {
		t.Errorf("only Volcano's own pod was evicted, got %v", bad)
	}
	dirty := append(clean, ev("dev", "sentinel", "Pod", "volcano", "Evict", "reclaim"),
		corev1.Event{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "x", Name: "unknown"}, ReportingController: "volcano-scheduler", Reason: "Preempt"})
	bad := EvictedNotScheduled(dirty, before)
	if len(bad) != 2 || !strings.HasPrefix(bad[0], "dev/sentinel (scheduled by default-scheduler") || !strings.HasPrefix(bad[1], "x/unknown (not in the snapshot") {
		t.Errorf("victims Volcano did not schedule: %v", bad)
	}
}

// The live message check (FU-11) is only worth anything with the markers the
// autoscaler itself reads.
func TestCapacityMarkersAreTheAutoscalers(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "platform", "controllers", "autoscaler", "decision.go"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)var capacityShortageMarkers = \[\]string\{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatal("decision.go no longer declares capacityShortageMarkers")
	}
	var theirs []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1) {
		theirs = append(theirs, string(m[1]))
	}
	if strings.Join(theirs, "|") != strings.Join(CapacityMarkers, "|") {
		t.Errorf("the suite's markers %v are not the autoscaler's %v", CapacityMarkers, theirs)
	}
	if MentionsCapacity("0/1 nodes are unavailable: 1 node(s) had untolerated taint") {
		t.Error("a taint is not a capacity shortage")
	}
}

/* ─────────────── A2.10 ─────────────── */

// applyPatch is the JSON patch subset ElementPatch emits, applied the way the
// API server does: every op in order, a failed test aborts the patch.
func applyPatch(t *testing.T, doc map[string]interface{}, raw []byte) bool {
	t.Helper()
	var ops []struct {
		Op, Path string
		Value    interface{}
	}
	if err := json.Unmarshal(raw, &ops); err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		parts := strings.Split(strings.TrimPrefix(op.Path, "/"), "/")
		var cur interface{} = doc
		for _, p := range parts[:len(parts)-1] {
			switch c := cur.(type) {
			case map[string]interface{}:
				cur = c[p]
			case []interface{}:
				i, _ := strconv.Atoi(p)
				cur = c[i]
			}
		}
		m, _ := cur.(map[string]interface{})
		last := parts[len(parts)-1]
		switch op.Op {
		case "test":
			if m[last] != op.Value {
				return false
			}
		case "replace", "add":
			m[last] = op.Value
		case "remove":
			delete(m, last)
		}
	}
	return true
}

func TestTheDescriptorIsRemovedThroughItsOwnElement(t *testing.T) {
	app, err := TrackingApp(map[string]string{argoTrackingID: "adhar-batch-volcano:/ConfigMap:adhar-system/adhar-batch-scheduler"})
	if err != nil || app != "adhar-batch-volcano" {
		t.Errorf("tracking app %q %v", app, err)
	}
	for _, id := range []string{"", "no-colon", "app:"} {
		if a, err := TrackingApp(map[string]string{argoTrackingID: id}); err == nil {
			t.Errorf("tracking id %q gave app %q", id, a)
		}
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(readLocalAppSet(t), &doc); err != nil {
		t.Fatal(err)
	}
	appset := &unstructured.Unstructured{Object: doc}
	if _, err := ElementForApplication(appset, "volcano"); err != nil {
		t.Error(err)
	}
	if _, err := ElementForApplication(appset, "argocd-not-an-element"); err == nil {
		t.Error("an Application that is no element was resolved")
	}
	before, _, _ := appsetElements(readLocalAppSet(t))
	patch, err := ElementPatch(appset, "volcano", true)
	if err != nil {
		t.Fatal(err)
	}
	if !applyPatch(t, appset.Object, patch) {
		t.Fatal("the patch's own test failed on the file it was made from")
	}
	raw, _ := yaml.Marshal(appset.Object)
	after, _, _ := appsetElements(raw)
	for n := range before {
		if want := before[n] || n == "volcano"; after[n] != want {
			t.Errorf("element %s enabled %v, want %v", n, after[n], want)
		}
	}
	// A list reordered between reading and patching fails the patch.
	gens := appset.Object["spec"].(map[string]interface{})["generators"].([]interface{})
	elems := gens[0].(map[string]interface{})["list"].(map[string]interface{})["elements"].([]interface{})
	elems[0], elems[len(elems)-1] = elems[len(elems)-1], elems[0]
	first, _ := ElementPatch(appset, elems[len(elems)-1].(map[string]interface{})["name"].(string), false)
	elems[0], elems[len(elems)-1] = elems[len(elems)-1], elems[0]
	if applyPatch(t, appset.Object, first) {
		t.Error("a patch made against another order switched a neighbour")
	}
	if _, err := ElementPatch(appset, "no-such", true); err == nil {
		t.Error("a patch for a missing element was made")
	}
}

/* ─────────────── run templates of Phase 2 ─────────────── */

func loadTemplateFixture(t *testing.T, name string) *RunTemplate {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "volcano", name))
	if err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseRunTemplateKinds(cm.Data)
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

func mutate(t *testing.T, tpl *RunTemplate, f func(m map[string]interface{})) *RunTemplate {
	t.Helper()
	c := *tpl
	c.Manifest = tpl.Manifest.DeepCopy()
	f(c.Manifest.Object)
	return &c
}

func TestTheVolcanoGangTemplateIsTheContract(t *testing.T) {
	tpl := loadTemplateFixture(t, "run-template-volcanojob.yaml")
	g, problems := CheckVolcanoGang(tpl, 4, "acme-payments-prod")
	if len(problems) > 0 || g.MinAvailable != 5 || g.Tasks["worker"] != 4 {
		t.Fatalf("the contract fixture: %+v %v", g, problems)
	}
	if _, p := CheckVolcanoGang(tpl, 3, "acme-payments-prod"); len(p) == 0 {
		t.Error("a gang of the wrong size passed")
	}
	if _, p := CheckVolcanoGang(tpl, 4, "acme-payments-dev"); len(p) == 0 {
		t.Error("another leaf's queue passed")
	}
	for what, f := range map[string]func(m map[string]interface{}){
		"no RestartJob": func(m map[string]interface{}) {
			pol, _, _ := unstructured.NestedSlice(m, "spec", "policies")
			_ = unstructured.SetNestedSlice(m, pol[:5], "spec", "policies")
		},
		"exit 2 retried": func(m map[string]interface{}) {
			pol, _, _ := unstructured.NestedSlice(m, "spec", "policies")
			_ = unstructured.SetNestedSlice(m, pol[1:], "spec", "policies")
		},
		"svc plugin": func(m map[string]interface{}) {
			_ = unstructured.SetNestedField(m, map[string]interface{}{"env": []interface{}{}, "svc": []interface{}{}}, "spec", "plugins")
		},
		"no CompleteJob": func(m map[string]interface{}) {
			tasks, _, _ := unstructured.NestedSlice(m, "spec", "tasks")
			delete(tasks[0].(map[string]interface{}), "policies")
			_ = unstructured.SetNestedSlice(m, tasks, "spec", "tasks")
		},
		"native class": func(m map[string]interface{}) {
			_ = unstructured.SetNestedField(m, "adhar-batch-critical", "spec", "priorityClassName")
		},
		"restart in place": func(m map[string]interface{}) {
			tasks, _, _ := unstructured.NestedSlice(m, "spec", "tasks")
			_ = unstructured.SetNestedField(tasks[1].(map[string]interface{}), "OnFailure", "template", "spec", "restartPolicy")
			_ = unstructured.SetNestedSlice(m, tasks, "spec", "tasks")
		},
		"worker runs as manager": func(m map[string]interface{}) {
			tasks, _, _ := unstructured.NestedSlice(m, "spec", "tasks")
			cs, _, _ := unstructured.NestedSlice(tasks[1].(map[string]interface{}), "template", "spec", "containers")
			cs[0].(map[string]interface{})["env"].([]interface{})[0].(map[string]interface{})["value"] = "manager"
			_ = unstructured.SetNestedSlice(tasks[1].(map[string]interface{}), cs, "template", "spec", "containers")
			_ = unstructured.SetNestedSlice(m, tasks, "spec", "tasks")
		},
	} {
		if _, p := CheckVolcanoGang(mutate(t, tpl, f), 4, "acme-payments-prod"); len(p) == 0 {
			t.Errorf("%s passed", what)
		}
	}
	if _, p := CheckVolcanoGang(loadTemplateFixture(t, "run-template-indexedjob.yaml"), 4, "x"); len(p) == 0 {
		t.Error("an IndexedJob passed as a gang")
	}
}

func TestTheNativeFallbackIsAnIndexedJobTheManagerEnds(t *testing.T) {
	tpl := loadTemplateFixture(t, "run-template-indexedjob.yaml")
	if p := CheckIndexedFallback(tpl, 4); len(p) > 0 {
		t.Fatalf("the contract fixture: %v", p)
	}
	for what, f := range map[string]func(m map[string]interface{}){
		"no success policy": func(m map[string]interface{}) { unstructured.RemoveNestedField(m, "spec", "successPolicy") },
		"NonIndexed": func(m map[string]interface{}) {
			_ = unstructured.SetNestedField(m, "NonIndexed", "spec", "completionMode")
		},
		"one short": func(m map[string]interface{}) { _ = unstructured.SetNestedField(m, int64(4), "spec", "parallelism") },
		"still Volcano": func(m map[string]interface{}) {
			_ = unstructured.SetNestedField(m, "volcano", "spec", "template", "spec", "schedulerName")
		},
		"no failure policy": func(m map[string]interface{}) { unstructured.RemoveNestedField(m, "spec", "podFailurePolicy") },
	} {
		if p := CheckIndexedFallback(mutate(t, tpl, f), 4); len(p) == 0 {
			t.Errorf("%s passed", what)
		}
	}
}

// A2.10 also reads a single-mode template: native has no Volcano in it, and on
// Volcano only the pod names it (§6.4).
func TestSingleModeNamesVolcanoOnlyInThePod(t *testing.T) {
	native := loadRunTemplate(t)
	if p := CheckNativeRun(native); len(p) > 0 {
		t.Errorf("the rendered native template: %v", p)
	}
	onVolcano := mutate(t, native, func(m map[string]interface{}) {
		_ = unstructured.SetNestedField(m, VolcanoScheduler, "spec", "template", "spec", "schedulerName")
		_ = unstructured.SetNestedField(m, ClassVolcanoCritical, "spec", "template", "spec", "priorityClassName")
		_ = unstructured.SetNestedField(m, "acme-payments-prod", "spec", "template", "metadata", "annotations", annotationQueueName)
	})
	if p := CheckVolcanoSingle(onVolcano, "acme-payments-prod"); len(p) > 0 {
		t.Errorf("single mode on Volcano: %v", p)
	}
	if p := CheckNativeRun(onVolcano); len(p) != 3 {
		t.Errorf("a Volcano template read as native: %v", p)
	}
	if p := CheckVolcanoSingle(native, "acme-payments-prod"); len(p) != 2 {
		t.Errorf("a native template read as Volcano: %v", p)
	}
	if p := CheckVolcanoSingle(onVolcano, "acme-payments-dev"); len(p) != 1 {
		t.Errorf("another leaf: %v", p)
	}
}

func TestPhaseTwoRunTemplatesAreReadByTheirKind(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "volcano", "run-template-volcanojob.yaml"))
	var cm corev1.ConfigMap
	_ = yaml.Unmarshal(raw, &cm)
	for what, data := range map[string]map[string]string{
		"kind says Job":   {"naming": "v1", "kind": "Job", "template.yaml": cm.Data["template.yaml"]},
		"unknown kind":    {"naming": "v1", "kind": "CronJob", "template.yaml": cm.Data["template.yaml"]},
		"naming v2":       {"naming": "v2", "kind": "VolcanoJob", "template.yaml": cm.Data["template.yaml"]},
		"named":           {"naming": "v1", "kind": "VolcanoJob", "template.yaml": "apiVersion: batch.volcano.sh/v1alpha1\nkind: Job\nmetadata: {name: x}\n"},
		"indexed volcano": {"naming": "v1", "kind": "IndexedJob", "template.yaml": cm.Data["template.yaml"]},
	} {
		if _, err := ParseRunTemplateKinds(data); err == nil {
			t.Errorf("%s was read", what)
		}
	}
	tpl := loadTemplateFixture(t, "run-template-volcanojob.yaml")
	if n, ok := intAt(tpl.Manifest.Object, "spec", "minAvailable"); !ok || n != 5 {
		t.Error("integers must stay integers (int64) through the template")
	}
	if specs := podSpecsOf(tpl); len(specs) != 2 {
		t.Errorf("%d task pod specs", len(specs))
	}
}

/* ─────────────── A2.2 ─────────────── */

func ledgerOf(rows ...string) Ledger {
	l, err := ParseLedger(strings.Join(rows, "\n"))
	if err != nil {
		panic(err)
	}
	return l
}

func TestARestartRerunsOnlyTheUnfinishedPartitions(t *testing.T) {
	good := ledgerOf(
		"E|1|10|FAILED|FAILED|0", "E|1|11|COMPLETED|COMPLETED|0",
		"S|1|10|COMPLETED|manager|300", "S|1|10|COMPLETED|work:partition0|100", "S|1|10|COMPLETED|work:partition1|100", "S|1|10|FAILED|work:partition2|40",
		"S|1|11|COMPLETED|manager|60", "S|1|11|COMPLETED|work:partition2|60",
	)
	if err := good.CheckPartitioned(300, 3); err != nil {
		t.Fatal(err)
	}
	r := good.Partitions()
	if strings.Join(r.Unfinished, ",") != "work:partition2" || strings.Join(r.Resumed, ",") != "work:partition2" || len(r.Reran) > 0 {
		t.Errorf("report %+v", r)
	}
	for what, l := range map[string]Ledger{
		"a completed partition ran again": ledgerOf("E|1|10|FAILED|FAILED|0", "E|1|11|COMPLETED|COMPLETED|0",
			"S|1|10|COMPLETED|work:partition0|100", "S|1|10|FAILED|work:partition1|0",
			"S|1|11|COMPLETED|work:partition0|100", "S|1|11|COMPLETED|work:partition1|100"),
		"a partition never completed": ledgerOf("E|1|11|COMPLETED|COMPLETED|0",
			"S|1|11|COMPLETED|work:partition0|150", "S|1|11|FAILED|work:partition1|150"),
		"too few partitions": ledgerOf("E|1|11|COMPLETED|COMPLETED|0", "S|1|11|COMPLETED|work:partition0|300"),
		"not partitioned":    ledgerOf("E|1|11|COMPLETED|COMPLETED|0", "S|1|11|COMPLETED|work|300"),
	} {
		if err := l.CheckPartitioned(300, 2); err == nil {
			t.Errorf("%s passed", what)
		}
	}
}

/* ─────────────── the canary's own workloads ─────────────── */

// A synthetic gang must be admitted as a team's run is, or a scenario would
// fail on admission and read as a scheduling result.
func TestSyntheticGangsLookLikeATeamsRun(t *testing.T) {
	g := SyntheticGang(GangSpec{Name: "e2e-fit-x", Namespace: "dev", Queue: "adhar-platform-dev", PriorityClass: ClassVolcano,
		Members: 4, Member: Room{MilliCPU: 571, Memory: memberMemory}, Labels: map[string]string{labelWorkload: "batch"}})
	tpl := &RunTemplate{Kind: "VolcanoJob", Manifest: g}
	m := g.Object
	if n, _ := intAt(m, "spec", "minAvailable"); n != 4 || str(m, "spec", "queue") != "adhar-platform-dev" || g.GetLabels()[labelWorkload] != "batch" {
		t.Errorf("gang %v", m["spec"])
	}
	if _, ok := m["spec"].(map[string]interface{})["plugins"]; ok {
		t.Error("a synthetic gang needs no plugin, and only env is allowed")
	}
	if ttl, ok := intAt(m, "spec", "ttlSecondsAfterFinished"); !ok || ttl <= 0 {
		t.Error("no TTL")
	}
	for _, spec := range podSpecsOf(tpl) {
		if str(spec, "restartPolicy") != "Never" || str(spec, "priorityClassName") != ClassVolcano || str(spec, "schedulerName") != VolcanoScheduler {
			t.Errorf("pod spec %v", spec)
		}
		if d, ok := intAt(spec, "activeDeadlineSeconds"); !ok || d <= 0 {
			t.Error("no deadline")
		}
		if _, ok := spec["volumes"]; ok {
			t.Error("volumes")
		}
		cs, _, _ := unstructured.NestedSlice(spec, "containers")
		req, _, _ := unstructured.NestedStringMap(cs[0].(map[string]interface{}), "resources", "requests")
		lim, _, _ := unstructured.NestedStringMap(cs[0].(map[string]interface{}), "resources", "limits")
		if req["memory"] != lim["memory"] || req["cpu"] != lim["cpu"] || req["cpu"] != "571m" || req["memory"] != "32Mi" {
			t.Errorf("requests %v limits %v: memory and CPU limits equal the requests", req, lim)
		}
	}
	b := Ballast("ballast-0", "batch-e2e-x", "kind-control-plane", Room{MilliCPU: 1300})
	if b.Spec.NodeName != "kind-control-plane" || b.Spec.SchedulerName != "" || b.Spec.PriorityClassName != "" ||
		b.Spec.Containers[0].Resources.Requests.Cpu().MilliValue() != 1300 {
		t.Errorf("ballast %+v", b.Spec)
	}
	s := Sentinel("sentinel", "dev", "adhar-batch", "pg-1")
	if s.Spec.NodeName != "" || s.Spec.PriorityClassName != "adhar-batch" || s.Annotations[annotationGroupName] != "pg-1" ||
		s.Spec.Containers[0].Resources.Limits.Memory().Cmp(*s.Spec.Containers[0].Resources.Requests.Memory()) != 0 {
		t.Errorf("sentinel %+v", s)
	}
	if b.Spec.Containers[0].Resources.Requests.Memory().Value() != 0 {
		t.Error("the sentinel changed the ballast it was built from")
	}
}

/* ─────────────── A2.9: wired into make and CI ─────────────── */

// The canary only counts if something runs it: `make e2e-batch-volcano` opts
// both suites in, and the CI job runs that target with a budget above the
// test's own, so a stuck run reports instead of being killed silently.
func TestTheVolcanoCanaryIsWiredIntoMakeAndCI(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	target := regexp.MustCompile(`(?ms)^e2e-batch-volcano:.*?\n((?:\t.*\n)+)`).FindSubmatch(mk)
	if target == nil {
		t.Fatal("no e2e-batch-volcano target in the Makefile")
	}
	recipe := string(target[1])
	for _, want := range []string{"ADHAR_E2E_BATCH=1", "ADHAR_E2E_VOLCANO=1", "--tags=e2e", "./tests/e2e/batch/..."} {
		if !strings.Contains(recipe, want) {
			t.Errorf("e2e-batch-volcano does not pass %s", want)
		}
	}
	m := regexp.MustCompile(`-timeout (\d+)m`).FindStringSubmatch(recipe)
	if m == nil {
		t.Fatal("e2e-batch-volcano sets no go test -timeout")
	}
	testMinutes, _ := strconv.Atoi(m[1])

	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "e2e.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Needs          interface{}       `json:"needs"`
			If             string            `json:"if"`
			TimeoutMinutes int               `json:"timeout-minutes"`
			Env            map[string]string `json:"env"`
			Steps          []struct {
				Run string `json:"run"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	job, ok := wf.Jobs["batch-volcano"]
	if !ok {
		t.Fatal("no batch-volcano job in .github/workflows/e2e.yaml")
	}
	runs := false
	for _, s := range job.Steps {
		runs = runs || strings.Contains(s.Run, "make e2e-batch-volcano")
	}
	if !runs {
		t.Error("the batch-volcano job does not run make e2e-batch-volcano")
	}
	if job.TimeoutMinutes <= testMinutes || job.TimeoutMinutes > 360 {
		t.Errorf("job timeout %dm must exceed go test's %dm and fit the hosted runner's 360", job.TimeoutMinutes, testMinutes)
	}
	needs, _ := json.Marshal(job.Needs)
	if !strings.Contains(string(needs), "volcano-rehearsal") || !strings.Contains(job.If, "volcano-changes") {
		t.Errorf("batch-volcano needs %s, if %q: it must follow the rehearsal and the path filter", needs, job.If)
	}
	rehearsal, ok := wf.Jobs["volcano-rehearsal"]
	if !ok || len(rehearsal.Steps) == 0 || !strings.Contains(rehearsal.Steps[len(rehearsal.Steps)-1].Run, "make e2e-volcano-rehearsal") {
		t.Error("no volcano-rehearsal job running make e2e-volcano-rehearsal")
	}
	if rt := regexp.MustCompile(`(?ms)^e2e-volcano-rehearsal:.*?\n((?:\t.*\n)+)`).FindSubmatch(mk); rt == nil ||
		!strings.Contains(string(rt[1]), "TestVolcanoComponentsRunOnTheirNarrowedRBAC") || !strings.Contains(string(rt[1]), "VOLCANO_BIN_DIR=") {
		t.Error("e2e-volcano-rehearsal does not run the rehearsal with VOLCANO_BIN_DIR set (it would skip)")
	}
	// The version the rehearsal builds is the package's.
	if !regexp.MustCompile(`(?m)^VOLCANO_VERSION \?= .*core/volcano/adhar-package\.yaml`).Match(mk) {
		t.Error("VOLCANO_VERSION is not read from core/volcano's contract")
	}
}
