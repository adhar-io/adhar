package adharplatform

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// A CI run must be able to start on a reasonably loaded cluster.
//
// Every TaskRun that mounts a run's ReadWriteOnce workspace is REQUIRED to
// share a node with the run's Tekton affinity assistant, which the scheduler
// places by its own 50m/100Mi request and a preference for nodes without
// another assistant — not by room for the run. On DigitalOcean (2026-11-01) a
// clone pod waited 38 minutes on a full workers-1 while other nodes had room;
// on AWS (2026-10-31) a build sat ExceededNodeResources for three hours.
// application/tekton/manifests/affinity-assistant-relocator.yaml moves such
// an assistant to the node with the most free requests; these tests drive its
// script through a stub kubectl, scenario by scenario, and pin the manifest.

const relocatorManifest = "application/tekton/manifests/affinity-assistant-relocator.yaml"

func relocatorDocs(t *testing.T) []k8sDoc { t.Helper(); return docsOf(t, relocatorManifest) }

func relocatorScript(t *testing.T) string {
	t.Helper()
	cm := findDoc(t, relocatorDocs(t), "ConfigMap", "tekton-affinity-relocator")
	s, _ := sgDig(cm, "data", "relocate.py").(string)
	if s == "" {
		t.Fatal("the relocator ConfigMap carries no relocate.py")
	}
	return s
}

// ── fixtures: a cluster as the API returns it ──────────────────────────────

const (
	relNS   = "adhar-system"
	relRun  = "app-ci-gateway-service-vvzcc"
	relAA   = "affinity-assistant-3f2c1a9b7e"
	relPVC  = "pvc-0d2e6b5a1c"
	relPV   = "pvc-sim-1"
	relUID  = "e2ca169f-b7b9-4968-a97e-43b63b4d2767"
	relNow  = "2026-11-01T16:16:00Z"
	relZone = "topology.kubernetes.io/zone"
)

var relNowT, _ = time.Parse(time.RFC3339, relNow)

func ago(d time.Duration) string { return relNowT.Add(-d).UTC().Format(time.RFC3339) }

type obj = map[string]any

type relNode struct {
	name, zone, cpu, mem string
	taints               []obj
	cordoned, notReady   bool
	labels               map[string]string
}

func (n relNode) json() obj {
	labels := obj{"kubernetes.io/hostname": n.name, relZone: n.zone}
	for k, v := range n.labels {
		labels[k] = v
	}
	ready := "True"
	if n.notReady {
		ready = "False"
	}
	spec := obj{}
	if n.taints != nil {
		spec["taints"] = n.taints
	}
	if n.cordoned {
		spec["unschedulable"] = true
	}
	return obj{
		"metadata": obj{"name": n.name, "labels": labels},
		"spec":     spec,
		"status": obj{
			"allocatable": obj{"cpu": n.cpu, "memory": n.mem, "pods": "110"},
			"conditions":  []obj{{"type": "Ready", "status": ready}},
		},
	}
}

// busy is a pod already holding requests on a node.
func busy(name, node, cpu, mem string) obj {
	return obj{
		"metadata": obj{"name": name, "namespace": "default"},
		"spec": obj{"nodeName": node, "containers": []obj{{
			"name": "c", "resources": obj{"requests": obj{"cpu": cpu, "memory": mem}}}}},
		"status": obj{"phase": "Running"},
	}
}

func assistantPod(node string) obj {
	p := obj{
		"metadata": obj{
			"name": relAA + "-0", "namespace": relNS, "uid": relUID,
			"labels": obj{
				"tekton.dev/pipelineRun":      relRun,
				"app.kubernetes.io/instance":  relAA,
				"app.kubernetes.io/component": "affinity-assistant",
			},
			"ownerReferences": []obj{{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": relAA, "uid": "sts-uid"}},
		},
		"spec": obj{
			"containers": []obj{{"name": "affinity-assistant", "resources": obj{
				"requests": obj{"cpu": "50m", "memory": "100Mi"}, "limits": obj{"cpu": "50m", "memory": "100Mi"}}}},
			"volumes": []obj{{"name": "workspace-0", "persistentVolumeClaim": obj{"claimName": relPVC}}},
			"affinity": obj{"podAntiAffinity": obj{"preferredDuringSchedulingIgnoredDuringExecution": []obj{{
				"weight": 100, "podAffinityTerm": obj{"topologyKey": "kubernetes.io/hostname",
					"labelSelector": obj{"matchLabels": obj{"app.kubernetes.io/component": "affinity-assistant"}}}}}}},
		},
		"status": obj{"phase": "Running"},
	}
	if node != "" {
		p["spec"].(obj)["nodeName"] = node
	} else {
		p["status"] = obj{"phase": "Pending", "conditions": []obj{{
			"type": "PodScheduled", "status": "False", "reason": "Unschedulable", "lastTransitionTime": ago(90 * time.Second),
			"message": "0/5 nodes are available: 2 Insufficient memory, 3 node(s) didn't match Pod's node affinity/selector."}}}
	}
	return p
}

// taskPod is a TaskRun pod pinned to the assistant, as Tekton's affinity
// transformer builds it. waited < 0 means it is placed on node.
func taskPod(task, node string, mem string, waited time.Duration, message string) obj {
	p := obj{
		"metadata": obj{
			"name": relRun + "-" + task + "-pod", "namespace": relNS,
			"labels":      obj{"tekton.dev/pipelineRun": relRun, "tekton.dev/pipelineTask": task},
			"annotations": obj{"pipeline.tekton.dev/affinity-assistant": relAA},
		},
		"spec": obj{
			"affinity": obj{"podAffinity": obj{"requiredDuringSchedulingIgnoredDuringExecution": []obj{{
				"topologyKey": "kubernetes.io/hostname",
				"labelSelector": obj{"matchLabels": obj{
					"app.kubernetes.io/instance": relAA, "app.kubernetes.io/component": "affinity-assistant"}}}}}},
			"initContainers": []obj{{"name": "prepare", "resources": obj{"requests": obj{"cpu": "20m", "memory": "64Mi"}}}},
			"containers":     []obj{{"name": "step-" + task, "resources": obj{"requests": obj{"cpu": "100m", "memory": mem}}}},
			"volumes":        []obj{{"name": "ws", "persistentVolumeClaim": obj{"claimName": relPVC}}},
		},
	}
	if node != "" {
		p["spec"].(obj)["nodeName"] = node
		p["status"] = obj{"phase": "Running"}
		return p
	}
	if message == "" {
		message = "0/5 nodes are available: 1 Insufficient memory, 1 node(s) didn't match PersistentVolume's node affinity, " +
			"1 node(s) had untolerated taint(s), 2 node(s) didn't match pod affinity rules."
	}
	p["status"] = obj{"phase": "Pending", "conditions": []obj{{
		"type": "PodScheduled", "status": "False", "reason": "Unschedulable",
		"lastTransitionTime": ago(waited), "message": message}}}
	return p
}

func assistantSTS(annotations obj, nodeAffinity obj) obj {
	tmplAffinity := obj{"podAntiAffinity": obj{"preferredDuringSchedulingIgnoredDuringExecution": []obj{{
		"weight": 100, "podAffinityTerm": obj{"topologyKey": "kubernetes.io/hostname",
			"labelSelector": obj{"matchLabels": obj{"app.kubernetes.io/component": "affinity-assistant"}}}}}}}
	if nodeAffinity != nil {
		tmplAffinity["nodeAffinity"] = nodeAffinity
	}
	md := obj{
		"name": relAA, "namespace": relNS, "generation": 1,
		"labels": obj{"tekton.dev/pipelineRun": relRun, "app.kubernetes.io/instance": relAA,
			"app.kubernetes.io/component": "affinity-assistant"},
		"ownerReferences": []obj{{"apiVersion": "tekton.dev/v1", "kind": "PipelineRun", "name": relRun,
			"uid": "11111111-2222-3333-4444-555555555555", "controller": true}},
	}
	if annotations != nil {
		md["annotations"] = annotations
	}
	return obj{"metadata": md, "spec": obj{"template": obj{"spec": obj{"affinity": tmplAffinity}}},
		"status": obj{"observedGeneration": 1}}
}

func zonalPV(zone string) obj {
	return obj{"metadata": obj{"name": relPV}, "spec": obj{
		"csi": obj{"driver": "ebs.csi.aws.com", "volumeHandle": "vol-1"},
		"nodeAffinity": obj{"required": obj{"nodeSelectorTerms": []obj{{"matchExpressions": []obj{{
			"key": relZone, "operator": "In", "values": []string{zone}}}}}}}}}
}

// relCluster is the DigitalOcean shape: the assistant on a nearly full
// worker-1; roomier workers in its volume's zone; one roomy node elsewhere.
type relCluster struct {
	nodes   []relNode
	busy    []obj
	runPods []obj
	sts     obj
	pv      obj
	aaPod   obj // what `get pod <assistant>` returns after the patch
}

func defaultRelCluster() *relCluster {
	aa := assistantPod("worker-1")
	return &relCluster{
		nodes: []relNode{
			{name: "cp-1", zone: "zone-a", cpu: "4", mem: "8Gi",
				taints: []obj{{"key": "node-role.kubernetes.io/control-plane", "effect": "NoSchedule"}}},
			{name: "worker-1", zone: "zone-a", cpu: "4", mem: "8Gi"},
			{name: "worker-2", zone: "zone-a", cpu: "4", mem: "8Gi"},
			{name: "worker-3", zone: "zone-a", cpu: "4", mem: "8Gi"},
			{name: "worker-4", zone: "zone-b", cpu: "4", mem: "8Gi"},
			{name: "worker-5", zone: "zone-a", cpu: "4", mem: "8Gi", cordoned: true},
			{name: "worker-6", zone: "zone-a", cpu: "4", mem: "8Gi", notReady: true},
		},
		busy: []obj{
			busy("filler-1", "worker-1", "500m", "7850Mi"),
			busy("filler-2", "worker-2", "500m", "5Gi"),
			busy("filler-3", "worker-3", "500m", "2Gi"),
		},
		runPods: []obj{aa, taskPod("build", "", "1Gi", 2*time.Minute, "")},
		sts:     assistantSTS(nil, nil),
		pv:      zonalPV("zone-a"),
		aaPod:   aa,
	}
}

type relCall struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

func (c relCall) verb() string { return c.Args[0] }

// relStubKubectl serves the fixtures and records every call. A collection is
// served only as the API server pages it — `get --raw <path>?limit=N`, with a
// continue token while items remain — so a script that reads a whole list in
// one call (`get pods -A -o json`) gets nothing. Its interpreter line is filled
// in by runRelocator.
const relStubKubectl = `import json, os, sys
from urllib.parse import parse_qs, urlsplit
d = os.environ["FIXTURES"]
args = sys.argv[1:]
stdin = sys.stdin.read() if "-f" in args else ""
with open(os.path.join(d, "calls.jsonl"), "a") as f:
    f.write(json.dumps({"args": args, "stdin": stdin}) + "\n")
fail = os.environ.get("STUB_FAIL", "")
if fail and " ".join(args).startswith(fail):
    sys.stderr.write(os.environ.get("STUB_FAIL_MSG", "Error from server (Forbidden): refused") + "\n")
    sys.exit(1)
def unexpected():
    sys.stderr.write("unexpected kubectl call\n")
    sys.exit(2)
def serve(name):
    p = os.path.join(d, name + ".json")
    if not os.path.exists(p):
        sys.stderr.write('Error from server (NotFound): "%s" not found\n' % name)
        sys.exit(1)
    sys.stdout.write(open(p).read())
LISTS = {"/api/v1/pods": "all-pods", "/api/v1/nodes": "nodes", "/apis/storage.k8s.io/v1/csinodes": "csinodes",
         "/api/v1/namespaces/adhar-system/pods": "run-pods"}
if args[0] == "get" and args[1] == "--raw":
    u = urlsplit(args[2])
    q = parse_qs(u.query)
    if u.path not in LISTS or "limit" not in q:
        unexpected()
    items = json.load(open(os.path.join(d, LISTS[u.path] + ".json")))["items"]
    start, limit = int(q.get("continue", ["0"])[0]), int(q["limit"][0])
    md = {"continue": str(start + limit)} if start + limit < len(items) else {}
    sys.stdout.write(json.dumps({"kind": "List", "metadata": md, "items": items[start:start + limit]}))
elif args[0] == "get" and args[1] not in ("pods", "nodes", "csinodes"):
    serve(args[1] + "-" + args[2])
elif args[0] in ("patch", "delete", "create", "annotate"):
    sys.exit(0)
else:
    unexpected()
`

var (
	relPythonOnce sync.Once
	relPythonPath string
)

// relPython is the python3 interpreter itself (not a version-manager shim, which
// cannot be a #! interpreter), or the test is skipped.
func relPython(t *testing.T) string {
	t.Helper()
	relPythonOnce.Do(func() {
		if py, err := exec.LookPath("python3"); err == nil {
			if out, err := exec.Command(py, "-I", "-S", "-c", "import sys; print(sys.executable)").Output(); err == nil {
				relPythonPath = strings.TrimSpace(string(out))
			}
		}
	})
	if relPythonPath == "" {
		t.Skip("python3 is not installed")
	}
	return relPythonPath
}

// runRelocator runs one pass of the shipped script against the cluster.
func runRelocator(t *testing.T, c *relCluster, env ...string) ([]relCall, string) {
	t.Helper()
	return runRelocatorArgs(t, c, []string{"--once"}, env...)
}

// runRelocatorPasses runs the loop for n passes with no pause between them, as
// one process, so whatever it remembers from one pass to the next is in play.
func runRelocatorPasses(t *testing.T, c *relCluster, n int, env ...string) ([]relCall, string) {
	t.Helper()
	return runRelocatorArgs(t, c, nil, append([]string{fmt.Sprintf("PASSES=%d", n), "INTERVAL_SECONDS=0"}, env...)...)
}

func runRelocatorArgs(t *testing.T, c *relCluster, args []string, env ...string) ([]relCall, string) {
	t.Helper()
	py := relPython(t)
	dir := t.TempDir()
	write := func(name string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var nodes []obj
	for _, n := range c.nodes {
		nodes = append(nodes, n.json())
	}
	write("nodes", obj{"items": nodes})
	write("csinodes", obj{"items": []obj{}})
	write("run-pods", obj{"items": c.runPods})
	all := append(append([]obj{}, c.busy...), c.runPods...)
	write("all-pods", obj{"items": all})
	write("pvc-"+relPVC, obj{"metadata": obj{"name": relPVC}, "spec": obj{"volumeName": relPV}})
	if c.pv != nil {
		write("pv-"+relPV, c.pv)
	}
	write("statefulset-"+relAA, c.sts)
	if c.aaPod != nil {
		write("pod-"+relAA+"-0", c.aaPod)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// -IS: stdlib only, no site-packages to import on every one of the
	// script's kubectl calls.
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte("#!"+py+" -IS\n"+relStubKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "relocate.py")
	if err := os.WriteFile(script, []byte(relocatorScript(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(py, append([]string{"-I", "-S", script}, args...)...)
	cmd.Env = append([]string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FIXTURES=" + dir, "NOW=" + relNow, "SETTLE_SECONDS=0",
	}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("relocator pass failed (%v):\n%s", err, out)
	}
	var calls []relCall
	f, err := os.Open(filepath.Join(dir, "calls.jsonl"))
	if err == nil {
		defer f.Close()
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 1<<20), 1<<20)
		for s.Scan() {
			var c relCall
			if err := json.Unmarshal(s.Bytes(), &c); err != nil {
				t.Fatal(err)
			}
			calls = append(calls, c)
		}
	}
	return calls, string(out)
}

func writes(calls []relCall) []relCall {
	var out []relCall
	for _, c := range calls {
		if c.verb() != "get" {
			out = append(out, c)
		}
	}
	return out
}

// templatePatch decodes the strategic merge patch the script sent for the
// assistant's StatefulSet template, or nil.
func templatePatch(t *testing.T, calls []relCall) obj {
	t.Helper()
	for _, c := range calls {
		if c.verb() == "patch" && c.Args[1] == "statefulset" && c.Args[2] == relAA && containsArg(c.Args, "strategic") {
			var p obj
			if err := json.Unmarshal([]byte(c.Args[len(c.Args)-1]), &p); err != nil {
				t.Fatal(err)
			}
			return p
		}
	}
	return nil
}

func containsArg(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

// pinnedNodes returns the node names a patch pins the assistant to.
func pinnedNodes(t *testing.T, patch obj) []string {
	t.Helper()
	terms, _ := sgDig(patch, "spec", "template", "spec", "affinity", "nodeAffinity",
		"requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms").([]any)
	var names []string
	for _, term := range terms {
		fields, _ := sgDig(term, "matchFields").([]any)
		for _, f := range fields {
			vals, _ := sgDig(f, "values").([]any)
			if sgDig(f, "key") == "metadata.name" && len(vals) != 1 {
				t.Fatalf("a node field selector carries %d values; the API server accepts exactly one", len(vals))
			}
			for _, v := range vals {
				names = append(names, v.(string))
			}
		}
	}
	return names
}

func deletedWithUIDPrecondition(calls []relCall) bool {
	for _, c := range calls {
		if c.verb() == "delete" && containsArg(c.Args, "--raw") &&
			strings.HasSuffix(c.Args[2], "/namespaces/"+relNS+"/pods/"+relAA+"-0") &&
			strings.Contains(c.Stdin, `"uid": "`+relUID+`"`) {
			return true
		}
	}
	return false
}

func eventReason(calls []relCall) string {
	for _, c := range calls {
		if c.verb() == "create" {
			var ev obj
			_ = json.Unmarshal([]byte(c.Stdin), &ev)
			if ev["kind"] == "Event" && sgDig(ev, "involvedObject", "kind") == "PipelineRun" && sgDig(ev, "involvedObject", "name") == relRun {
				return fmt.Sprint(ev["reason"])
			}
		}
	}
	return ""
}

// ── behaviour ──────────────────────────────────────────────────────────────

func TestAffinityRelocatorMovesAStuckRunToTheNodesWithRoom(t *testing.T) {
	calls, out := runRelocator(t, defaultRelCluster())
	patch := templatePatch(t, calls)
	if patch == nil {
		t.Fatalf("the assistant was not moved although its build waits on a full node and worker-2/3 have room:\n%s", out)
	}
	// worker-3 (2Gi used) before worker-2 (5Gi used); never the full current
	// node, the tainted control plane, the other zone, a cordoned or NotReady node.
	if got := pinnedNodes(t, patch); strings.Join(got, ",") != "worker-3,worker-2" {
		t.Fatalf("pinned to %v; want the zone-a workers with room, most free memory first: [worker-3 worker-2]\n%s", got, out)
	}
	ann, _ := sgDig(patch, "metadata", "annotations").(map[string]any)
	if ann["adhar.io/relocations"] != "1" || ann["adhar.io/relocated-from"] != "worker-1" || ann["adhar.io/relocated-at"] != relNow {
		t.Fatalf("the move is not recorded on the StatefulSet: %v", ann)
	}
	if _, ok := ann["adhar.io/relocation-original-node-affinity"]; !ok {
		t.Fatal("the original node affinity is not kept, so it cannot be restored")
	}
	if !deletedWithUIDPrecondition(calls) {
		t.Fatalf("the old assistant pod is not replaced (with a UID precondition):\n%s", out)
	}
	if r := eventReason(calls); r != "AffinityAssistantMoved" {
		t.Fatalf("no AffinityAssistantMoved event on the PipelineRun (got %q)", r)
	}
}

// The run's own requirements and the volume's topology bound where it may go.
func TestAffinityRelocatorKeepsTheAssistantsOwnConstraints(t *testing.T) {
	c := defaultRelCluster()
	original := obj{"requiredDuringSchedulingIgnoredDuringExecution": obj{"nodeSelectorTerms": []obj{{
		"matchExpressions": []obj{{"key": "pool", "operator": "In", "values": []string{"ci"}}}}}}}
	c.sts = assistantSTS(nil, original)
	c.nodes[3].labels = map[string]string{"pool": "ci"} // worker-3 only
	calls, out := runRelocator(t, c)
	patch := templatePatch(t, calls)
	if patch == nil {
		t.Fatalf("not moved:\n%s", out)
	}
	if got := pinnedNodes(t, patch); strings.Join(got, ",") != "worker-3" {
		t.Fatalf("pinned to %v; worker-2 is not in the run's own node pool", got)
	}
	terms, _ := sgDig(patch, "spec", "template", "spec", "affinity", "nodeAffinity",
		"requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms").([]any)
	if len(terms) != 1 || sgDig(terms[0], "matchExpressions") == nil {
		t.Fatalf("the run's own required term was dropped from the pinned affinity: %v", terms)
	}
	stored, _ := sgDig(patch, "metadata", "annotations", "adhar.io/relocation-original-node-affinity").(string)
	if !strings.Contains(stored, `"pool"`) {
		t.Fatalf("the original affinity kept for a restore is %q", stored)
	}
}

func TestAffinityRelocatorLeavesARunAloneWhenItCannotHelp(t *testing.T) {
	cases := []struct {
		name  string
		setup func(c *relCluster)
		env   []string
		note  string // the reason it records, "" for none
	}{
		{name: "a pod of the run still uses the workspace on the assistant's node", setup: func(c *relCluster) {
			c.runPods = append(c.runPods, taskPod("sbom", "worker-1", "384Mi", -1, ""))
		}},
		{name: "the pod has not waited out the grace period", setup: func(c *relCluster) {
			c.runPods[1] = taskPod("build", "", "1Gi", 30*time.Second, "")
		}},
		{name: "the pod is only waiting for its assistant to be placed", setup: func(c *relCluster) {
			c.runPods[1] = taskPod("build", "", "1Gi", 5*time.Minute, "0/5 nodes are available: 5 node(s) didn't match pod affinity rules.")
		}},
		{name: "it was moved too recently", setup: func(c *relCluster) {
			c.sts = assistantSTS(obj{"adhar.io/relocations": "1", "adhar.io/relocated-at": ago(time.Minute),
				"adhar.io/relocated-to": "worker-1"}, nil)
		}},
		{name: "the StatefulSet is not one Tekton made for an assistant", setup: func(c *relCluster) {
			md := c.sts["metadata"].(obj)
			md["ownerReferences"] = []obj{}
		}},
		{name: "it has been moved as often as it may be", setup: func(c *relCluster) {
			c.sts = assistantSTS(obj{"adhar.io/relocations": "3", "adhar.io/relocated-at": ago(time.Hour)}, nil)
		}, note: "already moved 3 times"},
		{name: "no node in the volume's zone has room", setup: func(c *relCluster) {
			c.busy = append(c.busy, busy("filler-2b", "worker-2", "1", "2Gi"), busy("filler-3b", "worker-3", "1", "5Gi"))
		}, note: "no other node has room"},
		{name: "the workspace is node-local", setup: func(c *relCluster) {
			c.pv = obj{"metadata": obj{"name": relPV}, "spec": obj{"nodeAffinity": obj{"required": obj{"nodeSelectorTerms": []obj{{
				"matchExpressions": []obj{{"key": "kubernetes.io/hostname", "operator": "In", "values": []string{"worker-1"}}}}}}}}}
		}, note: "cannot be attached to any other node"},
		{name: "the workspace volume cannot be read", setup: func(c *relCluster) { c.pv = nil }},
		{name: "the pod list is refused", setup: func(c *relCluster) {}, env: []string{"STUB_FAIL=get --raw /api/v1/namespaces/" + relNS + "/pods"}},
		{name: "the cluster-wide pod list is refused", setup: func(c *relCluster) {}, env: []string{"STUB_FAIL=get --raw /api/v1/pods"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := defaultRelCluster()
			tc.setup(c)
			calls, out := runRelocator(t, c, tc.env...)
			if p := templatePatch(t, calls); p != nil {
				t.Fatalf("moved the assistant (%v):\n%s", pinnedNodes(t, p), out)
			}
			if deletedWithUIDPrecondition(calls) {
				t.Fatalf("deleted the assistant pod:\n%s", out)
			}
			noted := ""
			for _, w := range writes(calls) {
				if w.verb() == "patch" && containsArg(w.Args, "merge") {
					noted = w.Args[len(w.Args)-1]
				}
			}
			switch {
			case tc.note == "" && len(writes(calls)) != 0:
				t.Fatalf("expected no writes at all, got %v:\n%s", writes(calls), out)
			case tc.note != "" && !strings.Contains(noted, tc.note):
				t.Fatalf("expected the reason %q recorded on the StatefulSet, got %q:\n%s", tc.note, noted, out)
			case tc.note != "" && eventReason(calls) != "AffinityAssistantNotMoved":
				t.Fatalf("the reason is not an Event on the PipelineRun:\n%s", out)
			}
		})
	}
}

// A reason already recorded is not recorded again on every pass.
func TestAffinityRelocatorSaysWhyOnce(t *testing.T) {
	c := defaultRelCluster()
	c.busy = append(c.busy, busy("filler-2b", "worker-2", "1", "2Gi"), busy("filler-3b", "worker-3", "1", "5Gi"))
	calls, _ := runRelocator(t, c)
	var note string
	for _, w := range writes(calls) {
		if w.verb() == "patch" {
			var p obj
			_ = json.Unmarshal([]byte(w.Args[len(w.Args)-1]), &p)
			note, _ = sgDig(p, "metadata", "annotations", "adhar.io/relocation-note").(string)
		}
	}
	if note == "" {
		t.Fatal("no reason recorded")
	}
	c.sts = assistantSTS(obj{"adhar.io/relocation-note": note}, nil)
	if calls, out := runRelocator(t, c); len(writes(calls)) != 0 {
		t.Fatalf("the same reason was written again: %v\n%s", writes(calls), out)
	}
}

// An assistant this loop pinned that then cannot be placed is retargeted away
// from those nodes, and when nothing else takes it, handed back to the scheduler
// rather than left pinned where it never fits.
func TestAffinityRelocatorRecoversAnAssistantItStranded(t *testing.T) {
	pinnedTo := func(names ...string) obj {
		var terms []obj
		for _, n := range names {
			terms = append(terms, obj{"matchFields": []obj{{"key": "metadata.name", "operator": "In", "values": []string{n}}}})
		}
		return obj{"requiredDuringSchedulingIgnoredDuringExecution": obj{"nodeSelectorTerms": terms}}
	}
	moved := obj{"adhar.io/relocations": "1", "adhar.io/relocated-at": ago(2 * time.Minute),
		"adhar.io/relocated-from": "worker-1", "adhar.io/relocated-to": "worker-3",
		"adhar.io/relocation-original-node-affinity": ""}

	t.Run("retargeted away from the nodes that did not take it", func(t *testing.T) {
		c := defaultRelCluster()
		aa := assistantPod("")
		c.runPods = []obj{aa, taskPod("build", "", "1Gi", 3*time.Minute, "0/7 nodes are available: 7 node(s) didn't match pod affinity rules.")}
		c.aaPod = aa
		c.sts = assistantSTS(moved, pinnedTo("worker-3"))
		calls, out := runRelocator(t, c)
		patch := templatePatch(t, calls)
		if patch == nil {
			t.Fatalf("the stranded assistant was not retargeted:\n%s", out)
		}
		if got := pinnedNodes(t, patch); strings.Join(got, ",") != "worker-2" {
			t.Fatalf("retargeted to %v; want worker-2 (worker-3 already failed it)", got)
		}
		if sgDig(patch, "metadata", "annotations", "adhar.io/relocations") != "2" {
			t.Fatalf("the retarget is not counted: %v", sgDig(patch, "metadata", "annotations"))
		}
		if !deletedWithUIDPrecondition(calls) {
			t.Fatal("a waiting assistant is never rolled by the StatefulSet controller; it must be replaced here")
		}
	})

	t.Run("handed back when no other node takes it", func(t *testing.T) {
		c := defaultRelCluster()
		c.busy = append(c.busy, busy("filler-2b", "worker-2", "1", "2Gi"))
		aa := assistantPod("")
		c.runPods = []obj{aa, taskPod("build", "", "1Gi", 3*time.Minute, "0/7 nodes are available: 7 node(s) didn't match pod affinity rules.")}
		c.aaPod = aa
		c.sts = assistantSTS(moved, pinnedTo("worker-3"))
		calls, out := runRelocator(t, c)
		patch := templatePatch(t, calls)
		if patch == nil {
			t.Fatalf("the stranded assistant was left pinned:\n%s", out)
		}
		na, present := sgDig(patch, "spec", "template", "spec", "affinity").(map[string]any)["nodeAffinity"]
		if !present || na != nil {
			t.Fatalf("the restore does not remove the pinned node affinity (original had none): %v", na)
		}
		if _, cleared := sgDig(patch, "metadata", "annotations").(map[string]any)["adhar.io/relocated-to"]; !cleared {
			t.Fatal("relocated-to is not cleared, so the handed-back assistant would be retargeted again")
		}
		if eventReason(calls) != "AffinityAssistantRestored" || !deletedWithUIDPrecondition(calls) {
			t.Fatalf("restore not completed:\n%s", out)
		}
	})

	t.Run("an assistant it never pinned stays the autoscaler's", func(t *testing.T) {
		c := defaultRelCluster()
		aa := assistantPod("")
		c.runPods = []obj{aa}
		c.aaPod = aa
		if calls, out := runRelocator(t, c); len(writes(calls)) != 0 {
			t.Fatalf("touched an assistant waiting for capacity it did not cause: %v\n%s", writes(calls), out)
		}
	})
}

// The room the script counts must be the room the scheduler counts: an init
// container is a peak, not an addition, and finished pods hold nothing.
func TestAffinityRelocatorCountsRequestsLikeTheScheduler(t *testing.T) {
	c := defaultRelCluster()
	// worker-3 has 6Gi left after its 2Gi filler. Add a pod whose init
	// container asks 4Gi and whose app container asks 1Gi: the scheduler holds
	// max(4Gi, 1Gi) = 4Gi, leaving 2Gi — enough for the 1Gi build. Summing
	// init and app containers (5Gi) would wrongly rule worker-3 out.
	initHeavy := busy("init-heavy", "worker-3", "100m", "1Gi")
	initHeavy["spec"].(obj)["initContainers"] = []obj{{"name": "i", "resources": obj{"requests": obj{"memory": "4Gi"}}}}
	c.busy = append(c.busy, initHeavy)
	// worker-2 has 3Gi left. A pod with a 1Gi app container and a 1Gi sidecar
	// (a restartable init container) holds BOTH for its life, leaving 1Gi —
	// too little; counted as an ordinary init container it would seem to
	// leave 2Gi.
	sidecar := busy("with-sidecar", "worker-2", "100m", "1Gi")
	sidecar["spec"].(obj)["initContainers"] = []obj{{"name": "s", "restartPolicy": "Always",
		"resources": obj{"requests": obj{"memory": "1Gi"}}}}
	c.busy = append(c.busy, sidecar)
	calls, out := runRelocator(t, c)
	patch := templatePatch(t, calls)
	if patch == nil {
		t.Fatalf("not moved:\n%s", out)
	}
	if got := pinnedNodes(t, patch); strings.Join(got, ",") != "worker-3" {
		t.Fatalf("pinned to %v; want only worker-3 (init containers are a peak, sidecars are held)", got)
	}
}

// Collections are read a page at a time — a whole pod list in one call is what
// got the loop evicted — so every page must be read: with one item per page the
// script must reach the decision it reaches with every item on one page. A
// skipped page of the run's pods loses the waiting pod, of the nodes loses the
// targets, and of the cluster's pods makes the fuller worker-2 look as empty as
// worker-3.
func TestAffinityRelocatorReadsEveryPage(t *testing.T) {
	calls, out := runRelocator(t, defaultRelCluster(), "PAGE_SIZE=1")
	patch := templatePatch(t, calls)
	if patch == nil {
		t.Fatalf("not moved when every list came one item per page:\n%s", out)
	}
	if got := pinnedNodes(t, patch); strings.Join(got, ",") != "worker-3,worker-2" {
		t.Fatalf("pinned to %v with one item per page; want [worker-3 worker-2], as with whole pages\n%s", got, out)
	}
	continued := map[string]bool{}
	for _, c := range calls {
		if c.verb() == "get" && len(c.Args) > 2 && c.Args[1] == "--raw" && strings.Contains(c.Args[2], "continue=") {
			continued[strings.SplitN(c.Args[2], "?", 2)[0]] = true
		}
	}
	for _, path := range []string{"/api/v1/namespaces/" + relNS + "/pods", "/api/v1/nodes", "/api/v1/pods"} {
		if !continued[path] {
			t.Errorf("%s was not read past its first page", path)
		}
	}
}

// A run that no node has room for is looked at again after REPLAN_SECONDS, not
// on every pass: each look reads every pod in the cluster, and every 20 s that
// cost ~210m CPU for as long as the run waited (measured against the 426 pods
// of the DigitalOcean cluster of 2026-11-03).
func TestAffinityRelocatorWaitsBeforeLookingAgainWhenNoNodeHasRoom(t *testing.T) {
	full := func() *relCluster {
		c := defaultRelCluster()
		c.busy = append(c.busy, busy("filler-2b", "worker-2", "1", "2Gi"), busy("filler-3b", "worker-3", "1", "5Gi"))
		return c
	}
	podReads := func(calls []relCall) int {
		n := 0
		for _, c := range calls {
			if c.verb() == "get" && len(c.Args) > 2 && strings.HasPrefix(c.Args[2], "/api/v1/pods?") {
				n++
			}
		}
		return n
	}
	calls, out := runRelocatorPasses(t, full(), 3)
	if n := podReads(calls); n != 1 {
		t.Fatalf("read every pod %d times in 3 passes, within REPLAN_SECONDS of finding no room; want once:\n%s", n, out)
	}
	// The same passes with no wait read them every time, or this test could
	// not tell the difference.
	if calls, out := runRelocatorPasses(t, full(), 3, "REPLAN_SECONDS=0"); podReads(calls) != 3 {
		t.Fatalf("with REPLAN_SECONDS=0, every pod was read %d times in 3 passes; want 3:\n%s", podReads(calls), out)
	}
}

// ── the manifest ───────────────────────────────────────────────────────────

// relocatorMeasuredPeak is the loop's peak memory (cgroup memory.peak, page
// cache included; 64.3 MiB at most) in its own image, against the 426 active
// pods and the CRDs captured from the DigitalOcean cluster of 2026-11-03, read
// page by page, over passes that found no room and passes that moved an
// assistant. Reading every pod in one `get pods -A -o json` peaked at
// 160-192 MiB.
const relocatorMeasuredPeak = "65Mi"

// The loop is needed when nodes are full, which is when the kubelet evicts the
// pods using more memory than they request — first among them, on 2026-11-03,
// this one at a 64Mi request. A request half as much again above its measured
// peak keeps it out of that first round.
func TestAffinityRelocatorRequestsMoreMemoryThanItUses(t *testing.T) {
	dep := findDoc(t, relocatorDocs(t), "Deployment", "tekton-affinity-relocator")
	c := sgDig(dep, "spec", "template", "spec", "containers", 0)
	peak := resource.MustParse(relocatorMeasuredPeak)
	req, err := resource.ParseQuantity(fmt.Sprint(sgDig(c, "resources", "requests", "memory")))
	if err != nil || 2*req.Value() < 3*peak.Value() {
		t.Errorf("memory request %v is under 1.5 x the loop's measured peak (%s): under memory pressure it is evicted when it is needed most",
			sgDig(c, "resources", "requests", "memory"), relocatorMeasuredPeak)
	}
	lim, err := resource.ParseQuantity(fmt.Sprint(sgDig(c, "resources", "limits", "memory")))
	if err != nil || lim.Value() < 2*peak.Value() {
		t.Errorf("memory limit %v leaves under 2 x the loop's measured peak (%s); a larger cluster's pages would OOM-kill it",
			sgDig(c, "resources", "limits", "memory"), relocatorMeasuredPeak)
	}
}

// The loop is needed from the first CI run on, and the platform starts its own
// library releases while the rest of the platform is still converging. Argo CD
// applies a sync wave only once every object of the earlier waves is Healthy,
// and the Tekton dashboard's oauth2-proxy (wave 0) waits for Keycloak: from
// wave 5 the loop arrived after release-adhar-ui had been pinned (DigitalOcean,
// 2026-11-03). Nothing in the Tekton Application may come before it, and its
// Deployment may not come before the ServiceAccount, RBAC and ConfigMap it uses.
func TestAffinityRelocatorRunsFromTheTektonApplicationsFirstWave(t *testing.T) {
	wave := func(d map[string]any) int {
		s, ok := sgDig(d, "metadata", "annotations", "argocd.argoproj.io/sync-wave").(string)
		if !ok {
			return 0
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			t.Fatalf("%v/%v: sync-wave %q is not a number", d["kind"], sgDig(d, "metadata", "name"), s)
		}
		return n
	}
	mine := map[string]int{}
	for _, d := range relocatorDocs(t) {
		mine[fmt.Sprintf("%v/%v", d["kind"], sgDig(d, "metadata", "name"))] = wave(d)
	}
	first, firstObj, others := 0, "", 0
	for _, path := range yamlFilesIn(t, filepath.Join(stackPackagesDir(t), "application", "tekton", "manifests")) {
		if filepath.Base(path) == filepath.Base(relocatorManifest) {
			continue
		}
		for _, d := range yamlDocs(t, path) {
			if w := wave(d); others == 0 || w < first {
				first, firstObj = w, fmt.Sprintf("%v/%v in %s", d["kind"], sgDig(d, "metadata", "name"), filepath.Base(path))
			}
			others++
		}
	}
	if others < 10 {
		t.Fatalf("found only %d other objects in the Tekton Application; this guard is not reading it", others)
	}
	dep, ok := mine["Deployment/tekton-affinity-relocator"]
	if !ok {
		t.Fatal("no Deployment/tekton-affinity-relocator")
	}
	for obj, w := range mine {
		if w > first {
			t.Errorf("%s is in sync wave %d, after %s (wave %d): Argo CD applies it only once that is Healthy", obj, w, firstObj, first)
		}
		if w > dep {
			t.Errorf("%s (wave %d) comes after the Deployment that uses it (wave %d)", obj, w, dep)
		}
	}
}

func TestAffinityRelocatorIsDeployedWithTheLeastItNeeds(t *testing.T) {
	docs := relocatorDocs(t)
	role := findDoc(t, docs, "Role", "tekton-affinity-relocator")
	if sgDig(role, "metadata", "namespace") != relNS {
		t.Fatal("the write permissions must be scoped to adhar-system")
	}
	allowed := map[string]bool{"statefulsets:get": true, "statefulsets:patch": true, "pods:delete": true,
		"persistentvolumeclaims:get": true, "events:create": true}
	for _, r := range sgDig(role, "rules").([]any) {
		for _, res := range sgDig(r, "resources").([]any) {
			for _, v := range sgDig(r, "verbs").([]any) {
				if !allowed[fmt.Sprintf("%v:%v", res, v)] {
					t.Errorf("the namespaced Role grants %v on %v, which the relocator does not use", v, res)
				}
			}
		}
	}
	cr := findDoc(t, docs, "ClusterRole", "tekton-affinity-relocator")
	for _, r := range sgDig(cr, "rules").([]any) {
		for _, v := range sgDig(r, "verbs").([]any) {
			if v != "get" && v != "list" {
				t.Errorf("the ClusterRole grants %v; cluster-wide it may only read", v)
			}
		}
	}

	dep := findDoc(t, docs, "Deployment", "tekton-affinity-relocator")
	pod := sgDig(dep, "spec", "template", "spec").(map[string]any)
	if pod["serviceAccountName"] != "tekton-affinity-relocator" || pod["enableServiceLinks"] != false {
		t.Fatalf("pod spec: serviceAccountName=%v enableServiceLinks=%v", pod["serviceAccountName"], pod["enableServiceLinks"])
	}
	if fmt.Sprint(sgDig(dep, "spec", "replicas")) != "1" || sgDig(dep, "spec", "strategy", "type") != "Recreate" {
		t.Fatal("exactly one loop may run: two would race to move the same assistant")
	}
	c := sgDig(pod, "containers", 0).(map[string]any)
	if img := c["image"]; img != "docker.io/alpine/k8s:1.31.0" {
		t.Errorf("image %v: use the tekton-pruner's image, which already runs here", img)
	}
	if cmd := fmt.Sprint(c["command"]); cmd != "[python3 -I /scripts/relocate.py]" {
		t.Errorf("command %s does not run the mounted script", cmd)
	}
	sc := c["securityContext"].(map[string]any)
	if sc["allowPrivilegeEscalation"] != false || sc["readOnlyRootFilesystem"] != true || sgDig(pod, "securityContext", "runAsNonRoot") != true {
		t.Errorf("the container is not locked down: %v / %v", sc, pod["securityContext"])
	}
	tolerates := false
	for _, tol := range pod["tolerations"].([]any) {
		if sgDig(tol, "key") == "node-role.kubernetes.io/control-plane" {
			tolerates = true
		}
	}
	if !tolerates {
		t.Error("it must run when the workers are full, so it tolerates the control plane")
	}
	env := map[string]string{}
	for _, e := range c["env"].([]any) {
		env[fmt.Sprint(sgDig(e, "name"))] = fmt.Sprint(sgDig(e, "value"))
	}
	var grace, cooldown int
	fmt.Sscan(env["GRACE_SECONDS"], &grace)
	fmt.Sscan(env["COOLDOWN_SECONDS"], &cooldown)
	if env["NAMESPACE"] != relNS || grace < 30 || cooldown < grace || env["MAX_MOVES"] == "" {
		t.Errorf("loop settings %v: a grace under 30 s chases transient waits; a cooldown under the grace ping-pongs", env)
	}
	if !strings.Contains(fmt.Sprint(sgDig(pod, "volumes")), "tekton-affinity-relocator") {
		t.Error("the script ConfigMap is not mounted")
	}
}

// The node autoscaler's grace must outlast the relocator's reaction, or the
// pod it waits for buys a node before the relocator has had its turn.
func TestAutoscalerWaitsLongerThanTheRelocatorReacts(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "autoscaler", "decision.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`affinityAssistantPinGrace = (\d+) \* time\.Minute`).FindSubmatch(src)
	if m == nil {
		t.Fatal("autoscaler/decision.go no longer declares affinityAssistantPinGrace in minutes; update this guard")
	}
	var minutes int
	fmt.Sscan(string(m[1]), &minutes)
	env := map[string]int{}
	dep := findDoc(t, relocatorDocs(t), "Deployment", "tekton-affinity-relocator")
	for _, e := range sgDig(dep, "spec", "template", "spec", "containers", 0, "env").([]any) {
		var n int
		if _, err := fmt.Sscan(fmt.Sprint(sgDig(e, "value")), &n); err == nil {
			env[fmt.Sprint(sgDig(e, "name"))] = n
		}
	}
	if _, ok := env["REPLAN_SECONDS"]; !ok {
		t.Fatal("the Deployment does not set REPLAN_SECONDS; the reaction time below cannot be computed")
	}
	// One grace to notice and one pass to act; then either a retarget, when the
	// first target filled up meanwhile (one more grace and pass), or, when no
	// node had room at the first look, one more look REPLAN_SECONDS later.
	react := env["GRACE_SECONDS"] + env["INTERVAL_SECONDS"] +
		max(env["GRACE_SECONDS"], env["REPLAN_SECONDS"]) + env["INTERVAL_SECONDS"]
	if minutes*60 <= react {
		t.Fatalf("the autoscaler counts a pinned pod after %d s, but the relocator may need %d s", minutes*60, react)
	}
}

// Every CI step requests little CPU. The pods that mount a run's workspace may
// only start on the node the run's affinity assistant holds, and a request is a
// reservation THAT node must have free: at 500m a build sat
// ExceededNodeResources for three hours next to idle cores (AWS, 2026-10-31).
// CPU is compressible — with no limit (or a burstable one) a step still takes
// every idle core — so the request only sets its share under contention.
// Memory is not held to this: it is requested in earnest, see
// TestHeavyCIStepsDeclareTheirMemory.
func TestCIStepsFitTheNodeTheirWorkspacePinsThemTo(t *testing.T) {
	const ceiling = "100m"
	max := resource.MustParse(ceiling)
	seen := 0
	for _, dir := range pipelineDirs(t) {
		for _, path := range yamlFilesIn(t, dir) {
			for _, doc := range yamlDocs(t, path) {
				if doc["kind"] != "Task" {
					continue
				}
				task := fmt.Sprint(sgDig(doc, "metadata", "name"))
				spec, _ := doc["spec"].(map[string]any)
				var parts []any
				for _, key := range []string{"steps", "sidecars"} {
					list, _ := spec[key].([]any)
					parts = append(parts, list...)
				}
				if st, ok := spec["stepTemplate"]; ok {
					parts = append(parts, st)
				}
				for _, p := range parts {
					cpu, ok := sgDig(p, "computeResources", "requests", "cpu").(string)
					if !ok {
						continue
					}
					seen++
					if q, err := resource.ParseQuantity(cpu); err != nil || q.Cmp(max) > 0 {
						t.Errorf("%s: Task %s step %v requests cpu %s; CI steps request at most %s (the pinned node must have it free)",
							filepath.Base(path), task, sgDig(p, "name"), cpu, ceiling)
					}
				}
			}
		}
	}
	if seen < 8 {
		t.Fatalf("found only %d CPU requests in the CI Tasks; this guard is not reading them", seen)
	}
}

// The relocator acts on Tekton's own labels and annotations; if a Tekton
// upgrade renames them the script would silently stop matching anything.
func TestAffinityRelocatorMatchesTektonsAffinityAssistantLabels(t *testing.T) {
	script := relocatorScript(t)
	for _, want := range []string{
		`COMPONENT = "app.kubernetes.io/component"`,
		`INSTANCE = "app.kubernetes.io/instance"`,
		`ASSISTANT = "affinity-assistant"`,
		`RUN_LABEL = "tekton.dev/pipelineRun"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("relocate.py no longer declares %s", want)
		}
	}
	// The Tekton release the platform pins is the one these labels were read
	// from (tektoncd/pipeline v0.65.0 pkg/workspace/affinity_assistant_names.go
	// and pkg/internal/affinityassistant/transformer.go); a bump must re-check.
	install := readStackFile(t, "application/tekton/manifests/install.yaml")
	if !strings.Contains(install, "version: v0.65.0") {
		t.Error("Tekton is no longer v0.65.0: re-check the affinity-assistant labels, its coschedule mode and " +
			"that the reconciler still leaves an assistant alone after the first TaskRun, then update this guard")
	}
	flags := docsOf(t, "application/tekton/manifests/install.yaml")
	ff := findDoc(t, flags, "ConfigMap", "feature-flags")
	if sgDig(ff, "data", "coschedule") != "workspaces" || sgDig(ff, "data", "disable-affinity-assistant") != "false" {
		t.Error("the affinity-assistant mode changed; the relocator assumes one assistant per workspace (coschedule: workspaces)")
	}
	names := []string{}
	for _, d := range relocatorDocs(t) {
		names = append(names, fmt.Sprint(d["kind"]))
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "ClusterRole,ClusterRoleBinding,ConfigMap,Deployment,Role,RoleBinding,ServiceAccount" {
		t.Errorf("unexpected objects in %s: %v", relocatorManifest, names)
	}
}
