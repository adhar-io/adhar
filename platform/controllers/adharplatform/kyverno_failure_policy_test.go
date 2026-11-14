package adharplatform

// Kyverno fails closed for exactly three policies (user decision DEC-4, 2026-11-14;
// design 0025 §10.3). security/kyverno no longer sets forceFailurePolicyIgnore, so
// each policy's own failurePolicy is what its webhook is registered with, and a
// policy that names none gets Kyverno's default: Fail. A Fail webhook that Kyverno
// cannot answer refuses every request it matches, so one forgotten line would put
// whatever that policy matches (often every Pod) behind Kyverno's availability.
//
//   - TestEveryKyvernoPolicyStatesItsFailurePolicy walks every Kyverno policy in
//     the stack and pins Fail on adhar-batch-run-integrity, its Volcano half
//     adhar-batch-run-integrity-volcano (FU-8) and adhar-batch-queue-binding,
//     Ignore on every other one, stated explicitly.
//   - TestComposedKyvernoPoliciesStateTheirFailurePolicy does the same for the
//     policies the control plane composes per XR (rendered, and walked in source).
//   - TestFailClosedPoliciesReachOnlyWhatTheyCheck runs the fail-closed policies'
//     webhook matchConditions on a real API server: a webhook nobody answers,
//     registered Fail with those conditions, must refuse a batch run (native or
//     Volcano) and a pod Volcano schedules, and admit everything else.
// Each guard was mutation-tested (the behaviour reverted, the test seen failing, the
// behaviour restored).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	sigsyaml "sigs.k8s.io/yaml"
)

// failClosedPolicies are the only Kyverno policies whose outage refuses requests.
var failClosedPolicies = map[string]bool{
	"adhar-batch-run-integrity":         true,
	"adhar-batch-run-integrity-volcano": true,
	"adhar-batch-queue-binding":         true,
}

func failClosedNames() []string {
	var out []string
	for k := range failClosedPolicies {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// kyvernoPolicyKinds are the kinds whose webhook failure policy Kyverno 1.19 reads
// from the policy (api/kyverno/v1 Spec.GetFailurePolicy; the CEL policy types'
// GetFailurePolicy), keyed by API group prefix.
var kyvernoPolicyKinds = regexp.MustCompile(`(?m)^kind:\s*(ClusterPolicy|Policy|ValidatingPolicy|NamespacedValidatingPolicy|MutatingPolicy|NamespacedMutatingPolicy|ImageValidatingPolicy|NamespacedImageValidatingPolicy)\s*$`)

var kyvernoPolicyAPI = regexp.MustCompile(`(?m)^apiVersion:\s*(kyverno\.io|policies\.kyverno\.io)/`)

// kyvernoTemplateAction neutralises Go template actions so a seed-time .yaml.tmpl parses.
var kyvernoTemplateAction = regexp.MustCompile(`\{\{[^}]*\}\}`)

type kyvernoPolicy struct {
	file, name, field, value string
}

func stackKyvernoPolicies(t *testing.T) []kyvernoPolicy {
	t.Helper()
	var out []kyvernoPolicy
	for _, f := range stackPackageManifests(t) {
		// The kyverno CLI's copies are generated from the shipped policies, which
		// are checked here; TestQueueBindingFixtureIsTheShippedPolicy and
		// TestBatchRunIntegrityFixtureIsTheShippedPolicy hold them together.
		if strings.Contains(filepath.ToSlash(f), "/policy-tests/") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, chunk := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(raw), -1) {
			if !kyvernoPolicyAPI.MatchString(chunk) || !kyvernoPolicyKinds.MatchString(chunk) {
				continue
			}
			var doc map[string]any
			if err := sigsyaml.Unmarshal([]byte(chunk), &doc); err != nil || doc == nil {
				if err := sigsyaml.Unmarshal([]byte(kyvernoTemplateAction.ReplaceAllString(chunk, "x")), &doc); err != nil || doc == nil {
					t.Errorf("%s: a Kyverno policy that does not parse, so its failurePolicy cannot be read: %v", rel(t, f), err)
					continue
				}
			}
			p := kyvernoPolicy{file: rel(t, f), name: toString(metaName(doc))}
			api := toString(doc["apiVersion"])
			wc := toString(dig(doc, "spec", "webhookConfiguration", "failurePolicy"))
			legacy := toString(dig(doc, "spec", "failurePolicy"))
			switch {
			case strings.HasPrefix(api, "policies.kyverno.io/"):
				p.field, p.value = "spec.failurePolicy", legacy
			case wc != "" && legacy != "":
				// Kyverno refuses the policy outright (validateDeprecatedFields).
				t.Errorf("%s: %s sets both spec.failurePolicy and spec.webhookConfiguration.failurePolicy; Kyverno refuses that", p.file, p.name)
				continue
			case wc != "":
				p.field, p.value = "spec.webhookConfiguration.failurePolicy", wc
			default:
				p.field, p.value = "spec.failurePolicy", legacy
			}
			out = append(out, p)
		}
	}
	return out
}

func TestEveryKyvernoPolicyStatesItsFailurePolicy(t *testing.T) {
	// The flag that used to force Ignore on every webhook is off, at the source and
	// in the rendered admission controller.
	values, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "security", "kyverno", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := sigsyaml.Unmarshal(values, &v); err != nil {
		t.Fatal(err)
	}
	if on, _ := dig(v, "features", "forceFailurePolicyIgnore", "enabled").(bool); on {
		t.Error("security/kyverno values.yaml sets features.forceFailurePolicyIgnore: every policy is registered Ignore and the two batch policies admit everything while Kyverno is down (DEC-4)")
	}
	install, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "security", "kyverno", "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(install), "--forceFailurePolicyIgnore=false") || strings.Contains(string(install), "--forceFailurePolicyIgnore=true") {
		t.Error("security/kyverno manifests/install.yaml does not run the admission controller with --forceFailurePolicyIgnore=false; run its generate-manifests.sh")
	}

	policies := stackKyvernoPolicies(t)
	if len(policies) < 50 {
		t.Fatalf("found %d Kyverno policies in the stack; the walk is missing some", len(policies))
	}
	seen := map[string]bool{}
	for _, p := range policies {
		want := "Ignore"
		if failClosedPolicies[p.name] {
			want = "Fail"
			seen[p.name] = true
		}
		switch {
		case p.value == "":
			t.Errorf("%s: %s states no failurePolicy, so Kyverno registers it Fail and an outage refuses everything it matches; say %s (DEC-4)", p.file, p.name, want)
		case p.value != want:
			t.Errorf("%s: %s is %s %s; want %s (DEC-4: only %v fail closed)", p.file, p.name, p.field, p.value, want, failClosedNames())
		}
	}
	for name := range failClosedPolicies {
		if !seen[name] {
			t.Errorf("the fail-closed policy %s is not in the stack", name)
		}
	}
}

// The control plane composes Kyverno policies per XR (a team's CompliancePolicy,
// through compositions/compliancepolicy and compositions/policy). They reach a
// cluster with a controller release, so they are checked where they are written:
// the go-templating composition is RENDERED for a CompliancePolicy that turns every
// predefined policy on, and every composition source is walked policy by policy, so
// a new composition emitting Kyverno policies is held to the same rule. Each
// composed policy states webhookConfiguration.failurePolicy Ignore, because none of
// them is a batch policy (DEC-4).
func TestComposedKyvernoPoliciesStateTheirFailurePolicy(t *testing.T) {
	// The rendered CompliancePolicy: the Objects' manifests are what Kyverno gets.
	xr := map[string]any{
		"apiVersion": "platform.adhar.io/v1alpha1", "kind": "CompliancePolicy",
		"metadata": map[string]any{"name": "baseline", "namespace": "adhar-system"},
		"spec": map[string]any{"parameters": map[string]any{"enforcementMode": "enforce"}},
	}
	req := map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": xr}}}
	rendered := 0
	for _, d := range renderGoTemplate(t, req, "compositions", "compliancepolicy", "local.yaml") {
		m, _ := dig(d, "spec", "forProvider", "manifest").(map[string]any)
		if m == nil || !kyvernoPolicyAPI.MatchString("apiVersion: "+toString(m["apiVersion"])) {
			continue
		}
		rendered++
		name := toString(metaName(m))
		if got := toString(dig(m, "spec", "webhookConfiguration", "failurePolicy")); got != "Ignore" {
			t.Errorf("compositions/compliancepolicy/local.yaml composes %s with webhookConfiguration.failurePolicy %q; want Ignore (DEC-4: a policy naming none is registered Fail)", name, got)
		}
		if legacy := dig(m, "spec", "failurePolicy"); legacy != nil {
			t.Errorf("compositions/compliancepolicy/local.yaml composes %s with spec.failurePolicy %v as well; Kyverno refuses both", name, legacy)
		}
	}
	if rendered != 4 {
		t.Errorf("a CompliancePolicy with every predefined policy on rendered %d Kyverno policies, want 4", rendered)
	}

	// Every composition source, one policy at a time: the text from one Kyverno
	// apiVersion to the next must state Ignore (YAML or KCL spelling) and never Fail.
	root := filepath.Join(stackRoot(t), "..", "controlplane", "configuration", "compositions")
	declares := regexp.MustCompile(`(?m)^\s*apiVersion\s*[:=]\s*"?(kyverno\.io|policies\.kyverno\.io)/v[0-9a-z]+"?\s*$`)
	ignore := regexp.MustCompile(`(?m)^\s*webhookConfiguration\s*[:=]\s*\{?\s*\n\s*failurePolicy\s*[:=]\s*"?Ignore"?\s*$`)
	celIgnore := regexp.MustCompile(`(?m)^\s*failurePolicy\s*[:=]\s*"?Ignore"?\s*$`)
	fail := regexp.MustCompile(`(?m)^\s*failurePolicy\s*[:=]\s*"?Fail"?\s*$`)
	declared := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(root, path)
		at := declares.FindAllSubmatchIndex(b, -1)
		for i, m := range at {
			end := len(b)
			if i+1 < len(at) {
				end = at[i+1][0]
			}
			block := b[m[0]:end]
			declared++
			states := ignore.Match(block)
			if string(b[m[2]:m[3]]) == "policies.kyverno.io" {
				states = celIgnore.Match(block)
			}
			if !states {
				t.Errorf("%s: Kyverno policy %d of %d states no failurePolicy Ignore, so Kyverno registers it Fail and an outage refuses everything it matches (DEC-4)", r, i+1, len(at))
			}
			if fail.Match(block) {
				t.Errorf("%s: Kyverno policy %d of %d says failurePolicy Fail; only %v fail closed (DEC-4)", r, i+1, len(at), failClosedNames())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if declared < 8 {
		t.Errorf("found %d composed Kyverno policies; compliancepolicy and policy compose 4 each, so the walk is missing some", declared)
	}
}

// failClosedMatchConditions reads the shipped webhook matchConditions of a
// fail-closed policy.
func failClosedMatchConditions(t *testing.T, file, name string) []admissionregistrationv1.MatchCondition {
	t.Helper()
	for _, doc := range readYAMLDocs(t, file) {
		if toString(metaName(doc)) != name {
			continue
		}
		var out []admissionregistrationv1.MatchCondition
		for _, c := range toSlice(dig(doc, "spec", "webhookConfiguration", "matchConditions")) {
			m := c.(map[string]any)
			out = append(out, admissionregistrationv1.MatchCondition{Name: toString(m["name"]), Expression: toString(m["expression"])})
		}
		if len(out) == 0 {
			t.Fatalf("%s has no matchConditions", name)
		}
		return out
	}
	t.Fatalf("no policy %s in %s", name, file)
	return nil
}

func TestFailClosedPoliciesReachOnlyWhatTheyCheck(t *testing.T) {
	batch := filepath.Join(stackPackagesDir(t), "core", "adhar-batch", "manifests")
	// The Volcano Job CRD core/volcano ships, so the Volcano half of run integrity is
	// registered against the real kind.
	var vcJobCRD *apiextensionsv1.CustomResourceDefinition
	for _, doc := range volcanoObjects(t) {
		if volcanoKind(doc) == "CustomResourceDefinition" && volcanoName(doc) == "jobs.batch.volcano.sh" {
			raw, _ := json.Marshal(doc)
			vcJobCRD = &apiextensionsv1.CustomResourceDefinition{}
			if err := json.Unmarshal(raw, vcJobCRD); err != nil {
				t.Fatal(err)
			}
		}
	}
	if vcJobCRD == nil {
		t.Fatal("core/volcano ships no jobs.batch.volcano.sh CRD")
	}
	env := &envtest.Environment{BinaryAssetsDirectory: envtestAssets(t),
		CRDInstallOptions: envtest.CRDInstallOptions{CRDs: []*apiextensionsv1.CustomResourceDefinition{vcJobCRD}}}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	ctx := context.Background()
	admin := kubernetes.NewForConfigOrDie(cfg)
	as := func(user string) kubernetes.Interface {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:masters"}}
		return kubernetes.NewForConfigOrDie(c)
	}
	const ns = "shop-dev"
	if _, err := admin.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := func(name, scheduler string, annotations map[string]string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Annotations: annotations},
			Spec: corev1.PodSpec{SchedulerName: scheduler, RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{Name: "batch", Image: "docker.io/library/busybox:1.36"}}},
		}
	}
	// What a controller owns: the controller ownerReference the Job and CronJob
	// controllers set on what they create.
	yes := true
	ownedBy := func(apiVersion, kind string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: "owner", UID: "00000000-0000-0000-0000-000000000001", Controller: &yes}}
	}
	ownedPod := func(name string, owners []metav1.OwnerReference) *corev1.Pod {
		p := pod(name, "volcano", map[string]string{"scheduling.volcano.sh/queue-name": "acme-payments-dev"})
		p.OwnerReferences = owners
		return p
	}
	// A job-controller pod still carrying the job tracking finalizer, which the
	// controller removes with an UPDATE once the pod has finished.
	tracked := ownedPod("tracked-before", ownedBy("batch/v1", "Job"))
	tracked.Finalizers = []string{"batch.kubernetes.io/job-tracking"}
	// Pods that exist before the webhook does, for the UPDATE cases.
	for _, p := range []*corev1.Pod{pod("plain-before", "", nil), pod("annotated-before", "", map[string]string{"scheduling.volcano.sh/queue-name": "acme-payments-dev"}), tracked} {
		if _, err := admin.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	// One webhook per fail-closed policy, registered as Kyverno registers it (Fail,
	// the policy's matchConditions) and pointing at a port nothing listens on: an
	// outage. Whatever reaches it is refused.
	fail := admissionregistrationv1.Fail
	none := admissionregistrationv1.SideEffectClassNone
	timeout := int32(2)
	url := "https://127.0.0.1:9/kyverno-is-down"
	hook := func(name string, rule admissionregistrationv1.RuleWithOperations, conds []admissionregistrationv1.MatchCondition) admissionregistrationv1.ValidatingWebhook {
		return admissionregistrationv1.ValidatingWebhook{
			Name: name + ".kyverno.test", ClientConfig: admissionregistrationv1.WebhookClientConfig{URL: &url},
			Rules: []admissionregistrationv1.RuleWithOperations{rule}, FailurePolicy: &fail, SideEffects: &none,
			AdmissionReviewVersions: []string{"v1"}, TimeoutSeconds: &timeout, MatchConditions: conds,
		}
	}
	rule := func(group, version, resource string, ops ...admissionregistrationv1.OperationType) admissionregistrationv1.RuleWithOperations {
		return admissionregistrationv1.RuleWithOperations{Operations: ops, Rule: admissionregistrationv1.Rule{
			APIGroups: []string{group}, APIVersions: []string{version}, Resources: strings.Split(resource, ",")}}
	}
	queueBinding := failClosedMatchConditions(t, filepath.Join(batch, "volcano", "kyverno-queue.yaml"), "adhar-batch-queue-binding")
	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "kyverno-outage"},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{
			hook("adhar-batch-queue-binding", rule("", "v1", "pods", admissionregistrationv1.Create, admissionregistrationv1.Update), queueBinding),
			// The same policy's batch/v1 rules (a single run on Volcano), as Kyverno
			// registers a policy naming several groups.
			hook("adhar-batch-queue-binding-batch", rule("batch", "v1", "jobs,cronjobs", admissionregistrationv1.Create, admissionregistrationv1.Update), queueBinding),
			hook("adhar-batch-run-integrity", rule("batch", "v1", "jobs", admissionregistrationv1.Create),
				failClosedMatchConditions(t, filepath.Join(batch, "kyverno-run-integrity.yaml"), "adhar-batch-run-integrity")),
			hook("adhar-batch-run-integrity-volcano", rule("batch.volcano.sh", "v1alpha1", "jobs", admissionregistrationv1.Create),
				failClosedMatchConditions(t, filepath.Join(batch, "volcano", "kyverno-run-integrity.yaml"), "adhar-batch-run-integrity-volcano")),
		},
	}
	// The API server compiles every expression here; a CEL error is refused.
	if _, err := admin.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, vwc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("the API server refuses the policies' matchConditions: %v", err)
	}

	dry := metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
	refused := func(err error) bool { return err != nil && strings.Contains(err.Error(), "kyverno-is-down") }
	expect := func(what string, wantRefused bool, err error) {
		t.Helper()
		if refused(err) != wantRefused {
			t.Errorf("%s: refused=%v (err %v), want refused=%v", what, refused(err), err, wantRefused)
		} else if err != nil && !wantRefused {
			t.Errorf("%s: %v", what, err)
		}
	}
	// The webhook configuration is read through an informer; wait until it bites.
	deadline := 0
	for ; deadline < 100; deadline++ {
		_, err := admin.CoreV1().Pods(ns).Create(ctx, pod("probe", "volcano", nil), dry)
		if refused(err) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if deadline == 100 {
		t.Fatal("the outage webhook never took effect")
	}

	// adhar-batch-queue-binding: only what Volcano schedules waits for Kyverno.
	_, err = admin.CoreV1().Pods(ns).Create(ctx, pod("plain", "", nil), dry)
	expect("a plain pod", false, err)
	_, err = admin.CoreV1().Pods(ns).Create(ctx, pod("other-scheduler", "default-scheduler", map[string]string{"example.com/x": "y"}), dry)
	expect("a pod on kube-scheduler with other annotations", false, err)
	_, err = admin.CoreV1().Pods(ns).Create(ctx, pod("volcano", "volcano", nil), dry)
	expect("a pod naming schedulerName volcano", true, err)
	_, err = admin.CoreV1().Pods(ns).Create(ctx, pod("queue-annotated", "", map[string]string{"scheduling.volcano.sh/queue-name": "acme-ledger-dev"}), dry)
	expect("a pod naming a Volcano queue", true, err)
	_, err = admin.CoreV1().Pods(ns).Create(ctx, pod("group-annotated", "", map[string]string{"scheduling.k8s.io/group-name": "settlement-x"}), dry)
	expect("a pod naming a PodGroup", true, err)
	_, err = as("system:serviceaccount:adhar-system:volcano-controllers").CoreV1().Pods(ns).Create(ctx, pod("controllers", "volcano", nil), dry)
	expect("Volcano's controllers creating a gang member", false, err)

	update := func(name string, mutate func(*corev1.Pod)) error {
		p, err := admin.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		mutate(p)
		_, err = admin.CoreV1().Pods(ns).Update(ctx, p, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
		return err
	}
	expect("labelling a plain pod", false, update("plain-before", func(p *corev1.Pod) { p.Labels = map[string]string{"a": "b"} }))
	expect("annotating a plain pod onto a queue", true, update("plain-before", func(p *corev1.Pod) {
		p.Annotations = map[string]string{"scheduling.volcano.sh/queue-name": "acme-ledger-dev"}
	}))
	expect("removing a pod's queue annotation", true, update("annotated-before", func(p *corev1.Pod) { p.Annotations = nil }))

	// ...nor what Kubernetes' Job and CronJob controllers create for a batch/v1 Job
	// or CronJob whose template the binding checked (a scheduled single run on
	// Volcano). Only for that owner: `system:kube-controller-manager` is every
	// controller without per-controller credentials.
	const jobController, cronJobController, kcm = "system:serviceaccount:kube-system:job-controller", "system:serviceaccount:kube-system:cronjob-controller", "system:kube-controller-manager"
	for _, controller := range []string{jobController, kcm} {
		_, err = as(controller).CoreV1().Pods(ns).Create(ctx, ownedPod("job-pod", ownedBy("batch/v1", "Job")), dry)
		expect(controller+" creating a pod of a batch/v1 Job on Volcano", false, err)
		_, err = as(controller).CoreV1().Pods(ns).Create(ctx, ownedPod("unowned", nil), dry)
		expect(controller+" creating a Volcano pod nothing owns", true, err)
		_, err = as(controller).CoreV1().Pods(ns).Create(ctx, ownedPod("rs-pod", ownedBy("apps/v1", "ReplicaSet")), dry)
		expect(controller+" creating a ReplicaSet's Volcano pod", true, err)
		_, err = as(controller).CoreV1().Pods(ns).Create(ctx, ownedPod("vc-pod", ownedBy("batch.volcano.sh/v1alpha1", "Job")), dry)
		expect(controller+" creating a Volcano Job's pod", true, err)
	}
	notController := ownedPod("owned-not-controlled", ownedBy("batch/v1", "Job"))
	notController.OwnerReferences[0].Controller = nil
	_, err = as(jobController).CoreV1().Pods(ns).Create(ctx, notController, dry)
	expect("a Volcano pod a batch/v1 Job owns but does not control", true, err)
	_, err = admin.CoreV1().Pods(ns).Create(ctx, ownedPod("forged-owner", ownedBy("batch/v1", "Job")), dry)
	expect("a person's Volcano pod claiming a batch/v1 Job as its owner", true, err)
	p, err := as(jobController).CoreV1().Pods(ns).Get(ctx, "tracked-before", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p.Finalizers = nil
	_, err = as(jobController).CoreV1().Pods(ns).Update(ctx, p, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
	expect("the job-controller removing its tracking finalizer from a finished Volcano pod", false, err)

	// adhar-batch-run-integrity: only batch runs wait for Kyverno.
	job := func(name string, labels map[string]string) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{Name: "batch", Image: "docker.io/library/busybox:1.36"}}}}},
		}
	}
	_, err = admin.BatchV1().Jobs(ns).Create(ctx, job("hook", map[string]string{"app.kubernetes.io/name": "x"}), dry)
	expect("a Job that is not a batch run (an Argo CD hook, a composed Job)", false, err)
	_, err = admin.BatchV1().Jobs(ns).Create(ctx, job("manual-run", map[string]string{"adhar.io/batch-job": "settlement"}), dry)
	expect("a person's batch run", true, err)
	for _, controller := range []string{"system:serviceaccount:kube-system:cronjob-controller", "system:kube-controller-manager", "system:serviceaccount:adhar-system:volcano-controllers"} {
		_, err = as(controller).BatchV1().Jobs(ns).Create(ctx, job("scheduled-run", map[string]string{"adhar.io/batch-job": "settlement"}), dry)
		expect("a scheduled run created by "+controller, false, err)
	}

	// The batch/v1 Jobs and CronJobs of a single run on Volcano: checked on their
	// template, so they wait for Kyverno; any other Job or CronJob does not.
	volcanoTemplate := func(t *corev1.PodTemplateSpec, scheduler string, annotations map[string]string) {
		t.Spec.SchedulerName, t.Annotations = scheduler, annotations
	}
	single := job("single", nil)
	volcanoTemplate(&single.Spec.Template, "volcano", nil)
	_, err = admin.BatchV1().Jobs(ns).Create(ctx, single, dry)
	expect("a person's Job whose pods Volcano schedules", true, err)
	queued := job("queued", nil)
	volcanoTemplate(&queued.Spec.Template, "", map[string]string{"scheduling.volcano.sh/queue-name": "acme-ledger-dev"})
	_, err = admin.BatchV1().Jobs(ns).Create(ctx, queued, dry)
	expect("a person's Job whose pod template names a Volcano queue", true, err)
	grouped := job("grouped", nil)
	volcanoTemplate(&grouped.Spec.Template, "", map[string]string{"scheduling.k8s.io/group-name": "settlement-x"})
	_, err = admin.BatchV1().Jobs(ns).Create(ctx, grouped, dry)
	expect("a person's Job whose pod template names a PodGroup", true, err)
	_, err = admin.BatchV1().Jobs(ns).Create(ctx, job("plain-job", nil), dry)
	expect("a Job kube-scheduler runs", false, err)
	for _, controller := range []string{cronJobController, kcm} {
		fired := job("fired", nil)
		volcanoTemplate(&fired.Spec.Template, "volcano", map[string]string{"scheduling.volcano.sh/queue-name": "acme-payments-dev"})
		fired.OwnerReferences = ownedBy("batch/v1", "CronJob")
		_, err = as(controller).BatchV1().Jobs(ns).Create(ctx, fired, dry)
		expect(controller+" creating the Job a batch/v1 CronJob on Volcano fires", false, err)
	}
	cronJob := func(name, scheduler string) *batchv1.CronJob {
		c := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: batchv1.CronJobSpec{Schedule: "30 1 * * *",
			JobTemplate: batchv1.JobTemplateSpec{Spec: job(name, nil).Spec}}}
		c.Spec.JobTemplate.Spec.Template.Spec.SchedulerName = scheduler
		return c
	}
	_, err = admin.BatchV1().CronJobs(ns).Create(ctx, cronJob("single-schedule", "volcano"), dry)
	expect("a CronJob whose pods Volcano schedules", true, err)
	_, err = admin.BatchV1().CronJobs(ns).Create(ctx, cronJob("plain-schedule", ""), dry)
	expect("a CronJob kube-scheduler runs", false, err)

	// adhar-batch-run-integrity-volcano: only Volcano batch runs wait for Kyverno.
	vcJobs := schema.GroupVersionResource{Group: "batch.volcano.sh", Version: "v1alpha1", Resource: "jobs"}
	vcJob := func(name string, labels map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "batch.volcano.sh/v1alpha1", "kind": "Job",
			"metadata": map[string]any{"name": name, "namespace": ns, "labels": labels},
			"spec": map[string]any{"schedulerName": "volcano", "minAvailable": int64(1), "tasks": []any{map[string]any{
				"name": "member", "replicas": int64(1), "template": map[string]any{"spec": map[string]any{"restartPolicy": "Never",
					"containers": []any{map[string]any{"name": "batch", "image": "docker.io/library/busybox:1.36"}}}}}}},
		}}
	}
	vcCreate := func(user string, j *unstructured.Unstructured) error {
		c := rest.CopyConfig(cfg)
		if user != "" {
			c.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:masters"}}
		}
		_, err := dynamic.NewForConfigOrDie(c).Resource(vcJobs).Namespace(ns).Create(ctx, j, dry)
		return err
	}
	expect("a Volcano Job that is not a batch run", false, vcCreate("", vcJob("gang", map[string]any{"app.kubernetes.io/name": "x"})))
	expect("a person's Volcano batch run", true, vcCreate("", vcJob("manual-gang", map[string]any{"adhar.io/batch-job": "reconciler"})))
	expect("a scheduled Volcano run created by Volcano's controllers", false,
		vcCreate("system:serviceaccount:adhar-system:volcano-controllers", vcJob("scheduled-gang", map[string]any{"adhar.io/batch-job": "reconciler"})))
}
