package adharplatform

// D26 (design 0025 §5.12): a BatchJob names a Bucket of its namespace, and every run
// pod gets the five ADHAR_S3_* variables the kit's AdharS3EnvironmentPostProcessor
// turns into Spring Cloud AWS's settings: endpoint, region and bucket from the
// Bucket's connection Secret <bucket>-bucket, the credential from root-creds projected
// beside the job by an ExternalSecret through ClusterSecretStore
// platform-object-storage (R5: it is RustFS's root). Rendered in-process with the
// same template language, through `crossplane render` when Docker is available, and
// held to the run-integrity policy (core/adhar-batch policy-tests/bucket-run-integrity).
// Each guard was mutation-tested: the behaviour reverted in the composition, the test
// seen failing, the fix restored.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

const bkFixtureDir = "testdata/batchjob-bucket"

// bkBucket is a Bucket XR as the Bucket composition leaves it; published "" leaves
// status.connectionSecret unset (a Bucket not yet reconciled).
func bkBucket(namespace, name, published string) map[string]any {
	status := map[string]any{"bucket": name, "endpoint": "http://rustfs.adhar-system.svc.cluster.local:9000"}
	if published != "" {
		status["connectionSecret"] = published
	}
	return map[string]any{"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Bucket",
		"metadata": map[string]any{"name": name, "namespace": namespace}, "status": status}
}

// bkWantEnv is what every run pod of job `name` reading Bucket `bucket` carries.
func bkWantEnv(name, bucket string) map[string]any {
	ref := func(secret, key string) any {
		return map[string]any{"secretKeyRef": map[string]any{"name": secret, "key": key}}
	}
	return map[string]any{
		"ADHAR_S3_ENDPOINT":   ref(bucket+"-bucket", "endpoint"),
		"ADHAR_S3_REGION":     ref(bucket+"-bucket", "region"),
		"ADHAR_S3_BUCKET":     ref(bucket+"-bucket", "bucket"),
		"ADHAR_S3_ACCESS_KEY": ref(name+"-s3", "ADHAR_S3_ACCESS_KEY"),
		"ADHAR_S3_SECRET_KEY": ref(name+"-s3", "ADHAR_S3_SECRET_KEY"),
	}
}

func bkS3Env(env map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range env {
		if strings.HasPrefix(k, "ADHAR_S3_") {
			out[k] = v
		}
	}
	return out
}

// Every pod of every run (each role, the clock's run and a person's) reads the same
// five variables; the credential is projected beside the job, never copied into it.
func TestBatchJobBucketReachesEveryRunPod(t *testing.T) {
	for _, c := range []struct {
		name   string
		xr     map[string]any
		render func(xr map[string]any, cluster []map[string]any) []map[string]any
		roles  []string
	}{
		{"single, scheduled", bjXR(t, "xr-scheduled.yaml"), func(xr map[string]any, cl []map[string]any) []map[string]any {
			return bjMustRender(t, xr, append(bjCluster(t), cl...), nil)
		}, []string{"single"}},
		{"partitioned, a Volcano gang", vcXR(t, "xr-partitioned.yaml"), func(xr map[string]any, cl []map[string]any) []map[string]any {
			return vcMustRender(t, xr, append(vcCluster(t), cl...), nil)
		}, []string{"manager", "worker"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ns := fmt.Sprint(dig(c.xr, "metadata", "namespace"))
			name := fmt.Sprint(dig(c.xr, "metadata", "name"))
			xr := bjWithParams(c.xr, map[string]any{"bucket": "reports"})
			docs := c.render(xr, []map[string]any{bkBucket(ns, "reports", "reports-bucket")})
			want := bkWantEnv(name, "reports")

			run, _ := vcTemplate(t, docs)
			pods := vcPods(run)
			for _, role := range c.roles {
				pod, ok := pods[role]
				if !ok {
					t.Fatalf("the run template has no %s pod (has %v)", role, pods)
				}
				_, env := vcContainerEnv(t, pod)
				if got := bkS3Env(env); !reflect.DeepEqual(got, want) {
					t.Errorf("%s pod: ADHAR_S3_* = %v, want %v", role, got, want)
				}
			}
			if cron := bjResource(docs, "schedule-native"); cron != nil {
				_, env := vcContainerEnv(t, dig(cron, "spec", "jobTemplate", "spec", "template").(map[string]any))
				if got := bkS3Env(env); !reflect.DeepEqual(got, want) {
					t.Errorf("the CronJob's run: ADHAR_S3_* = %v, want %v", got, want)
				}
			}

			es := bjMustResource(t, docs, "s3-credentials")
			if es["apiVersion"] != "external-secrets.io/v1" || es["kind"] != "ExternalSecret" ||
				dig(es, "metadata", "name") != name+"-s3" || dig(es, "metadata", "namespace") != ns {
				t.Fatalf("s3-credentials is %v %v %v/%v", es["apiVersion"], es["kind"], dig(es, "metadata", "namespace"), dig(es, "metadata", "name"))
			}
			if store := dig(es, "spec", "secretStoreRef"); !reflect.DeepEqual(store, map[string]any{"kind": "ClusterSecretStore", "name": "platform-object-storage"}) {
				t.Errorf("the credential is read through %v, want ClusterSecretStore platform-object-storage", store)
			}
			if dig(es, "spec", "target", "name") != name+"-s3" {
				t.Errorf("the ExternalSecret writes %v, which the run does not read", dig(es, "spec", "target", "name"))
			}
			wantData := []any{
				map[string]any{"secretKey": "ADHAR_S3_ACCESS_KEY", "remoteRef": map[string]any{"key": "root-creds", "property": "rootUser"}},
				map[string]any{"secretKey": "ADHAR_S3_SECRET_KEY", "remoteRef": map[string]any{"key": "root-creds", "property": "rootPassword"}},
			}
			if got := dig(es, "spec", "data"); !reflect.DeepEqual(got, wantData) {
				t.Errorf("the ExternalSecret maps %v, want root-creds rootUser/rootPassword", got)
			}
			if dig(es, "metadata", "labels", "adhar.io/batch-job") != name {
				t.Errorf("the ExternalSecret lacks the job's labels: %v", dig(es, "metadata", "labels"))
			}
			if dig(es, "metadata", "annotations", "gotemplating.fn.crossplane.io/ready") != "False" {
				t.Error("an ExternalSecret not yet observed is declared ready")
			}
			if p := bjCondition(t, docs, "PrerequisitesReady"); p["status"] != "True" {
				t.Errorf("PrerequisitesReady = %v with the Bucket present", p)
			}
		})
	}
}

// The ExternalSecret is ready once it exists, as the composition's other objects are:
// whether ESO could read the credential is ESO's to report, on the ExternalSecret.
func TestBatchJobBucketCredentialIsReadyOnceObserved(t *testing.T) {
	xr := bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"bucket": "inbound"})
	observed := bjMustRender(t, xr, append(bjCluster(t), bkBucket("prod", "inbound", "inbound-bucket")),
		map[string]any{"s3-credentials": map[string]any{"resource": map[string]any{"metadata": map[string]any{"uid": "u1"}}}})
	if dig(bjMustResource(t, observed, "s3-credentials"), "metadata", "annotations", "gotemplating.fn.crossplane.io/ready") != "True" {
		t.Error("the ExternalSecret is not ready once observed")
	}
}

// A job that names no bucket asks for none and gets nothing: no requirement, no
// ExternalSecret, no ADHAR_S3_* variable.
func TestBatchJobWithoutABucketGetsNoObjectStore(t *testing.T) {
	docs := bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil)
	if r := calRequirements(docs)["bucket"]; r != nil {
		t.Errorf("a job without bucket requires %v", r)
	}
	if es := bjResource(docs, "s3-credentials"); es != nil {
		t.Errorf("a job without bucket composes %v", es)
	}
	if got := bkS3Env(bjEnv(t, bjContainer(t, bjRunTemplate(t, docs)))); len(got) != 0 {
		t.Errorf("a job without bucket carries %v", got)
	}
}

// The Bucket is read where the job lives, by name; one that does not exist, or that
// publishes its connection under another name than the run reads, suspends the
// schedule and says why (PrerequisitesReady), as a missing calendar does.
func TestBatchJobBucketMustExistAndPublishItsSecret(t *testing.T) {
	xr := bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"bucket": "inbound"})
	r, _ := calRequirements(bjMustRender(t, xr, bjCluster(t), nil))["bucket"].(map[string]any)
	if r["apiVersion"] != "platform.adhar.io/v1alpha1" || r["kind"] != "Bucket" || r["matchName"] != "inbound" || r["namespace"] != "prod" {
		t.Errorf("the bucket requirement is %v, want Bucket prod/inbound", r)
	}

	for _, c := range []struct {
		name    string
		cluster []map[string]any
		ok      bool
		message string
	}{
		{"absent", bjCluster(t), false, "bucket names Bucket inbound, which does not exist in namespace prod"},
		{"in another namespace", append(bjCluster(t), bkBucket("dev", "inbound", "inbound-bucket")), false, "does not exist in namespace prod"},
		{"publishing another Secret", append(bjCluster(t), bkBucket("prod", "inbound", "inbound-conn")), false, "publishes its connection as Secret inbound-conn, not inbound-bucket"},
		{"not yet reconciled", append(bjCluster(t), bkBucket("prod", "inbound", "")), true, ""},
		{"present", append(bjCluster(t), bkBucket("prod", "inbound", "inbound-bucket")), true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			docs := bjMustRender(t, xr, c.cluster, nil)
			p := bjCondition(t, docs, "PrerequisitesReady")
			if c.ok {
				if p["status"] != "True" {
					t.Errorf("PrerequisitesReady = %v", p)
				}
				return
			}
			if p["status"] != "False" || p["reason"] != "BucketUnavailable" || !strings.Contains(fmt.Sprint(p["message"]), c.message) {
				t.Errorf("PrerequisitesReady = %v, want False BucketUnavailable %q", p, c.message)
			}
			if dig(bjMustResource(t, docs, "schedule-native"), "spec", "suspend") != true {
				t.Error("the schedule keeps firing runs that cannot reach their bucket")
			}
		})
	}
}

// The five variables are the bucket's: an env entry naming one of them beside
// `bucket` contradicts it and is refused; without `bucket` a team may set them.
func TestBatchJobBucketRefusesItsOwnVariablesInEnv(t *testing.T) {
	cluster := append(bjCluster(t), bkBucket("prod", "inbound", "inbound-bucket"))
	withEnv := bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"bucket": "inbound", "env": map[string]any{"ADHAR_S3_ENDPOINT": "http://elsewhere:9000"}})
	if _, err := bjRender(t, withEnv, cluster, nil); err == nil || !strings.Contains(err.Error(), "env ADHAR_S3_ENDPOINT contradicts bucket") {
		t.Errorf("ADHAR_S3_ENDPOINT beside bucket: %v", err)
	}
	own := bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"env": map[string]any{"ADHAR_S3_ENDPOINT": "http://elsewhere:9000"}})
	env := bjEnv(t, bjContainer(t, bjRunTemplate(t, bjMustRender(t, own, bjCluster(t), nil))))
	if env["ADHAR_S3_ENDPOINT"] != "http://elsewhere:9000" {
		t.Errorf("without bucket a team's own ADHAR_S3_ENDPOINT is %v", env["ADHAR_S3_ENDPOINT"])
	}
}

// D3: the XRD accepts `bucket` because this composition renders it, as a DNS label,
// the name of a Bucket XR; and the fixture uses only fields the XRD defines.
func TestBatchJobXRDDefinesTheBucket(t *testing.T) {
	x := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd/batchjob.xrd.yaml"))[0]
	versions := asList(dig(x, "spec", "versions"))
	params := dig(versions[0].(map[string]any), "schema", "openAPIV3Schema", "properties", "spec", "properties", "parameters", "properties").(map[string]any)
	b, ok := params["bucket"].(map[string]any)
	if !ok {
		t.Fatal("the BatchJob XRD defines no parameters.bucket")
	}
	re := regexp.MustCompile(fmt.Sprint(b["pattern"]))
	for name, valid := range map[string]bool{"inbound": true, "reports-2026": true, "Inbound": false, "-x": false, "a_b": false} {
		if re.MatchString(name) != valid {
			t.Errorf("bucket %q valid = %v, want %v", name, !valid, valid)
		}
	}
	if b["type"] != "string" || fmt.Sprint(b["maxLength"]) != "63" {
		t.Errorf("parameters.bucket = %v", b)
	}
	xr := readYAMLDocs(t, filepath.Join(bkFixtureDir, "xr-bucket.yaml"))[0]
	for k := range dig(xr, "spec", "parameters").(map[string]any) {
		if _, ok := params[k]; !ok {
			t.Errorf("xr-bucket.yaml sets parameters.%s, which the XRD does not define", k)
		}
	}
}

// The ExternalSecret only works because three things outside the composition agree:
// the store serves environment namespaces, an environment namespace carries the label
// it selects, and its ServiceAccount may read root-creds, which holds both keys.
func TestTheBucketCredentialIsReadableFromAnEnvironmentNamespace(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	var store, role map[string]any
	for _, d := range readYAMLDocs(t, filepath.Join(root, "platform/stack/packages/security/external-secrets/manifests/platform-store.yaml")) {
		if d["kind"] == "ClusterSecretStore" && dig(d, "metadata", "name") == "platform-object-storage" {
			store = d
		}
		if d["kind"] == "Role" && dig(d, "metadata", "name") == "eso-store-object-storage" {
			role = d
		}
	}
	if store == nil || role == nil {
		t.Fatal("security/external-secrets ships no platform-object-storage store and its Role")
	}
	served := false
	for _, c := range asList(dig(store, "spec", "conditions")) {
		for _, e := range asList(dig(c.(map[string]any), "namespaceSelector", "matchExpressions")) {
			m := e.(map[string]any)
			if m["key"] == "adhar.io/plane" && m["operator"] == "In" && strings.Contains(fmt.Sprint(m["values"]), "workload") {
				served = true
			}
		}
	}
	if !served {
		t.Errorf("platform-object-storage does not serve adhar.io/plane=workload namespaces: %v", dig(store, "spec", "conditions"))
	}
	readable := false
	for _, r := range asList(role["rules"]) {
		m := r.(map[string]any)
		if strings.Contains(fmt.Sprint(m["resources"]), "secrets") && strings.Contains(fmt.Sprint(m["resourceNames"]), "root-creds") {
			readable = true
		}
	}
	if !readable {
		t.Error("the store's ServiceAccount may not read root-creds")
	}
	env := readConfigurationFile(t, "compositions/env/local.yaml")
	if !strings.Contains(env, "adhar.io/plane: workload") {
		t.Error("the Environment composition no longer labels its namespace adhar.io/plane: workload, which the store selects")
	}
	rustfs := string(bkRead(t, filepath.Join(root, "platform/stack/packages/data/rustfs/manifests/install.yaml")))
	for _, key := range []string{"rootUser:", "rootPassword:"} {
		if !strings.Contains(rustfs, key) {
			t.Errorf("root-creds no longer carries %s", key)
		}
	}
}

// R5 is recorded where the root credential is taken: the BatchJob composition projects
// root-creds for a bucket, so it says, there, that this is RustFS's root and a Bucket is
// one team's (as compositions/bucket does for the Secret it hands out).
func TestBatchJobBucketRecordsR5WhereItTakesTheRootCredential(t *testing.T) {
	src := readConfigurationFile(t, bjComposition)
	takes := strings.Contains(src, "remoteRef: {key: root-creds")
	i := strings.Index(src, "kind: ExternalSecret")
	header := ""
	if i > 0 {
		header = src[max(0, i-900):i]
	}
	// Comment lines wrap: read the header as one sentence.
	header = strings.Join(strings.Fields(strings.ReplaceAll(header, "#", " ")), " ")
	if takes && !(strings.Contains(header, "design 0025 R5") && strings.Contains(header, "RustFS's root credential")) {
		t.Error("the BatchJob composition projects root-creds without recording R5 (RustFS's root credential, one team per Bucket) beside the ExternalSecret")
	}
}

func bkRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

/* ─────────────── the golden render and `crossplane render` ─────────────── */

func bkGoldenRender(t *testing.T) []map[string]any {
	return bjMustRender(t, readYAMLDocs(t, filepath.Join(bkFixtureDir, "xr-bucket.yaml"))[0],
		append(bjCluster(t), readYAMLDocs(t, filepath.Join(bkFixtureDir, "bucket.yaml"))...), nil)
}

// ADHAR_UPDATE_GOLDEN=1 rewrites it; read the diff first.
func TestBatchJobBucketGoldenRender(t *testing.T) {
	got := vcGolden(t, bkGoldenRender(t))
	path := filepath.Join(bkFixtureDir, "golden-native-bucket.yaml")
	if os.Getenv("ADHAR_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (ADHAR_UPDATE_GOLDEN=1 writes it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("golden-native-bucket.yaml differs from the render (ADHAR_UPDATE_GOLDEN=1 rewrites it; read the diff first):\n%s", lineDiff(string(want), string(got)))
	}
}

// The real function containers compose the same ExternalSecret and the same run.
func TestBatchJobBucketRendersWithCrossplane(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping `crossplane render`, which starts the function containers")
	}
	bin, err := exec.LookPath("crossplane")
	if err != nil {
		t.Skip("the crossplane CLI is not installed; the in-process renders still ran")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available; `crossplane render` runs each function in a container")
	}
	var parts []string
	for _, d := range append(bjCluster(t), readYAMLDocs(t, filepath.Join(bkFixtureDir, "bucket.yaml"))...) {
		y, _ := yaml.Marshal(d)
		parts = append(parts, string(y))
	}
	required := filepath.Join(t.TempDir(), "required.yaml")
	if err := os.WriteFile(required, []byte(strings.Join(parts, "---\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "render", filepath.Join(bkFixtureDir, "xr-bucket.yaml"), filepath.Join(controlPlaneDir(t), bjComposition),
		filepath.Join(bjFixtureDir, "functions.yaml"), "--required-resources="+required)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := runRender(cmd); err != nil {
		t.Fatalf("crossplane render: %v\n%s", err, stderr.String())
	}
	real := map[string]map[string]any{}
	var status map[string]any
	for _, d := range strings.Split(stdout.String(), "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(d), &m); err != nil || len(m) == 0 {
			continue
		}
		if m["kind"] == "BatchJob" {
			status, _ = m["status"].(map[string]any)
		}
		if n, _ := dig(m, "metadata", "annotations", "crossplane.io/composition-resource-name").(string); n != "" {
			real[n] = m
		}
	}
	inProcess := bkGoldenRender(t)
	es := real["s3-credentials"]
	if es == nil {
		t.Fatalf("crossplane render composed no s3-credentials (composed %v)", keysOf(real))
	}
	if a, b := tqJSON(es["spec"]), tqJSON(bjMustResource(t, inProcess, "s3-credentials")["spec"]); a != b {
		t.Errorf("the ExternalSecret differs between crossplane render and the in-process render:\n%s\n%s", a, b)
	}
	var run map[string]any
	if err := yaml.Unmarshal([]byte(fmt.Sprint(dig(real["run-template"], "data", "template.yaml"))), &run); err != nil {
		t.Fatal(err)
	}
	if a, b := tqJSON(run), tqJSON(bjRunTemplate(t, inProcess)); a != b {
		t.Errorf("the run template differs between crossplane render and the in-process render:\n%s\n%s", a, b)
	}
	if got := bkS3Env(bjEnv(t, bjContainer(t, run))); !reflect.DeepEqual(got, bkWantEnv("importer", "inbound")) {
		t.Errorf("through crossplane render ADHAR_S3_* = %v", got)
	}
	for _, c := range asList(status["conditions"]) {
		if m := c.(map[string]any); m["type"] == "PrerequisitesReady" && m["status"] != "True" {
			t.Errorf("PrerequisitesReady through crossplane render = %v", m)
		}
	}
}

/* ─────── the run-integrity suite's fixtures (core/adhar-batch policy-tests) ─────── */

func bkIntegrityDir(t *testing.T) string {
	return filepath.Join(queuePolicyDir(t), "policy-tests", "bucket-run-integrity")
}

// bkTemplateText is the composed template.yaml of the on-demand fixture reading a Bucket.
func bkTemplateText(t *testing.T) string {
	t.Helper()
	xr := bjWithParams(bjXR(t, "xr-ondemand.yaml"), map[string]any{"bucket": "reports"})
	cm := bjMustResource(t, bjMustRender(t, xr, append(bjCluster(t), bkBucket("dev", "reports", "reports-bucket")), nil), "run-template")
	return dig(cm, "data", "template.yaml").(string)
}

// bkAdmitted is the template as a kube-apiserver admits it: a server-side dry run on
// envtest, so every default the policy must tolerate is in it.
func bkAdmitted(t *testing.T, templateText string) map[string]any {
	t.Helper()
	var run map[string]any
	if err := yaml.Unmarshal([]byte(templateText), &run); err != nil {
		t.Fatal(err)
	}
	run["metadata"].(map[string]any)["name"] = "run-ok"
	env := &envtest.Environment{BinaryAssetsDirectory: envtestAssets(t)}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v", err)
	}
	defer func() { _ = env.Stop() }()
	ctx := context.Background()
	cs := kubernetes.NewForConfigOrDie(cfg)
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	never := corev1.PreemptNever
	if _, err := cs.SchedulingV1().PriorityClasses().Create(ctx, &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: "adhar-batch"}, Value: -100, PreemptionPolicy: &never}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := dynamic.NewForConfigOrDie(cfg).Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace("dev").
		Create(ctx, &unstructured.Unstructured{Object: run}, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	if err != nil {
		t.Fatalf("the API server refuses the bucket run: %v", err)
	}
	out := got.Object
	delete(out, "status")
	md := out["metadata"].(map[string]any)
	for _, k := range []string{"uid", "creationTimestamp", "resourceVersion", "managedFields", "generation"} {
		delete(md, k)
	}
	uid := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	b, _ := json.Marshal(out)
	b = uid.ReplaceAll(b, []byte("ffee02c4-eea5-4e1f-8549-4661a269f063"))
	var fixed map[string]any
	_ = json.Unmarshal(b, &fixed)
	return fixed
}

// The suite proves the run-integrity policy against what a BatchJob with a bucket
// really composes: a run of that template is admitted, and one whose S3 variables
// point at another Secret is not. With ADHAR_UPDATE_GOLDEN=1 this test writes
// run-template.json and job-as-admitted.json (KUBEBUILDER_ASSETS), then
// generate-resources.sh builds values.yaml and resources.yaml from them.
func TestBucketRunIntegrityFixtureIsTheComposedTemplate(t *testing.T) {
	text := bkTemplateText(t)
	dir := bkIntegrityDir(t)
	var tpl map[string]any
	if err := yaml.Unmarshal([]byte(text), &tpl); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ADHAR_UPDATE_GOLDEN") == "1" {
		b, _ := json.MarshalIndent(tpl, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "run-template.json"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		admitted, _ := json.MarshalIndent(bkAdmitted(t, text), "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "job-as-admitted.json"), append(admitted, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("bash", filepath.Join(dir, "generate-resources.sh")).CombinedOutput(); err != nil {
			t.Fatalf("generate-resources.sh: %v\n%s", err, out)
		}
	}
	var fixture map[string]any
	if err := json.Unmarshal(bkRead(t, filepath.Join(dir, "run-template.json")), &fixture); err != nil {
		t.Fatal(err)
	}
	if tqJSON(fixture) != tqJSON(tpl) {
		t.Error("bucket-run-integrity/run-template.json is not the composed template (ADHAR_UPDATE_GOLDEN=1 rewrites it)")
	}
	values := readYAMLDocs(t, filepath.Join(dir, "values.yaml"))[0]
	rules := asList(asList(values["policies"])[0].(map[string]any)["rules"])
	var mocked map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(dig(rules[0].(map[string]any), "values", "templateYaml"))), &mocked); err != nil {
		t.Fatalf("values.yaml's templateYaml does not parse: %v", err)
	}
	if tqJSON(mocked) != tqJSON(tpl) {
		t.Error("bucket-run-integrity/values.yaml mocks another template than the composed one (run generate-resources.sh)")
	}
	var admitted map[string]any
	if err := json.Unmarshal(bkRead(t, filepath.Join(dir, "job-as-admitted.json")), &admitted); err != nil {
		t.Fatal(err)
	}
	if got := bkS3Env(bjEnv(t, bjContainer(t, admitted))); !reflect.DeepEqual(got, bkWantEnv("reconciler", "reports")) {
		t.Errorf("job-as-admitted.json carries ADHAR_S3_* = %v", got)
	}
}
