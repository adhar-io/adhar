package batch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// D2 fixes the keys of `adhar batch run -o json`; the suite reads them, so a
// CLI that dropped one fails here rather than as a nil deep in an e2e.
func TestBatchRunOutcomeIsTheD2Document(t *testing.T) {
	doc := `{"run":"orders-09932cb3","namespace":"dev","batchJob":"orders","kind":"Job","status":"Succeeded","exitCode":0,
	 "executionId":97,"summary":{"v":1,"outcome":"skipped","reason":"already-complete","exitCode":0}}`
	o, err := ParseRunOutcome(doc)
	if err != nil {
		t.Fatal(err)
	}
	if o.Run != "orders-09932cb3" || o.Status != "Succeeded" || *o.ExecutionID != 97 {
		t.Errorf("outcome: %+v", o)
	}
	if err := o.ParsedSummary().CheckSkipped("already-complete"); err != nil {
		t.Error(err)
	}
	if _, err := ParseRunOutcome(strings.Replace(doc, `"executionId":97,`, "", 1)); err == nil {
		t.Error("a document without executionId was accepted")
	}
	if _, err := ParseRunOutcome("Run orders-09932cb3 created"); err == nil {
		t.Error("a table was accepted as JSON")
	}
	if o, _ := ParseRunOutcome(strings.Replace(doc, `{"v":1,"outcome":"skipped","reason":"already-complete","exitCode":0}`, "null", 1)); o.ParsedSummary() != nil {
		t.Error("a null summary was read as one")
	}
}

// Both of the CLI's kubeconfig resolutions must land on the suite's cluster.
func TestBatchCLIMustTargetTheSuitesCluster(t *testing.T) {
	const kind, cloud = "https://127.0.0.1:6443", "https://18.143.183.20:6443"
	kubeconfig := func(dir, current string) string {
		path := filepath.Join(dir, "config")
		doc := "apiVersion: v1\nkind: Config\ncurrent-context: " + current + "\nclusters:\n" +
			"- {name: kind, cluster: {server: '" + kind + "'}}\n- {name: cloud, cluster: {server: '" + cloud + "'}}\n" +
			"users:\n- {name: u, user: {token: x}}\ncontexts:\n" +
			"- {name: kind-adhar, context: {cluster: kind, user: u}}\n- {name: adhar, context: {cluster: cloud, user: u}}\n"
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	home := func(current string) string {
		h := t.TempDir()
		kubeconfig(filepath.Join(h, ".kube"), current)
		return h
	}
	if err := CheckCLITarget(kind, "", home("kind-adhar")); err != nil {
		t.Errorf("current context on the suite's cluster: %v", err)
	}
	if err := CheckCLITarget(kind, "", home("adhar")); err == nil || !strings.Contains(err.Error(), cloud) {
		t.Errorf("current context on another cluster: %v", err)
	}
	// `adhar batch` follows $KUBECONFIG, `adhar application` does not: either one astray is refused.
	if err := CheckCLITarget(kind, kubeconfig(t.TempDir(), "adhar"), home("kind-adhar")); err == nil {
		t.Error("$KUBECONFIG on another cluster was accepted")
	}
	if err := CheckCLITarget(kind, kubeconfig(t.TempDir(), "kind-adhar"), home("adhar")); err == nil {
		t.Error("~/.kube/config on another cluster was accepted")
	}
	if err := CheckCLITarget(kind, "", t.TempDir()); err == nil {
		t.Error("no kubeconfig at all was accepted")
	}
}

func TestBatchJobFinishedOnlyOnATrueTerminalCondition(t *testing.T) {
	job := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "PodFailurePolicy"},
		{Type: batchv1.JobComplete, Status: corev1.ConditionFalse},
	}}}
	if c, _ := Finished(job); c != "" {
		t.Errorf("a FailureTarget is not the end yet: %q", c)
	}
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "PodFailurePolicy"})
	if c, r := Finished(job); c != "Failed" || r != "PodFailurePolicy" {
		t.Errorf("finished = %q %q", c, r)
	}
}

func TestBatchTerminatedReadsTheBatchContainer(t *testing.T) {
	pod := corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: "sidecar", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
		{Name: "batch", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2, Message: `{"v":1}`}}},
	}}}
	if term, ok := Terminated(pod); !ok || term.ExitCode != 2 {
		t.Errorf("terminated = %+v %v", term, ok)
	}
	running := corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "batch",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	if _, ok := Terminated(running); ok {
		t.Error("a running container read as terminated")
	}
}

func TestBatchEventuallyReportsWhatItLastSaw(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	n := 0
	err := Eventually(ctx, 10*time.Millisecond, "the run to finish", func(context.Context) (bool, string, error) {
		n++
		return false, "active 1", nil
	})
	if err == nil || !strings.Contains(err.Error(), "last seen: active 1") || n < 2 {
		t.Errorf("timeout error %v after %d polls", err, n)
	}
	if err := Eventually(context.Background(), time.Millisecond, "x", func(context.Context) (bool, string, error) { return true, "", nil }); err != nil {
		t.Error(err)
	}
}
