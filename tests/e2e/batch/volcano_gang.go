package batch

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The canary's own workloads. A2.1, A2.4 and A2.7 are properties of the
// scheduler and its configuration, so they are driven with synthetic gangs
// whose sizes the suite computes from the room it leaves (volcano_model.go),
// beside the real batch runs. Every one is shaped to pass the platform's
// policies as a team's run must: its namespace's leaf queue, a Volcano run
// class (DEC-1), restartPolicy Never, a deadline and a TTL, memory request ==
// limit, no volumes and no plugin but env (adhar-batch-volcano-pod-standards,
// §4.5), and a CPU limit equal to the request, because the environment's
// LimitRange would otherwise give a large member a default limit below its
// request.

// gangImage is the platform's most used utility image, already in the Kind
// node's pull-through cache.
const gangImage = "docker.io/library/busybox:1.36"

// labelE2E marks everything the canary creates, for cleanup and diagnosis.
const labelE2E = "adhar.io/e2e"

// GangSpec is one synthetic Volcano Job.
type GangSpec struct {
	Name, Namespace, Queue, PriorityClass string
	Members                               int
	Member                                Room
	// Seconds is how long each member runs before exiting 0; 0 runs until deleted.
	Seconds int
	// Labels go on the Volcano Job, which Volcano copies to its PodGroup —
	// where the node autoscaler reads adhar.io/workload and adhar.io/scale-up (A2.6).
	Labels map[string]string
}

func quantities(r Room) map[string]interface{} {
	out := map[string]interface{}{}
	if r.MilliCPU > 0 {
		out["cpu"] = resource.NewMilliQuantity(r.MilliCPU, resource.DecimalSI).String()
	}
	if r.Memory > 0 {
		out["memory"] = resource.NewQuantity(r.Memory, resource.BinarySI).String()
	}
	return out
}

func sleeper(seconds int) []interface{} {
	if seconds <= 0 {
		seconds = 86400
	}
	return []interface{}{"sh", "-c", "sleep " + strconv.Itoa(seconds)}
}

func restrictedContainer(name string, r Room, seconds int) map[string]interface{} {
	return map[string]interface{}{
		"name":    name,
		"image":   gangImage,
		"command": sleeper(seconds),
		"resources": map[string]interface{}{
			"requests": quantities(r),
			"limits":   quantities(r),
		},
		"securityContext": map[string]interface{}{
			"allowPrivilegeEscalation": false,
			"runAsNonRoot":             true,
			"runAsUser":                int64(65534),
			"capabilities":             map[string]interface{}{"drop": []interface{}{"ALL"}},
			"seccompProfile":           map[string]interface{}{"type": "RuntimeDefault"},
		},
	}
}

// SyntheticGang is an all-or-nothing Volcano Job of g.Members identical members.
func SyntheticGang(g GangSpec) *unstructured.Unstructured {
	labels := map[string]interface{}{labelE2E: "batch-volcano"}
	for k, v := range g.Labels {
		labels[k] = v
	}
	deadline := int64(3600)
	if g.Seconds > 0 {
		deadline = int64(g.Seconds) + 600
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch.volcano.sh/v1alpha1",
		"kind":       "Job",
		"metadata": map[string]interface{}{
			"name":      g.Name,
			"namespace": g.Namespace,
			"labels":    labels,
		},
		"spec": map[string]interface{}{
			"schedulerName":           VolcanoScheduler,
			"queue":                   g.Queue,
			"priorityClassName":       g.PriorityClass,
			"minAvailable":            int64(g.Members),
			"maxRetry":                int64(0),
			"ttlSecondsAfterFinished": int64(600),
			"tasks": []interface{}{map[string]interface{}{
				"name":     "member",
				"replicas": int64(g.Members),
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{"labels": map[string]interface{}{labelE2E: "batch-volcano", "adhar.io/e2e-gang": g.Name}},
					"spec": map[string]interface{}{
						"schedulerName":                 VolcanoScheduler,
						"priorityClassName":             g.PriorityClass,
						"restartPolicy":                 "Never",
						"activeDeadlineSeconds":         deadline,
						"terminationGracePeriodSeconds": int64(5),
						"enableServiceLinks":            false,
						"automountServiceAccountToken":  false,
						"containers":                    []interface{}{restrictedContainer("member", g.Member, g.Seconds)},
					},
				},
			}},
		},
	}}
}

// Ballast is a pod that takes CPU on one node so a scenario's room is exact.
// It names no scheduler (it is bound by nodeName, as a static claim on the
// node) and runs at the default priority: to Volcano it is a pod some other
// scheduler placed, which is what A2.7 says it must never evict.
func Ballast(name, namespace, node string, cpu Room) *corev1.Pod {
	q := resource.NewMilliQuantity(cpu.MilliCPU, resource.DecimalSI)
	no, yes, nobody := false, true, int64(65534)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{labelE2E: "ballast"}},
		Spec: corev1.PodSpec{
			NodeName:                      node,
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: new(int64),
			EnableServiceLinks:            &no,
			AutomountServiceAccountToken:  &no,
			Containers: []corev1.Container{{
				Name: "ballast", Image: gangImage, Command: []string{"sh", "-c", "sleep 86400"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: *q},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: *q},
				},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, RunAsNonRoot: &yes, RunAsUser: &nobody,
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			}},
		},
	}
}

// Sentinel is a pod kube-scheduler places in a team namespace, at the native
// batch class (the lowest priority on the cluster): if Volcano ever chose a
// victim it did not schedule, this is the one it would choose (A2.7).
// groupName, when set, is the scheduling.k8s.io/group-name annotation that
// puts a default-scheduler pod into Volcano's cache — the stage-1 edge (ST-2),
// which the queue-binding policy refuses once it carries that rule.
func Sentinel(name, namespace, class, groupName string) *corev1.Pod {
	p := Ballast(name, namespace, "", Room{MilliCPU: 10})
	p.Labels = map[string]string{labelE2E: "sentinel"}
	p.Spec.PriorityClassName = class
	p.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("16Mi")
	p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("16Mi")
	if groupName != "" {
		p.Annotations = map[string]string{annotationGroupName: groupName}
	}
	return p
}

// QuotaFree is the smallest free room any of a namespace's ResourceQuotas
// leaves for requests (and, since the canary's pods limit what they request,
// for limits); zero fields mean no quota constrains that resource.
func QuotaFree(quotas []corev1.ResourceQuota) Room {
	var free Room
	take := func(cur *int64, v int64) {
		if *cur == 0 || v < *cur {
			*cur = max(v, 1)
		}
	}
	for _, q := range quotas {
		for _, pair := range []struct {
			cpu bool
			res corev1.ResourceName
		}{{true, "requests.cpu"}, {true, "limits.cpu"}, {false, "requests.memory"}, {false, "limits.memory"}} {
			hard, ok := q.Status.Hard[pair.res]
			if !ok {
				hard, ok = q.Spec.Hard[pair.res]
			}
			if !ok {
				continue
			}
			used := q.Status.Used[pair.res]
			if pair.cpu {
				take(&free.MilliCPU, hard.MilliValue()-used.MilliValue())
			} else {
				take(&free.Memory, hard.Value()-used.Value())
			}
		}
	}
	return free
}

// QueueFree is a leaf's capability less what it has allocated; zero fields
// mean the queue declares no capability for that resource.
func QueueFree(q Queue) Room {
	capa := roomOf(q.Capability)
	if capa.MilliCPU == 0 && capa.Memory == 0 {
		return Room{}
	}
	used := roomOf(q.Allocated)
	free := Room{}
	if capa.MilliCPU > 0 {
		free.MilliCPU = max(capa.MilliCPU-used.MilliCPU, 1)
	}
	if capa.Memory > 0 {
		free.Memory = max(capa.Memory-used.Memory, 1)
	}
	return free
}

// Tighter is the smaller non-zero room per resource.
func Tighter(a, b Room) Room {
	pick := func(x, y int64) int64 {
		switch {
		case x == 0:
			return y
		case y == 0:
			return x
		}
		return min(x, y)
	}
	return Room{MilliCPU: pick(a.MilliCPU, b.MilliCPU), Memory: pick(a.Memory, b.Memory)}
}

// gangName is a synthetic gang's name: the suite's tag keeps a rerun from
// meeting an old one.
func gangName(tag, what string) string { return fmt.Sprintf("e2e-%s-%s", what, tag) }
