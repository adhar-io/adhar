package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/tests/e2e"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

// The suite's access to the Kind cluster and the CLI. Untagged, so `go vet`
// compiles it without the e2e tag.

var (
	batchJobGVR    = schema.GroupVersionResource{Group: "platform.adhar.io", Version: "v1alpha1", Resource: "batchjobs"}
	jobGVR         = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	pipelineRunGVR = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "pipelineruns"}
)

const (
	batchContainer = "batch"
	// The Database XR <app>-db composes a CNPG Cluster of that name whose
	// database and owner are both `app` (compositions/database/local-cnpg.yaml).
	databaseName  = "app"
	databaseOwner = "app"
)

// Cluster is the typed and dynamic clients of the Kind cluster.
type Cluster struct {
	REST *rest.Config
	CS   kubernetes.Interface
	DC   dynamic.Interface
}

// Connect pins the clients to the Adhar Kind context, as bootstrap does.
func Connect() (*Cluster, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
		&clientcmd.ConfigOverrides{CurrentContext: e2e.KubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig context %s: %w", e2e.KubeContext, err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Cluster{REST: cfg, CS: cs, DC: dc}, nil
}

// CheckCLITarget refuses to drive the CLI at a cluster other than the suite's.
// The suite's clients pin the Kind context; the CLI takes the CURRENT context —
// `adhar batch` as kubectl resolves it ($KUBECONFIG, then ~/.kube/config),
// `adhar application` from ~/.kube/config alone — so with the current context
// on a cloud cluster the suite would scaffold, run and clean up THERE while
// verifying Kind.
func CheckCLITarget(suiteServer, kubeconfigEnv, home string) error {
	file := filepath.Join(home, ".kube", "config")
	paths := filepath.SplitList(kubeconfigEnv)
	if len(paths) == 0 {
		paths = []string{file}
	}
	for _, rules := range []*clientcmd.ClientConfigLoadingRules{{Precedence: paths}, {ExplicitPath: file}} {
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return fmt.Errorf("the adhar CLI cannot load its kubeconfig (%v): %w", rules.GetLoadingPrecedence(), err)
		}
		if cfg.Host != suiteServer {
			return fmt.Errorf("the adhar CLI would reach %s through the current context of %v, not the suite's %s at %s; "+
				"run `kubectl config use-context %s` first", cfg.Host, rules.GetLoadingPrecedence(), e2e.KubeContext, suiteServer, e2e.KubeContext)
		}
	}
	return nil
}

// CLIResult is one `adhar` invocation. Stdout is kept apart: `-o json` is
// parsed from it, while the logger writes to stderr.
type CLIResult struct {
	Stdout, Stderr string
	ExitCode       int
}

func (r CLIResult) String() string {
	return fmt.Sprintf("exit %d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
}

// Adhar runs the CLI from the repository root. A non-zero exit is a result,
// not an error: `adhar batch run --wait` exits with the run's code.
func Adhar(ctx context.Context, timeout time.Duration, args ...string) (CLIResult, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(cctx, e2e.AdharBinaryLocation, args...)
	c.Dir = e2e.RepoRoot
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	r := CLIResult{Stdout: out.String(), Stderr: errb.String()}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.ExitCode = exit.ExitCode()
	case err != nil:
		return r, fmt.Errorf("adhar %s: %w", strings.Join(args, " "), err)
	}
	if cctx.Err() != nil {
		return r, fmt.Errorf("adhar %s: no answer within %s", strings.Join(args, " "), timeout)
	}
	return r, nil
}

// RunOutcome is `adhar batch run -o json` (decision D2).
type RunOutcome struct {
	Run         string                 `json:"run"`
	Namespace   string                 `json:"namespace"`
	BatchJob    string                 `json:"batchJob"`
	Kind        string                 `json:"kind"`
	Status      string                 `json:"status"`
	ExitCode    int                    `json:"exitCode"`
	ExecutionID *int64                 `json:"executionId"`
	Summary     map[string]interface{} `json:"summary"`
}

// ParseRunOutcome reads the D2 document; every key is required.
func ParseRunOutcome(stdout string) (RunOutcome, error) {
	var o RunOutcome
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &keys); err != nil {
		return o, fmt.Errorf("not the D2 document: %w\n%s", err, stdout)
	}
	for _, k := range []string{"run", "namespace", "batchJob", "kind", "status", "exitCode", "executionId", "summary"} {
		if _, ok := keys[k]; !ok {
			return o, fmt.Errorf("D2 document without %q: %s", k, stdout)
		}
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(stdout)), &o)
	return o, nil
}

// ParsedSummary is the outcome's summary read as D13 v1.
func (o RunOutcome) ParsedSummary() *Summary {
	if o.Summary == nil {
		return nil
	}
	b, _ := json.Marshal(o.Summary)
	return ParseSummary(string(b))
}

// Eventually polls check until it reports done; on timeout the error carries
// the last thing it saw, which is what makes an e2e failure diagnosable.
func Eventually(ctx context.Context, every time.Duration, what string, check func(context.Context) (bool, string, error)) error {
	var last string
	for {
		done, seen, err := check(ctx)
		if seen != "" {
			last = seen
		}
		if err != nil {
			return fmt.Errorf("%s: %w (last seen: %s)", what, err, last)
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: gave up (%v); last seen: %s", what, ctx.Err(), last)
		case <-time.After(every):
		}
	}
}

// Psql runs SQL in the application database on the CNPG cluster's primary,
// as the superuser over the local socket, and returns `-At -F'|'` output.
func (c *Cluster) Psql(ctx context.Context, ns, dbCluster, sql string) (string, error) {
	var pod string
	for _, sel := range []string{"cnpg.io/cluster=" + dbCluster + ",cnpg.io/instanceRole=primary", "cnpg.io/cluster=" + dbCluster + ",role=primary"} {
		pods, err := c.CS.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			return "", err
		}
		if len(pods.Items) > 0 {
			pod = pods.Items[0].Name
			break
		}
	}
	if pod == "" {
		return "", fmt.Errorf("no primary of CNPG cluster %s/%s", ns, dbCluster)
	}
	req := c.CS.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "postgres",
			Command:   []string{"psql", "-v", "ON_ERROR_STOP=1", "-At", "-F", "|", "-d", databaseName, "-c", sql},
			Stdout:    true, Stderr: true,
		}, scheme.ParameterCodec)
	ex, err := remotecommand.NewSPDYExecutor(c.REST, "POST", req.URL())
	if err != nil {
		return "", err
	}
	var out, errb bytes.Buffer
	if err := ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &out, Stderr: &errb}); err != nil {
		return out.String(), fmt.Errorf("psql in %s/%s: %w: %s", ns, pod, err, errb.String())
	}
	return out.String(), nil
}

// Ledger reads the JobRepository for one identity.
func (c *Cluster) Ledger(ctx context.Context, ns, app string, identifying map[string]string) (Ledger, error) {
	out, err := c.Psql(ctx, ns, app+"-db", LedgerSQL(identifying))
	if err != nil {
		return Ledger{}, err
	}
	return ParseLedger(out)
}

// RunTemplate reads ConfigMap <batchJob>-run.
func (c *Cluster) RunTemplate(ctx context.Context, ns, batchJob string) (*RunTemplate, error) {
	cm, err := c.CS.CoreV1().ConfigMaps(ns).Get(ctx, batchJob+"-run", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return ParseRunTemplate(cm.Data)
}

// CreateRun creates a run the suite built; admission (run-integrity) answers here.
func (c *Cluster) CreateRun(ctx context.Context, run *unstructured.Unstructured) error {
	_, err := c.DC.Resource(jobGVR).Namespace(run.GetNamespace()).Create(ctx, run, metav1.CreateOptions{})
	return err
}

// Finished is a Job's terminal condition: Complete, Failed, or "" while it runs.
func Finished(job *batchv1.Job) (string, string) {
	for _, c := range job.Status.Conditions {
		if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) {
			return string(c.Type), c.Reason
		}
	}
	return "", ""
}

// WaitRunFinished waits for the run's Job to be Complete or Failed.
func (c *Cluster) WaitRunFinished(ctx context.Context, ns, name string) (*batchv1.Job, error) {
	var job *batchv1.Job
	err := Eventually(ctx, 3*time.Second, "run "+ns+"/"+name+" to finish", func(ctx context.Context) (bool, string, error) {
		j, err := c.CS.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error(), nil
		}
		job = j
		cond, reason := Finished(j)
		return cond != "", fmt.Sprintf("active %d failed %d suspended %v %s %s", j.Status.Active, j.Status.Failed,
			j.Spec.Suspend != nil && *j.Spec.Suspend, cond, reason), nil
	})
	return job, err
}

// RunPods are the run's pods, oldest attempt first.
func (c *Cluster) RunPods(ctx context.Context, ns, run string) ([]corev1.Pod, error) {
	pods, err := c.CS.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labelJobName + "=" + run})
	if err != nil {
		return nil, err
	}
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].CreationTimestamp.Before(&pods.Items[j].CreationTimestamp)
	})
	return pods.Items, nil
}

// Terminated is how the batch container of a pod ended, if it has.
func Terminated(pod corev1.Pod) (*corev1.ContainerStateTerminated, bool) {
	for _, s := range pod.Status.ContainerStatuses {
		if s.Name != batchContainer {
			continue
		}
		if s.State.Terminated != nil {
			return s.State.Terminated, true
		}
		if s.LastTerminationState.Terminated != nil {
			return s.LastTerminationState.Terminated, true
		}
	}
	return nil, false
}

// LastSummary is the termination summary of the run's newest pod that wrote one.
func (c *Cluster) LastSummary(ctx context.Context, ns, run string) (*Summary, int32, error) {
	pods, err := c.RunPods(ctx, ns, run)
	if err != nil {
		return nil, 0, err
	}
	for i := len(pods) - 1; i >= 0; i-- {
		if t, ok := Terminated(pods[i]); ok {
			return ParseSummary(t.Message), t.ExitCode, nil
		}
	}
	return nil, 0, fmt.Errorf("no pod of %s/%s has terminated", ns, run)
}

// Evict asks for the pod through the Eviction API, as a drain or a preemption
// does: the pod gains DisruptionTarget, which the run's podFailurePolicy ignores.
func (c *Cluster) Evict(ctx context.Context, ns, pod string) error {
	return c.CS.PolicyV1().Evictions(ns).Evict(ctx, &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod, Namespace: ns}})
}

// KillBatchContainer SIGKILLs the pod's batch container from its Kind node, as
// the OOM killer does: no SIGTERM reaches the JVM, so nothing stops the
// execution and it is left STARTED for the replacement to recover (F1).
func (c *Cluster) KillBatchContainer(ctx context.Context, pod corev1.Pod) error {
	var id string
	for _, s := range pod.Status.ContainerStatuses {
		if s.Name == batchContainer {
			id = s.ContainerID
		}
	}
	args, err := NodeKillCommand(os.Getenv("KIND_EXPERIMENTAL_PROVIDER"), pod.Spec.NodeName, id)
	if err != nil {
		return fmt.Errorf("pod %s: %w", pod.Name, err)
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(cctx, args[0], args[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("SIGKILL %s on node %s: %w: %s", pod.Name, pod.Spec.NodeName, err, out)
	}
	return nil
}

// DescribeRun is what a failed assertion prints: the Job's state, each pod's
// end and the tail of its log.
func (c *Cluster) DescribeRun(ctx context.Context, ns, run string) string {
	var b strings.Builder
	if j, err := c.CS.BatchV1().Jobs(ns).Get(ctx, run, metav1.GetOptions{}); err == nil {
		fmt.Fprintf(&b, "\nJob %s/%s: active %d succeeded %d failed %d", ns, run, j.Status.Active, j.Status.Succeeded, j.Status.Failed)
		for _, cd := range j.Status.Conditions {
			fmt.Fprintf(&b, "\n  %s=%s %s: %s", cd.Type, cd.Status, cd.Reason, cd.Message)
		}
	} else {
		fmt.Fprintf(&b, "\nJob %s/%s: %v", ns, run, err)
	}
	pods, _ := c.RunPods(ctx, ns, run)
	for _, p := range pods {
		fmt.Fprintf(&b, "\n  pod %s %s", p.Name, p.Status.Phase)
		if t, ok := Terminated(p); ok {
			fmt.Fprintf(&b, " exit %d (%s) message %q", t.ExitCode, t.Reason, t.Message)
		}
		tail := int64(25)
		if logs, err := c.CS.CoreV1().Pods(ns).GetLogs(p.Name, &corev1.PodLogOptions{Container: batchContainer, TailLines: &tail}).DoRaw(ctx); err == nil {
			fmt.Fprintf(&b, "\n    %s", strings.ReplaceAll(strings.TrimSpace(string(logs)), "\n", "\n    "))
		}
	}
	return b.String()
}

// ConditionTrue says whether an unstructured object carries type=True.
func ConditionTrue(obj *unstructured.Unstructured, typ string) (bool, string) {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]interface{})
		if m["type"] == typ {
			return m["status"] == "True", fmt.Sprintf("%s=%v %v: %v", typ, m["status"], m["reason"], m["message"])
		}
	}
	return false, typ + " not reported"
}
