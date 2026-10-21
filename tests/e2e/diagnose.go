package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DescribeUnhealthy explains WHY the named Applications are not Healthy, in the
// words of the objects themselves.
//
// Two podman E2E runs (2026-10-09, 2026-10-10) ended with nothing more than
// "timed out waiting for apps to be healthy: [kyverno]" after kyverno had sat at
// Progressing for over thirty minutes — while the same commit passed on Docker.
// The Application's own status already names the resource that is holding its
// health, the pod that resource is waiting on carries the reason in its
// container state, and the namespace's Warning events carry the rest. None of
// that reached the CI log, so the failure was unactionable. This gathers all
// three; best effort, never an error of its own.
func DescribeUnhealthy(ctx context.Context, kubeClient client.Client, names []string) string {
	var b strings.Builder
	for _, name := range names {
		app := argov1alpha1.Application{}
		if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: name}, &app); err != nil {
			fmt.Fprintf(&b, "\n%s: could not read Application: %v", name, err)
			continue
		}
		fmt.Fprintf(&b, "\n%s: health=%s sync=%s", name, app.Status.Health.Status, app.Status.Sync.Status)
		if op := app.Status.OperationState; op != nil && op.Message != "" {
			fmt.Fprintf(&b, "\n  operation: %s — %s", op.Phase, truncate(op.Message, 240))
		}
		for _, r := range app.Status.Resources {
			if r.Health == nil || r.Health.Status == "Healthy" || r.Health.Status == "" {
				continue
			}
			fmt.Fprintf(&b, "\n  %s/%s: %s %s", r.Kind, r.Name, r.Health.Status, truncate(r.Health.Message, 200))
		}
	}

	pods := corev1.PodList{}
	if err := kubeClient.List(ctx, &pods, client.InNamespace(PlatformNamespace)); err == nil {
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodSucceeded || podReady(&p) {
				continue
			}
			fmt.Fprintf(&b, "\npod %s: %s", p.Name, p.Status.Phase)
			if p.Spec.NodeName == "" {
				for _, c := range p.Status.Conditions {
					if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue {
						fmt.Fprintf(&b, " — unscheduled: %s", truncate(c.Message, 200))
					}
				}
			}
			for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
				switch {
				case cs.State.Waiting != nil:
					fmt.Fprintf(&b, "\n    %s waiting: %s %s", cs.Name, cs.State.Waiting.Reason, truncate(cs.State.Waiting.Message, 160))
				case cs.State.Terminated != nil:
					fmt.Fprintf(&b, "\n    %s terminated: %s (exit %d)", cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
				case !cs.Ready:
					fmt.Fprintf(&b, "\n    %s running but not ready (restarts %d)", cs.Name, cs.RestartCount)
				}
				if t := cs.LastTerminationState.Terminated; t != nil && cs.RestartCount > 0 {
					fmt.Fprintf(&b, "\n    %s last exit: %s (exit %d), restarts %d", cs.Name, t.Reason, t.ExitCode, cs.RestartCount)
				}
			}
		}
	}

	events := corev1.EventList{}
	if err := kubeClient.List(ctx, &events, client.InNamespace(PlatformNamespace)); err == nil {
		warnings := make([]corev1.Event, 0, len(events.Items))
		for _, e := range events.Items {
			if e.Type == corev1.EventTypeWarning {
				warnings = append(warnings, e)
			}
		}
		sort.Slice(warnings, func(i, j int) bool { return warnings[i].LastTimestamp.After(warnings[j].LastTimestamp.Time) })
		if len(warnings) > 20 {
			warnings = warnings[:20]
		}
		for _, e := range warnings {
			fmt.Fprintf(&b, "\nevent %s %s/%s %s: %s", e.LastTimestamp.Format("15:04:05"), e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Reason, truncate(e.Message, 180))
		}
	}
	return b.String()
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// DumpPlatformState captures what the cluster looked like when `adhar up`
// gave up, for the CI log. A failed bring-up used to report only "n/15 apps
// Synced + Healthy" and then tear the cluster down, so the Podman job failed
// on every run for days with nothing to say WHICH apps were stuck or why.
// Each command is best effort: a diagnostic must never be the thing that fails.
func DumpPlatformState(ctx context.Context) string {
	return dumpState(ctx, KubeContext)
}

func dumpState(ctx context.Context, kubeContext string) string {
	kc := "kubectl --context " + kubeContext
	sections := []struct{ title, cmd string }{
		{"Argo CD applications", kc + ` -n adhar-system get applications.argoproj.io -o custom-columns=` +
			`'APP:.metadata.name,HEALTH:.status.health.status,SYNC:.status.sync.status,` +
			`OP:.status.operationState.phase,MESSAGE:.status.operationState.message'`},
		{"pods not Running or Completed", kc + ` get pods -A -o wide | grep -v -E ' (Running|Completed) ' || true`},
		{"recent warning events", kc + ` get events -A --field-selector type=Warning --sort-by=.lastTimestamp | tail -n 40`},
		{"node allocation", kc + ` describe nodes | grep -A 8 'Allocated resources' || true`},
		{"container engine", `(docker info --format '{{.NCPU}} cpu {{.MemTotal}} bytes {{.Driver}}' || ` +
			`podman info --format '{{.Host.CPUs}} cpu {{.Host.MemTotal}} bytes rootless={{.Host.Security.Rootless}}')` +
			` 2>/dev/null || true`},
	}
	var b strings.Builder
	for _, s := range sections {
		cmdCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		// Through a shell: the sections use pipes and quoting, which
		// RunCommand (whitespace-split, no shell) cannot express.
		out, err := exec.CommandContext(cmdCtx, "sh", "-c", s.cmd).CombinedOutput()
		cancel()
		// Tables keep their lines (truncate() folds whitespace for one-line
		// reasons); only the length is capped.
		text := string(out)
		if len(text) > 12000 {
			text = text[:12000] + "\n…(truncated)\n"
		}
		fmt.Fprintf(&b, "\n===== %s =====\n%s", s.title, text)
		if err != nil {
			fmt.Fprintf(&b, "(command failed: %v)\n", err)
		}
	}
	return b.String()
}
