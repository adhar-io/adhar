package batch

import (
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A1.1's three interruptions, against the run template the composition
// renders: a delete counts once and an eviction not at all (both stop the
// execution), a SIGKILL counts once and leaves it for `recover` (F1). A policy
// that put 137 among the FailJob codes would end an OOM-killed run unrestarted.
func TestBatchInterruptionsEndAsThePodFailurePolicyAndF1F2Say(t *testing.T) {
	tpl := loadRunTemplate(t)
	for how, want := range map[Interruption]Expected{
		Delete: {Failed: 1, Interrupted: "STOPPED", ExitCode: 143},
		Evict:  {Failed: 0, Interrupted: "STOPPED", ExitCode: 143},
		Kill:   {Failed: 1, Interrupted: "FAILED", ExitCode: 137},
	} {
		got, err := tpl.Expect(how)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v, want %+v", how, got, err, want)
		}
	}
	if _, err := tpl.Expect("oom"); err == nil {
		t.Error("an unknown interruption was given an expectation")
	}

	fail := tpl.Manifest.DeepCopy()
	rules, _, _ := unstructured.NestedSlice(fail.Object, "spec", "podFailurePolicy", "rules")
	codes := rules[1].(map[string]interface{})["onExitCodes"].(map[string]interface{})
	codes["values"] = append(codes["values"].([]interface{}), int64(137))
	_ = unstructured.SetNestedSlice(fail.Object, rules, "spec", "podFailurePolicy", "rules")
	if _, err := (&RunTemplate{Manifest: fail}).Expect(Kill); err == nil {
		t.Error("a policy that fails the run on 137 was accepted for A1.1")
	}
	unstructured.RemoveNestedField(fail.Object, "spec", "podFailurePolicy")
	if _, err := (&RunTemplate{Manifest: fail}).Expect(Delete); err == nil {
		t.Error("a template without a podFailurePolicy was given an expectation")
	}
}

func TestBatchPodFailurePolicyIsReadInOrderLikeTheJobController(t *testing.T) {
	tpl := loadRunTemplate(t)
	disrupted := []corev1.PodConditionType{corev1.DisruptionTarget}
	for _, tc := range []struct {
		code       int32
		conditions []corev1.PodConditionType
		want       batchv1.PodFailurePolicyAction
	}{
		{2, nil, batchv1.PodFailurePolicyActionFailJob},
		{7, nil, batchv1.PodFailurePolicyActionFailJob},
		{5, nil, batchv1.PodFailurePolicyActionCount},
		{1, nil, batchv1.PodFailurePolicyActionCount},
		// The first rule wins: a disrupted pod is replaced whatever it exited with.
		{2, disrupted, batchv1.PodFailurePolicyActionIgnore},
		{137, []corev1.PodConditionType{corev1.PodReady}, batchv1.PodFailurePolicyActionCount},
	} {
		got, err := tpl.PolicyAction(tc.code, tc.conditions)
		if err != nil || got != tc.want {
			t.Errorf("exit %d %v: %s %v, want %s", tc.code, tc.conditions, got, err, tc.want)
		}
	}
	// The exit-code rule names the batch container; a sidecar's code is not it.
	other := tpl.Manifest.DeepCopy()
	rules, _, _ := unstructured.NestedSlice(other.Object, "spec", "podFailurePolicy", "rules")
	rules[1].(map[string]interface{})["onExitCodes"].(map[string]interface{})["containerName"] = "sidecar"
	_ = unstructured.SetNestedSlice(other.Object, rules, "spec", "podFailurePolicy", "rules")
	if got, _ := (&RunTemplate{Manifest: other}).PolicyAction(2, nil); got != batchv1.PodFailurePolicyActionCount {
		t.Errorf("another container's rule decided the batch container's exit: %s", got)
	}
	// NotIn matches the codes it does not list; a condition's status defaults to True.
	codes := rules[1].(map[string]interface{})["onExitCodes"].(map[string]interface{})
	codes["containerName"], codes["operator"], codes["values"] = "batch", "NotIn", []interface{}{int64(0)}
	delete(rules[0].(map[string]interface{})["onPodConditions"].([]interface{})[0].(map[string]interface{}), "status")
	_ = unstructured.SetNestedSlice(other.Object, rules, "spec", "podFailurePolicy", "rules")
	for code, want := range map[int32]batchv1.PodFailurePolicyAction{5: batchv1.PodFailurePolicyActionFailJob, 0: batchv1.PodFailurePolicyActionCount} {
		if got, _ := (&RunTemplate{Manifest: other}).PolicyAction(code, nil); got != want {
			t.Errorf("NotIn [0], exit %d: %s, want %s", code, got, want)
		}
	}
	if got, _ := (&RunTemplate{Manifest: other}).PolicyAction(5, disrupted); got != batchv1.PodFailurePolicyActionIgnore {
		t.Errorf("a DisruptionTarget rule with no status: %s, want Ignore", got)
	}
}

// The kill runs a shell on a node of the operator's machine, so only a
// containerd id reaches it, and a pid that is not a container's never is killed.
func TestBatchNodeKillReachesOnlyTheBatchContainersProcess(t *testing.T) {
	id := strings.Repeat("ab12", 16)
	args, err := NodeKillCommand("", "adhar-control-plane", "containerd://"+id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args[:5], " ") != "docker exec adhar-control-plane sh -c" {
		t.Errorf("command %q", args[:5])
	}
	script := args[5]
	for _, want := range []string{"crictl inspect -o go-template --template '{{.info.pid}}' " + id, `[ "$pid" -gt 1 ]`, `kill -9 "$pid"`} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	if !strings.Contains(script, "exit 1") || strings.Index(script, "exit 1") > strings.Index(script, "kill -9") {
		t.Errorf("the pid guard does not stop the kill:\n%s", script)
	}
	if args, _ := NodeKillCommand("podman", "adhar-control-plane", "containerd://"+id); len(args) == 0 || args[0] != "podman" {
		t.Errorf("podman: %q", args)
	}
	for _, bad := range []struct{ engine, node, id string }{
		{"", "adhar-control-plane", ""},
		{"", "adhar-control-plane", "docker://" + id},
		{"", "adhar-control-plane", "containerd://" + id + "; reboot"},
		{"", "adhar-control-plane", "containerd://$(reboot)"},
		{"", "", "containerd://" + id},
		{"", "-it", "containerd://" + id},
		{"nerdctl", "adhar-control-plane", "containerd://" + id},
	} {
		if args, err := NodeKillCommand(bad.engine, bad.node, bad.id); err == nil {
			t.Errorf("%+v accepted: %q", bad, args)
		}
	}
}

func TestBatchLedgerStatusOfNamesOneExecution(t *testing.T) {
	l := Ledger{Executions: []Execution{{ID: 4, Status: "COMPLETED"}, {ID: 3, Status: "FAILED"}}}
	if l.StatusOf(3) != "FAILED" || l.StatusOf(4) != "COMPLETED" || l.StatusOf(5) != "" {
		t.Errorf("StatusOf: %q %q %q", l.StatusOf(3), l.StatusOf(4), l.StatusOf(5))
	}
}
