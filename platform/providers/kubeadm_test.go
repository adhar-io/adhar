package provider

import (
	"strings"
	"testing"

	"adhar-io/adhar/globals"
)

// The kubeadm package stream must follow the platform-wide Kubernetes default
// so cloud clusters run the same minor as local Kind.
func TestKubeadmDefaultMinorMatchesGlobals(t *testing.T) {
	want := K8sMinorFromVersion(globals.DefaultKubernetesVersion)
	if KubeadmDefaultK8sMinor != want {
		t.Fatalf("KubeadmDefaultK8sMinor=%q but globals.DefaultKubernetesVersion=%q (minor %q)", KubeadmDefaultK8sMinor, globals.DefaultKubernetesVersion, want)
	}
	if !strings.HasPrefix(KubeadmNodePrepScript(KubeadmDefaultK8sMinor), "#!/bin/bash") {
		t.Fatal("node prep script must be a bash script")
	}
}

func TestWorkerScalePlanAddsLowestFreeIndicesAndRemovesHighestFirst(t *testing.T) {
	prefix := "adhar-dev-workers-"
	// Gaps are reused (workers-4 was retired earlier), never renumbered.
	add, remove := WorkerScalePlan(prefix, []string{prefix + "1", prefix + "2", prefix + "3", prefix + "5"}, 6)
	if len(remove) != 0 || len(add) != 2 || add[0] != prefix+"4" || add[1] != prefix+"6" {
		t.Fatalf("scale up: add=%v remove=%v", add, remove)
	}
	// Scale down retires the youngest (highest-indexed) workers first.
	add, remove = WorkerScalePlan(prefix, []string{prefix + "1", prefix + "2", prefix + "3", prefix + "5"}, 2)
	if len(add) != 0 || len(remove) != 2 || remove[0] != prefix+"5" || remove[1] != prefix+"3" {
		t.Fatalf("scale down: add=%v remove=%v", add, remove)
	}
	// Names that are not members of this group are ignored, not counted.
	add, remove = WorkerScalePlan(prefix, []string{prefix + "1", "adhar-dev-master-1", "other-workers-9"}, 1)
	if len(add) != 0 || len(remove) != 0 {
		t.Fatalf("unrelated names must not count: add=%v remove=%v", add, remove)
	}
	// A negative desired count is treated as zero.
	_, remove = WorkerScalePlan(prefix, []string{prefix + "1"}, -3)
	if len(remove) != 1 {
		t.Fatalf("negative desired: remove=%v", remove)
	}
}

func TestWorkerScalePlanEdgeCases(t *testing.T) {
	if add, remove := WorkerScalePlan("w-", []string{"w-1", "w-2"}, 2); len(add) != 0 || len(remove) != 0 {
		t.Errorf("at the desired count nothing changes: add=%v remove=%v", add, remove)
	}
	if add, remove := WorkerScalePlan("w-", []string{"w-1", "w-2"}, 0); len(add) != 0 || len(remove) != 2 || remove[0] != "w-2" {
		t.Errorf("scaling to zero removes highest first: add=%v remove=%v", add, remove)
	}
	if add, _ := WorkerScalePlan("w-", nil, 2); len(add) != 2 || add[0] != "w-1" || add[1] != "w-2" {
		t.Errorf("from nothing, indices start at 1: %v", add)
	}
	if add, _ := WorkerScalePlan("w-", []string{"w-1", "w-3", "other"}, 4); len(add) != 2 || add[0] != "w-2" || add[1] != "w-4" {
		t.Errorf("gaps are filled lowest-first and foreign names ignored: %v", add)
	}
}

func TestCriticalPathImagesAreTheCiliumDataPathAndPinned(t *testing.T) {
	if len(CriticalPathImages) != 3 {
		t.Fatalf("the pre-pull set must stay minimal (it is on the critical path); got %d", len(CriticalPathImages))
	}
	for _, img := range CriticalPathImages {
		if !strings.HasPrefix(img, "quay.io/cilium/") {
			t.Errorf("only the CNI data path belongs on the pre-pull set: %s", img)
		}
		if !strings.Contains(img, ":") || strings.HasSuffix(img, ":latest") {
			t.Errorf("pre-pulled images must be pinned to an exact tag: %s", img)
		}
	}
}

func TestNodePrepPrePullsTheCriticalPathInTheBackground(t *testing.T) {
	script := KubeadmNodePrepScript("1.37")
	for _, want := range []string{
		"ctr -n k8s.io images pull", "--hosts-dir /etc/containerd/certs.d", "nohup sh -c", "|| true",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("node prep must pre-pull tolerantly through certs.d; missing %q", want)
		}
	}
	for _, img := range CriticalPathImages {
		if !strings.Contains(script, img) {
			t.Errorf("node prep does not pre-pull %s", img)
		}
	}
	// The pre-pull must not block node prep, and must run before the
	// completion marker so the provider's WaitForNodePrep is not delayed.
	pull := strings.Index(script, "ctr -n k8s.io images pull")
	marker := strings.Index(script, "touch "+KubeadmCloudInitMarker)
	if pull < 0 || marker < 0 || pull > marker {
		t.Errorf("pre-pull must be started before the completion marker (pull=%d marker=%d)", pull, marker)
	}
	line := script[strings.LastIndex(script[:pull], "\n")+1:]
	line = line[:strings.Index(line, "\n")]
	if !strings.HasSuffix(strings.TrimSpace(line), "&") {
		t.Errorf("the pre-pull must be backgrounded so node prep returns immediately: %q", line)
	}
}

// The image-pull tuning is the biggest single lever on bring-up time, and it is
// applied by appending to a file kubeadm generated — so the exact quoting matters:
// it has to survive Go -> ssh -> sh -> printf, and a slip is only discovered on a
// node that has already been paid for.
func TestImagePullTuningCommand(t *testing.T) {
	// The KubeletConfiguration keys must be the config-file spellings, not the
	// deprecated flag names: there is no flag form of maxParallelImagePulls at all,
	// and --serialize-image-pulls is on its way out.
	tuning := ""
	for _, t := range kubeletTuning {
		tuning += t.Line + "\n"
	}
	for _, want := range []string{"serializeImagePulls: false", "maxParallelImagePulls: 5"} {
		if !strings.Contains(tuning, want) {
			t.Errorf("kubeletTuning is missing %q", want)
		}
	}

	cmd := imagePullTuningCommand()

	// One line. A real newline in the command would split the ssh script and the
	// second half would run as its own, meaningless, command.
	if strings.Contains(cmd, "\n") {
		t.Errorf("the command must be a single line, got:\n%s", cmd)
	}
	// Each payload reaches sh as an escaped newline inside quotes, which printf then
	// expands — that is what appends a real YAML line. One payload per key now, so
	// a node missing only some keys gains only those.
	for _, want := range []string{
		`"serializeImagePulls: false\n"`,
		`"maxParallelImagePulls: 5\n"`,
		`"maxPods: 250\n"`,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("payload %s is not quoted for printf expansion, got: %s", want, cmd)
		}
	}
	// Idempotent: a re-run must neither duplicate the keys nor bounce a healthy
	// kubelet, which on a converged cluster would evict nothing but would restart
	// every static pod's probes for no reason.
	if !strings.Contains(cmd, "grep -q '^serializeImagePulls:'") {
		t.Errorf("the command is not guarded against a second run: %s", cmd)
	}
	if !strings.Contains(cmd, "systemctl restart kubelet") {
		t.Errorf("the kubelet is never restarted, so the config would not take effect: %s", cmd)
	}
	// It writes the file kubeadm generates, not a drop-in kubeadm would overwrite.
	if !strings.Contains(cmd, "/var/lib/kubelet/config.yaml") {
		t.Errorf("the command targets the wrong file: %s", cmd)
	}
}

// maxPods must be raised on CLOUD nodes too, not only on Kind.
//
// The kubelet's default of 110 is a hard scheduling ceiling, and the Kind provider
// has set 250 since it shipped. Cloud nodes did not, so every kubeadm cluster was
// capped at 110 per node however large the machine. Measured on Azure
// (2026-09-26): of 113 pods that could not run, 96 were blocked by the pod ceiling
// and only 1 by CPU — the node had CPU to spare and still could place nothing.
func TestKubeletTuningRaisesMaxPods(t *testing.T) {
	var line string
	for _, t := range kubeletTuning {
		if t.Key == "maxPods" {
			line = t.Line
		}
	}
	if line == "" {
		t.Fatal("kubeletTuning does not set maxPods; cloud nodes stay at the 110 default")
	}
	if !strings.Contains(line, "250") {
		t.Errorf("maxPods = %q, want 250 to match the Kind provider", line)
	}
	if !strings.Contains(imagePullTuningCommand(), "maxPods") {
		t.Error("the applied command does not mention maxPods")
	}
}

// Each key is guarded on its own, so a node written by an older release gains a key
// added later. Guarding the whole block on one key meant the append was skipped
// entirely once that key was present.
func TestKubeletTuningGuardsEachKeySeparately(t *testing.T) {
	cmd := imagePullTuningCommand()
	for _, k := range []string{"serializeImagePulls", "maxParallelImagePulls", "maxPods"} {
		if !strings.Contains(cmd, "grep -q '^"+k+":'") {
			t.Errorf("no separate guard for %q; an existing node would never gain it", k)
		}
	}
	// Exactly one restart, and only when something actually changed.
	if strings.Count(cmd, "systemctl restart kubelet") != 1 {
		t.Errorf("expected exactly one kubelet restart, got %d", strings.Count(cmd, "systemctl restart kubelet"))
	}
	if !strings.Contains(cmd, "changed=1") {
		t.Error("the restart must be conditional on a change, or every run bounces the kubelet")
	}
}
