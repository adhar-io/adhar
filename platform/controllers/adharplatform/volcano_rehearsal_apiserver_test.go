package adharplatform

// core/volcano's scheduler and controllers, run for real against a real
// kube-apiserver (envtest, RBAC on) as their own ServiceAccounts, with the
// shipped ClusterRoles, CRDs, scheduler configuration and root queue applied.
//
// Reading the YAML cannot show that a narrowed ClusterRole still covers every
// call the binaries make, or that the scheduler configuration really reclaims
// from a dev gang for prod while leaving a pod it did not schedule alone. This
// does both, offline, in about a minute:
//
//   - every request either binary makes is authorised: nothing in their logs
//     says "forbidden" from start-up through a gang being bound, a queue being
//     opened, and pods being evicted;
//   - the rules that were removed stay removed (secrets, CRD writes, leases,
//     node writes, the controllers' ConfigMap/Service/NetworkPolicy writes);
//   - A2.4 / A2.7 in miniature: a dev gang fills a node beside a pod
//     kube-scheduler placed; a prod run below its deserved share arrives; the
//     scheduler evicts dev pods, never the other one. Every run pod carries the
//     batch PriorityClass the platform SHIPS, because Volcano will not reclaim
//     or preempt for a pod whose preemptionPolicy is Never, and a rehearsal on a
//     class of its own would pass where the platform cannot.
//
// The Volcano binaries are not vendored. Build them from the pinned tag and
// point VOLCANO_BIN_DIR at them; without it the test is skipped:
//
//	curl -fsSL https://github.com/volcano-sh/volcano/archive/refs/tags/v1.16.0.tar.gz | tar xz
//	cd volcano-1.16.0 && for c in scheduler controller-manager; do
//	  CGO_ENABLED=0 go build -o "$BIN/vc-$c" ./cmd/$c; done
//	VOLCANO_BIN_DIR=$BIN go test ./platform/controllers/adharplatform/ -run TestVolcanoComponentsRunOnTheirNarrowedRBAC -v
//
// No kubelet runs here, so a bound pod is marked Running by the test, as a
// kubelet would, and an evicted one is recognised by its deletionTimestamp.

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"adhar-io/adhar/api/v1alpha1"
)

// syncBuffer collects a child process's log while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// volcanoKubeconfig writes a kubeconfig that authenticates as the test's
// administrator and impersonates the component's ServiceAccount, so every call
// the binary makes is authorised against the ClusterRole bound to that account.
func volcanoKubeconfig(t *testing.T, cfg *rest.Config, serviceAccount string) string {
	t.Helper()
	conf := clientcmdapi.NewConfig()
	conf.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	conf.AuthInfos["sa"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: cfg.CertData,
		ClientKeyData:         cfg.KeyData,
		Token:                 cfg.BearerToken,
		Impersonate:           "system:serviceaccount:adhar-system:" + serviceAccount,
		ImpersonateGroups:     []string{"system:serviceaccounts", "system:serviceaccounts:adhar-system", "system:authenticated"},
	}
	conf.Contexts["sa"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "sa"}
	conf.CurrentContext = "sa"
	path := filepath.Join(t.TempDir(), serviceAccount+".kubeconfig")
	if err := clientcmd.WriteToFile(*conf, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// volcanoComponentArgs returns the shipped container args, minus what only makes
// sense in a pod (the shell redirect, stderr logging) and with every listener
// moved to a free loopback port.
func volcanoComponentArgs(t *testing.T, deployment string, replace map[string]string) []string {
	t.Helper()
	var out []string
	for _, a := range volcanoArgs(t, volcanoDeployments(t)[deployment]) {
		if a == "2>&1" || a == "--logtostderr" {
			continue
		}
		flag, _, _ := strings.Cut(a, "=")
		if v, ok := replace[flag]; ok {
			out = append(out, flag+"="+v)
			delete(replace, flag)
			continue
		}
		out = append(out, a)
	}
	for flag, v := range replace {
		out = append(out, flag+"="+v)
	}
	return out
}

func startVolcanoComponent(t *testing.T, ctx context.Context, binary string, args []string, env []string) *syncBuffer {
	t.Helper()
	log := &syncBuffer{}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", binary, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return log
}

type volcanoRehearsal struct {
	ctx    context.Context
	admin  kubernetes.Interface
	dyn    dynamic.Interface
	mapper meta.RESTMapper
}

func (r *volcanoRehearsal) create(t *testing.T, obj map[string]any) {
	t.Helper()
	u := &unstructured.Unstructured{Object: obj}
	gvk := u.GroupVersionKind()
	var mapping *meta.RESTMapping
	deadline := time.Now().Add(30 * time.Second)
	for {
		var err error
		mapping, err = r.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no API for %s: %v", gvk, err)
		}
		if m, ok := r.mapper.(meta.ResettableRESTMapper); ok {
			m.Reset()
		}
		time.Sleep(200 * time.Millisecond)
	}
	var err error
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := u.GetNamespace()
		if ns == "" {
			ns = "adhar-system"
		}
		_, err = r.dyn.Resource(mapping.Resource).Namespace(ns).Create(r.ctx, u, metav1.CreateOptions{})
	} else {
		_, err = r.dyn.Resource(mapping.Resource).Create(r.ctx, u, metav1.CreateOptions{})
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating %s %s: %v", gvk.Kind, u.GetName(), err)
	}
}

var vcQueueGVR = schema.GroupVersionResource{Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "queues"}

func vcQueue(name string, reclaimable bool, deserved map[string]any) map[string]any {
	spec := map[string]any{"parent": "adhar-batch", "reclaimable": reclaimable, "weight": 1,
		"capability": map[string]any{"cpu": "4", "memory": "8Gi"}}
	if deserved != nil {
		spec["deserved"] = deserved
	}
	return map[string]any{"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "Queue",
		"metadata": map[string]any{"name": name}, "spec": spec}
}

// volcanoRunPriorityClass is the class the BatchJob composition gives a Volcano
// run that is not `priority: critical` (design 0025 §4.4). The rehearsal creates
// it from the stack, never from a definition of its own (2026-11-14): Volcano
// v1.16.0 skips a reclaimer whose pod is preemptionPolicy Never
// (actions/reclaim/reclaim.go) and refuses it as a preemptor
// (actions/preempt/preempt.go), and the Priority admission plugin copies the
// class's policy onto every pod. With the native classes at Never, prod cannot
// reclaim from dev; rehearsed with pods that named no class, it could. So a Volcano
// run gets the Volcano class (user decision DEC-1, 2026-11-14): adhar-batch-volcano,
// PreemptLowerPriority, the same value as adhar-batch.
const volcanoRunPriorityClass = "adhar-batch-volcano"

// vcGang is the shape design 0025 §4.5 composes: minAvailable is every task.
func vcGang(ns, name, queue string, tasks int, cpu string) map[string]any {
	return map[string]any{
		"apiVersion": "batch.volcano.sh/v1alpha1", "kind": "Job",
		"metadata": map[string]any{"name": name, "namespace": ns, "labels": map[string]any{"adhar.io/workload": "batch"}},
		"spec": map[string]any{
			"schedulerName": "volcano", "queue": queue, "minAvailable": tasks, "maxRetry": 3,
			"plugins":  map[string]any{"env": []any{}},
			"policies": []any{map[string]any{"events": []any{"PodEvicted", "PodFailed"}, "action": "RestartJob"}},
			"tasks": []any{map[string]any{
				"name": "worker", "replicas": tasks,
				"template": map[string]any{"spec": map[string]any{
					"restartPolicy":     "Never",
					"priorityClassName": volcanoRunPriorityClass,
					"containers": []any{map[string]any{
						"name": "run", "image": "registry.k8s.io/pause:3.10",
						"resources": map[string]any{
							"requests": map[string]any{"cpu": cpu, "memory": "256Mi"},
							"limits":   map[string]any{"memory": "256Mi"},
						},
					}},
				}},
			}},
		},
	}
}

func TestVolcanoComponentsRunOnTheirNarrowedRBAC(t *testing.T) {
	bin := os.Getenv("VOLCANO_BIN_DIR")
	if bin == "" {
		t.Skip("VOLCANO_BIN_DIR is unset; see the comment at the top of this file to build the v1.16.0 binaries")
	}
	for _, b := range []string{"vc-scheduler", "vc-controller-manager"} {
		if _, err := os.Stat(filepath.Join(bin, b)); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
	}

	env := &envtest.Environment{BinaryAssetsDirectory: envtestAssets(t)}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	admin := kubernetes.NewForConfigOrDie(cfg)
	r := &volcanoRehearsal{ctx: ctx, admin: admin, dyn: dynamic.NewForConfigOrDie(cfg),
		mapper: restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(admin.Discovery()))}
	for _, ns := range []string{"adhar-system", "acme-payments-dev", "acme-payments-prod", "acme-payments-svc"} {
		if _, err := admin.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	// The package as shipped, minus what needs a pod network (the admission
	// webhooks: the API server here cannot reach a Service) and the workloads
	// this test runs as processes instead.
	var crds, others []map[string]any
	var schedulerConf string
	for _, doc := range volcanoObjects(t) {
		switch volcanoKind(doc) {
		case "Deployment", "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
			continue
		case "CustomResourceDefinition":
			crds = append(crds, doc)
			continue
		case "ConfigMap":
			if volcanoName(doc) == "volcano-scheduler-configmap" {
				schedulerConf, _ = dig(doc, "data", "volcano-scheduler.conf").(string)
			}
		}
		others = append(others, doc)
	}
	for _, doc := range append(crds, others...) {
		r.create(t, doc)
	}
	if schedulerConf == "" {
		t.Fatal("no volcano-scheduler.conf in the manifest")
	}
	// The batch PriorityClasses, from whichever package ships them.
	shipped := false
	for _, f := range stackPackageManifests(t) {
		for _, doc := range yamlDocs(t, f) {
			if name, _ := metaName(doc).(string); doc["kind"] == "PriorityClass" && strings.HasPrefix(name, "adhar-batch") {
				r.create(t, doc)
				shipped = shipped || name == volcanoRunPriorityClass
			}
		}
	}
	if !shipped {
		t.Fatalf("no stack package ships the PriorityClass %s a Volcano run is given", volcanoRunPriorityClass)
	}
	confPath := filepath.Join(t.TempDir(), "volcano-scheduler.conf")
	if err := os.WriteFile(confPath, []byte(schedulerConf), 0o600); err != nil {
		t.Fatal(err)
	}

	// The rules the generator removed must stay removed: each would be a way out
	// of Volcano's own lane.
	for sa, forbidden := range map[string][]authorizationCheck{
		"volcano-controllers": {
			{verb: "get", resource: "secrets", namespace: "adhar-system"},
			{verb: "update", resource: "configmaps", namespace: "adhar-system", name: "volcano-scheduler-configmap"},
			{verb: "create", resource: "configmaps", namespace: "kube-system"},
			{verb: "delete", resource: "services", namespace: "adhar-system"},
			{verb: "create", group: "networking.k8s.io", resource: "networkpolicies", namespace: "acme-payments-prod"},
			{verb: "create", group: "apiextensions.k8s.io", resource: "customresourcedefinitions"},
			{verb: "update", group: "coordination.k8s.io", resource: "leases", namespace: "kube-system"},
		},
		"volcano-scheduler": {
			{verb: "patch", resource: "nodes"},
			{verb: "update", resource: "configmaps", namespace: "adhar-system"},
			{verb: "delete", group: "apiextensions.k8s.io", resource: "customresourcedefinitions"},
			{verb: "update", group: "coordination.k8s.io", resource: "leases", namespace: "kube-system"},
		},
		"volcano-admission": {
			{verb: "get", resource: "secrets", namespace: "adhar-system"},
			{verb: "update", group: "admissionregistration.k8s.io", resource: "validatingwebhookconfigurations", name: "kyverno-resource-validating-webhook-cfg"},
		},
	} {
		for _, c := range forbidden {
			if allowed := c.allowed(t, ctx, admin, "system:serviceaccount:adhar-system:"+sa); allowed {
				t.Errorf("%s may %s; the narrowed ClusterRole must not grant it", sa, c)
			}
		}
	}

	schedLog := startVolcanoComponent(t, ctx, filepath.Join(bin, "vc-scheduler"),
		volcanoComponentArgs(t, "volcano-scheduler", map[string]string{
			"--kubeconfig":      volcanoKubeconfig(t, cfg, "volcano-scheduler"),
			"--scheduler-conf":  confPath,
			"--healthz-address": freeAddr(t),
			"--listen-address":  freeAddr(t),
		}), []string{"DEBUG_SOCKET_DIR=" + t.TempDir()})
	ctrlLog := startVolcanoComponent(t, ctx, filepath.Join(bin, "vc-controller-manager"),
		volcanoComponentArgs(t, "volcano-controllers", map[string]string{
			"--kubeconfig":      volcanoKubeconfig(t, cfg, "volcano-controllers"),
			"--healthz-address": freeAddr(t),
			"--listen-address":  freeAddr(t),
		}), []string{"KUBE_POD_NAMESPACE=adhar-system"})
	defer func() {
		if t.Failed() {
			t.Logf("vc-scheduler log:\n%s", tail(schedLog.String(), 80))
			t.Logf("vc-controller-manager log:\n%s", tail(ctrlLog.String(), 80))
		}
	}()

	// One 4-CPU worker.
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1", Labels: map[string]string{"kubernetes.io/hostname": "worker-1"}}}
	if node, err = admin.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// The API server's TaintNodesByCondition admission marks a new node
	// not-ready; no node controller runs here to lift it.
	node.Spec.Taints = nil
	if node, err = admin.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	capacity := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi"), corev1.ResourcePods: resource.MustParse("110")}
	node.Status = corev1.NodeStatus{Capacity: capacity, Allocatable: capacity,
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: metav1.Now()}}}
	if _, err := admin.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	// A pod kube-scheduler placed, holding one CPU: Volcano must never take it.
	svc := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: "acme-payments-svc"},
		Spec: corev1.PodSpec{NodeName: "worker-1", Containers: []corev1.Container{{Name: "api", Image: "registry.k8s.io/pause:3.10",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("256Mi")}}}}}}
	if svc, err = admin.CoreV1().Pods(svc.Namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	markRunning(t, ctx, admin, svc)

	// The tree the Team composition composes: the shipped root, a reclaimable dev
	// leaf and a prod leaf with a deserved share (scaled to this one-node
	// cluster). The dev leaf starts WITH a share it is inside of, so the first
	// thing shown is that prod does not take from a leaf that is within its own.
	// The root queue as a platform is seeded (D17; the unsized fallback).
	rootQueue := volcanoRootQueue(t, v1alpha1.BuildCustomizationSpec{})
	waitFor(t, 60*time.Second, "the scheduler to create the root queue", func() bool {
		_, err := r.dyn.Resource(vcQueueGVR).Get(ctx, "root", metav1.GetOptions{})
		return err == nil
	})
	r.create(t, rootQueue)
	r.create(t, vcQueue("acme-payments-dev", true, map[string]any{"cpu": "3", "memory": "4Gi"}))
	r.create(t, vcQueue("acme-payments-prod", false, map[string]any{"cpu": "1", "memory": "2Gi"}))
	waitFor(t, 60*time.Second, "the controllers to open the leaf queues", func() bool {
		for _, q := range []string{"adhar-batch", "acme-payments-dev", "acme-payments-prod"} {
			o, err := r.dyn.Resource(vcQueueGVR).Get(ctx, q, metav1.GetOptions{})
			if err != nil {
				return false
			}
			if s, _ := dig(o.Object, "status", "state").(string); s != "Open" {
				return false
			}
		}
		return true
	})

	// A dev gang of three fills the rest of the node.
	r.create(t, vcGang("acme-payments-dev", "nightly", "acme-payments-dev", 3, "1"))
	var devPods []corev1.Pod
	waitFor(t, 90*time.Second, "the dev gang to be created and bound whole", func() bool {
		pods, err := admin.CoreV1().Pods("acme-payments-dev").List(ctx, metav1.ListOptions{})
		if err != nil || len(pods.Items) != 3 {
			return false
		}
		for _, p := range pods.Items {
			if p.Spec.NodeName == "" {
				return false
			}
		}
		devPods = pods.Items
		return true
	})
	for i := range devPods {
		markRunning(t, ctx, admin, &devPods[i])
	}

	// A2.4 runs as a subtest so a failure there still leaves the authorisation
	// check below to run: the two fail for unrelated reasons.
	t.Run("A2.4 prod reclaims from a dev gang, A2.7 never from a pod Volcano did not schedule", func(t *testing.T) {
		reclaimFromDev(t, r, admin)
	})

	// Every call either binary made was authorised.
	for name, log := range map[string]*syncBuffer{"vc-scheduler": schedLog, "vc-controller-manager": ctrlLog} {
		for _, line := range strings.Split(log.String(), "\n") {
			if strings.Contains(strings.ToLower(line), "forbidden") {
				t.Errorf("%s was refused a call its ClusterRole should allow: %s", name, line)
			}
		}
	}
}

func reclaimFromDev(t *testing.T, r *volcanoRehearsal, admin kubernetes.Interface) {
	ctx := r.ctx
	// Prod, below its deserved share, needs room the dev gang holds.
	r.create(t, vcGang("acme-payments-prod", "settlement", "acme-payments-prod", 1, "1500m"))
	devEvicted := func() bool {
		pods, err := admin.CoreV1().Pods("acme-payments-dev").List(ctx, metav1.ListOptions{})
		if err != nil {
			return false
		}
		for _, p := range pods.Items {
			if p.DeletionTimestamp != nil {
				return true
			}
		}
		return len(pods.Items) < 3
	}
	// The scheduler runs a session every second; fifteen is many chances to
	// get this wrong. With gang's reclaim vote turned off, conformance alone
	// decides and this dev gang IS evicted (values.yaml, rehearsed 2026-11-14).
	time.Sleep(15 * time.Second)
	if devEvicted() {
		t.Fatal("prod reclaimed from a dev leaf that holds no more than its deserved share; only the capacity plugin may pick reclaim victims (values.yaml scheduler_config_override)")
	}

	// Design 0025 §4.3: dev leaves have no deserved share, so whatever they hold
	// is over it, and reclaimable.
	dev, err := r.dyn.Resource(vcQueueGVR).Get(ctx, "acme-payments-dev", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	unstructured.RemoveNestedField(dev.Object, "spec", "deserved")
	if _, err := r.dyn.Resource(vcQueueGVR).Update(ctx, dev, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 90*time.Second, "prod to reclaim from the dev gang (A2.4; a run whose PriorityClass is preemptionPolicy Never cannot reclaim)", devEvicted)
	got, err := admin.CoreV1().Pods("acme-payments-svc").Get(ctx, "payments-api", metav1.GetOptions{})
	if err != nil || got.DeletionTimestamp != nil {
		t.Errorf("Volcano evicted a pod it did not schedule (A2.7): err=%v deletionTimestamp=%v", err, got.DeletionTimestamp)
	}
}

func markRunning(t *testing.T, ctx context.Context, c kubernetes.Interface, p *corev1.Pod) {
	t.Helper()
	for i := 0; i < 5; i++ {
		cur, err := c.CoreV1().Pods(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		cur.Status.Phase = corev1.PodRunning
		cur.Status.Conditions = append(cur.Status.Conditions, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
		if _, err = c.CoreV1().Pods(p.Namespace).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err == nil {
			return
		} else if !apierrors.IsConflict(err) {
			t.Fatal(err)
		}
	}
	t.Fatalf("could not mark %s/%s Running", p.Namespace, p.Name)
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

type authorizationCheck struct{ verb, group, resource, namespace, name string }

func (c authorizationCheck) String() string {
	s := c.verb + " " + c.resource
	if c.group != "" {
		s += "." + c.group
	}
	if c.name != "" {
		s += "/" + c.name
	}
	if c.namespace != "" {
		s += " in " + c.namespace
	}
	return s
}

func (c authorizationCheck) allowed(t *testing.T, ctx context.Context, admin kubernetes.Interface, user string) bool {
	t.Helper()
	review, err := admin.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   user,
			Groups: []string{"system:serviceaccounts", "system:serviceaccounts:adhar-system", "system:authenticated"},
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb: c.verb, Group: c.group, Resource: c.resource, Namespace: c.namespace, Name: c.name,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("SubjectAccessReview for %s: %v", c, err)
	}
	return review.Status.Allowed
}
