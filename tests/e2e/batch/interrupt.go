package batch

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Interruption is how A1.1 takes a run's pod away mid-chunk. The three reach
// the runner differently, and that difference is what the suite checks (§13):
// a signal the JVM sees is a graceful stop (F2), a SIGKILL is not (F1).
type Interruption string

const (
	// Delete is a plain API delete: SIGTERM, then the pod's grace period.
	Delete Interruption = "delete"
	// Evict is the Eviction API, as a drain or preemption: SIGTERM plus DisruptionTarget.
	Evict Interruption = "evict"
	// Kill is SIGKILL with no SIGTERM first — what the OOM killer does.
	Kill Interruption = "kill"
)

// Expected is how an interrupted run must end.
type Expected struct {
	// Failed is status.failed of the finished Job: the podFailurePolicy's verdict.
	Failed int32
	// Interrupted is the final status of the execution the pod was running:
	// STOPPED by the graceful stop (§5.9), or FAILED by `recover` (§5.5).
	Interrupted string
	// ExitCode is the killed container's, when the suite can read it.
	ExitCode int32
}

// Expect is what interrupting a run of this template must end in, the Job's
// count taken from the template's own podFailurePolicy.
func (t *RunTemplate) Expect(how Interruption) (Expected, error) {
	// SIGTERM: the JVM's signal status stands (§5.7); SIGKILL: 128+9.
	e := Expected{Interrupted: "STOPPED", ExitCode: 143}
	var conditions []corev1.PodConditionType
	switch how {
	case Delete:
	case Evict:
		conditions = []corev1.PodConditionType{corev1.DisruptionTarget}
	case Kill:
		e = Expected{Interrupted: "FAILED", ExitCode: 137}
	default:
		return e, fmt.Errorf("unknown interruption %q", how)
	}
	action, err := t.PolicyAction(e.ExitCode, conditions)
	if err != nil {
		return e, err
	}
	switch action {
	case batchv1.PodFailurePolicyActionCount:
		e.Failed = 1
	case batchv1.PodFailurePolicyActionIgnore:
	default:
		return e, fmt.Errorf("a %s would %s the run; A1.1 needs it replaced", how, action)
	}
	return e, nil
}

// PolicyAction evaluates the template's podFailurePolicy for a batch container
// that exited with code on a pod carrying conditions (True): the first rule
// that matches wins, and no match is Count [S49].
func (t *RunTemplate) PolicyAction(code int32, conditions []corev1.PodConditionType) (batchv1.PodFailurePolicyAction, error) {
	raw, found, err := unstructured.NestedMap(t.Manifest.Object, "spec", "podFailurePolicy")
	if err != nil || !found {
		return "", fmt.Errorf("the run template has no podFailurePolicy (§5.7): %v", err)
	}
	var policy batchv1.PodFailurePolicy
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &policy); err != nil {
		return "", fmt.Errorf("podFailurePolicy: %w", err)
	}
	for _, r := range policy.Rules {
		for _, p := range r.OnPodConditions {
			if (p.Status == "" || p.Status == corev1.ConditionTrue) && slices.Contains(conditions, p.Type) {
				return r.Action, nil
			}
		}
		if c := r.OnExitCodes; c != nil && (c.ContainerName == nil || *c.ContainerName == batchContainer) {
			in := slices.Contains(c.Values, code)
			if (c.Operator == batchv1.PodFailurePolicyOnExitCodesOpIn) == in {
				return r.Action, nil
			}
		}
	}
	return batchv1.PodFailurePolicyActionCount, nil
}

var (
	containerdID = regexp.MustCompile(`^containerd://([0-9a-f]{12,64})$`)
	kindNodeName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
)

// NodeKillCommand is the command that SIGKILLs a container's process from its
// Kind node. Inside the container that process is PID 1, which its own
// namespace cannot SIGKILL; the node's namespace is the parent and can. The
// pid is guarded so a failed lookup never reaches the node's own init.
func NodeKillCommand(engine, node, containerID string) ([]string, error) {
	if engine == "" {
		engine = "docker"
	}
	if engine != "docker" && engine != "podman" {
		return nil, fmt.Errorf("KIND_EXPERIMENTAL_PROVIDER=%q: the suite drives docker or podman", engine)
	}
	if !kindNodeName.MatchString(node) {
		return nil, fmt.Errorf("node %q is not a Kind node name", node)
	}
	m := containerdID.FindStringSubmatch(containerID)
	if m == nil {
		return nil, fmt.Errorf("container %q is not a containerd container", containerID)
	}
	script := strings.Join([]string{
		`pid=$(crictl inspect -o go-template --template '{{.info.pid}}' ` + m[1] + `)`,
		`[ "$pid" -gt 1 ] 2>/dev/null || { echo "no process for ` + m[1] + `: $pid" >&2; exit 1; }`,
		`kill -9 "$pid"`,
	}, "\n")
	return []string{engine, "exec", node, "sh", "-c", script}, nil
}
