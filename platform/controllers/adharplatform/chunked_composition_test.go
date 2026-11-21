package adharplatform

// Phase 4 of the BatchJob composition (design 0025 D11, §5.11): `mode: chunked` is
// remote chunking in the partitioned SHAPE — an Indexed Job natively, a Volcano gang
// on Volcano, the same roles — with ADHAR_BATCH_DISTRIBUTION=chunked instead of the
// partition channel, the two RabbitMQ Queue XRs it names mounted by envFrom, and
// opt-in OpenLineage. Rendered in-process the way function-go-templating does
// (vcRender), through `crossplane render` when Docker is available, and the XRD's
// rules on a real API server (envtest). The run-integrity policies are proven
// against these renders by the kyverno CLI (`make policy-test`,
// core/adhar-batch/policy-tests/chunked-run-integrity). Each guard was
// mutation-tested: the behaviour reverted in the composition or the XRD, the test
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
	"sort"
	"strings"
	"testing"
	"time"

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

const chFixtureDir = "testdata/batchjob-chunked"

func chXR(t *testing.T) map[string]any {
	t.Helper()
	docs := readYAMLDocs(t, filepath.Join(chFixtureDir, "xr-chunked.yaml"))
	if len(docs) != 1 || docs[0]["kind"] != "BatchJob" {
		t.Fatal("xr-chunked.yaml must hold exactly one BatchJob")
	}
	return docs[0]
}

// chExtras are the chunked job's two Queues and the OpenMetadata Service, minus the named ones.
func chExtras(t *testing.T, without ...string) []map[string]any {
	t.Helper()
	var out []map[string]any
docs:
	for _, d := range readYAMLDocs(t, filepath.Join(chFixtureDir, "queues.yaml")) {
		for _, w := range without {
			if dig(d, "metadata", "name") == w {
				continue docs
			}
		}
		out = append(out, d)
	}
	return out
}

// chCluster is the Volcano fixture cluster plus the chunked job's Queues and receiver.
func chCluster(t *testing.T, without ...string) []map[string]any {
	return append(vcCluster(t, without...), chExtras(t, without...)...)
}

func chNative(t *testing.T, without ...string) []map[string]any {
	return append(vcNoVolcano(t), chExtras(t, without...)...)
}

// chEnvFrom is a container's envFrom as a list of maps.
func chEnvFrom(c map[string]any) []map[string]any {
	var out []map[string]any
	for _, e := range asList(c["envFrom"]) {
		out = append(out, e.(map[string]any))
	}
	return out
}

// The run every pod of a chunked job carries: the distribution, the manager's wait,
// both Queues by envFrom under their own prefix, the broker's password, and none of
// the partition channel or ADHAR_BATCH_MODE.
func chAssertChunkedPod(t *testing.T, role string, pod map[string]any) {
	t.Helper()
	c, env := vcContainerEnv(t, pod)
	for k, v := range map[string]string{"ADHAR_BATCH_ROLE": role, "ADHAR_BATCH_DISTRIBUTION": "chunked", "ADHAR_BATCH_CHUNKING_TIMEOUT": "21300s"} {
		if env[k] != v {
			t.Errorf("%s pod: env %s = %v, want %s", role, k, env[k], v)
		}
	}
	for _, k := range []string{"ADHAR_BATCH_MODE", "ADHAR_BATCH_PARTITION_TRANSPORT", "ADHAR_BATCH_PARTITION_GRIDSIZE", "ADHAR_BATCH_PARTITION_TIMEOUT"} {
		if _, set := env[k]; set {
			t.Errorf("%s pod: %s is set on a chunked run (Spring binds ADHAR_BATCH_MODE to auto|kubernetes|embedded; a chunked run has no partitions)", role, k)
		}
	}
	pw, _ := env["ADHAR_BATCH_CHUNKING_PASSWORD"].(map[string]any)
	if tqJSON(pw) != `{"secretKeyRef":{"key":"rabbitmq-password","name":"ledger-batch-broker"}}` {
		t.Errorf("%s pod: ADHAR_BATCH_CHUNKING_PASSWORD = %s", role, tqJSON(pw))
	}
	want := `[{"prefix":"ADHAR_BATCH_CHUNKING_REQUESTS_","secretRef":{"name":"ledger-chunks-queue"}},{"prefix":"ADHAR_BATCH_CHUNKING_REPLIES_","secretRef":{"name":"ledger-replies-queue"}}]`
	if got := tqJSON(chEnvFrom(c)); got != want {
		t.Errorf("%s pod: envFrom = %s\nwant %s", role, got, want)
	}
}

// Natively a chunked run is the partitioned Indexed Job, index 0 the manager.
func TestChunkedWithoutVolcanoIsAnIndexedJob(t *testing.T) {
	docs := vcMustRender(t, chXR(t), chNative(t), nil)
	run, kind := vcTemplate(t, docs)
	if kind != "IndexedJob" || run["apiVersion"] != "batch/v1" {
		t.Fatalf("kind %s, %v", kind, run["apiVersion"])
	}
	spec := run["spec"].(map[string]any)
	for k, v := range map[string]string{"completionMode": "Indexed", "completions": "3", "parallelism": "3"} {
		if fmt.Sprint(spec[k]) != v {
			t.Errorf("%s = %v, want %s", k, spec[k], v)
		}
	}
	pod := spec["template"].(map[string]any)
	chAssertChunkedPod(t, "indexed", pod)
	if _, env := vcContainerEnv(t, pod); dig(env["JOB_COMPLETION_INDEX"].(map[string]any), "fieldRef", "fieldPath") != "metadata.annotations['batch.kubernetes.io/job-completion-index']" {
		t.Errorf("JOB_COMPLETION_INDEX = %v; the kit resolves the manager from it", env["JOB_COMPLETION_INDEX"])
	}
	if st := bjStatus(t, docs); st["scheduler"] != "kubernetes" {
		t.Errorf("status.scheduler = %v", st["scheduler"])
	}
	if c := bjCondition(t, docs, "PrerequisitesReady"); c["status"] != "True" {
		t.Errorf("PrerequisitesReady = %v", c)
	}
	chAssertSameShapeAsPartitioned(t, chNative(t), docs)
}

// On Volcano a chunked run is the partitioned gang: same tasks, policies, gang size.
func TestChunkedOnVolcanoIsAGang(t *testing.T) {
	docs := vcMustRender(t, chXR(t), chCluster(t), nil)
	run, kind := vcTemplate(t, docs)
	if kind != "VolcanoJob" || run["apiVersion"] != "batch.volcano.sh/v1alpha1" {
		t.Fatalf("kind %s, %v", kind, run["apiVersion"])
	}
	spec := run["spec"].(map[string]any)
	if fmt.Sprint(spec["minAvailable"]) != "3" || spec["queue"] != "acme-payments-dev" {
		t.Errorf("minAvailable %v queue %v", spec["minAvailable"], spec["queue"])
	}
	pods := vcPods(run)
	if len(pods) != 2 || pods["manager"] == nil || pods["worker"] == nil {
		t.Fatalf("roles %v", reflect.ValueOf(pods).MapKeys())
	}
	for role, pod := range pods {
		chAssertChunkedPod(t, role, pod)
	}
	chAssertSameShapeAsPartitioned(t, chCluster(t), docs)
}

// D11 "renders the partitioned gang shape": with the env and envFrom a chunked run
// adds taken away, and the partition channel taken from the partitioned run, the two
// run objects are the same object.
func chAssertSameShapeAsPartitioned(t *testing.T, cluster []map[string]any, chunkedDocs []map[string]any) {
	t.Helper()
	partitioned := bjWithParams(chXR(t), map[string]any{"mode": "partitioned", "chunking": nil})
	pRun, pKind := vcTemplate(t, vcMustRender(t, partitioned, cluster, nil))
	cRun, cKind := vcTemplate(t, chunkedDocs)
	if pKind != cKind {
		t.Fatalf("chunked renders a %s, partitioned a %s", cKind, pKind)
	}
	strip := func(run map[string]any, drop func(string) bool) string {
		for _, pod := range vcPods(run) {
			for _, c := range asList(dig(pod, "spec", "containers")) {
				cm := c.(map[string]any)
				var keep []any
				for _, e := range asList(cm["env"]) {
					if !drop(e.(map[string]any)["name"].(string)) {
						keep = append(keep, e)
					}
				}
				cm["env"] = keep
				delete(cm, "envFrom")
			}
		}
		return tqJSON(run)
	}
	p := strip(pRun, func(n string) bool {
		return strings.HasPrefix(n, "ADHAR_BATCH_PARTITION_") || strings.HasPrefix(n, "OPENLINEAGE_")
	})
	c := strip(cRun, func(n string) bool {
		return n == "ADHAR_BATCH_DISTRIBUTION" || strings.HasPrefix(n, "ADHAR_BATCH_CHUNKING_") || strings.HasPrefix(n, "OPENLINEAGE_")
	})
	if p != c {
		t.Errorf("a chunked run differs from the partitioned run in more than its distribution:\n%s\n%s", c, p)
	}
}

// A chunked run can never start without its broker, so a Queue that is missing, or
// that publishes its connection under another name than the template mounts, is a
// missing prerequisite: the schedule is suspended and the condition says which.
func TestChunkedRunNeedsItsQueues(t *testing.T) {
	scheduled := bjWithParams(chXR(t), map[string]any{"schedule": map[string]any{"cron": "0 2 * * *"}})
	renamed := func(d map[string]any) { d["status"].(map[string]any)["connectionSecret"] = "elsewhere" }
	for _, c := range []struct {
		name    string
		cluster []map[string]any
		want    string // "" = met
	}{
		{"both Queues exist", chNative(t), ""},
		{"the request Queue is missing", chNative(t, "ledger-chunks"), "chunking.requestQueue names Queue ledger-chunks, which does not exist in namespace dev"},
		{"the reply Queue is missing", chNative(t, "ledger-replies"), "chunking.replyQueue names Queue ledger-replies, which does not exist in namespace dev"},
		{"a Queue publishes another Secret", vcEdit(chNative(t), "ledger-replies", renamed), "Queue ledger-replies publishes its connection as Secret elsewhere, not ledger-replies-queue"},
		{"a Queue not yet reconciled (no status) still counts", vcEdit(chNative(t), "ledger-chunks", func(d map[string]any) { delete(d, "status") }), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			docs := vcMustRender(t, scheduled, c.cluster, nil)
			prereq := bjCondition(t, docs, "PrerequisitesReady")
			suspended := dig(bjMustResource(t, docs, "schedule-native"), "spec", "suspend")
			if c.want == "" {
				if prereq["status"] != "True" || suspended != false {
					t.Errorf("PrerequisitesReady = %v, suspend = %v", prereq, suspended)
				}
				return
			}
			if prereq["status"] != "False" || prereq["reason"] != "ChunkQueueUnavailable" || !strings.Contains(fmt.Sprint(prereq["message"]), c.want) {
				t.Errorf("PrerequisitesReady = %v, want ChunkQueueUnavailable saying %q", prereq, c.want)
			}
			if suspended != true {
				t.Error("a chunked job whose Queue is unusable keeps its schedule running")
			}
			if !strings.Contains(fmt.Sprint(bjStatus(t, docs)["message"]), c.want) {
				t.Errorf("status.message %q does not say why", bjStatus(t, docs)["message"])
			}
		})
	}
	// The Queues are asked for in the job's own namespace, and only in mode chunked.
	reqs := calRequirements(vcMustRender(t, chXR(t), chNative(t), nil))
	for key, name := range map[string]string{"chunkRequestQueue": "ledger-chunks", "chunkReplyQueue": "ledger-replies"} {
		sel, _ := reqs[key].(map[string]any)
		if sel["apiVersion"] != "platform.adhar.io/v1alpha1" || sel["kind"] != "Queue" || sel["matchName"] != name || sel["namespace"] != "dev" {
			t.Errorf("requirement %s = %v", key, sel)
		}
	}
	for key := range calRequirements(vcMustRender(t, vcXR(t, "xr-partitioned.yaml"), vcNoVolcano(t), nil)) {
		if strings.HasPrefix(key, "chunk") {
			t.Errorf("a partitioned job asks for %s", key)
		}
	}
}

// What the composition refuses although the XRD's rules should have stopped it first:
// a definition that reached the composition another way must still not render a run
// with no broker, and a user env entry must not fight lineage over the client's vars.
func TestChunkedRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		set  map[string]any
		want string
	}{
		{"chunked without chunking", map[string]any{"chunking": nil}, "mode chunked needs chunking.requestQueue and chunking.replyQueue"},
		{"one queue twice", map[string]any{"chunking": map[string]any{"requestQueue": "ledger-chunks", "replyQueue": "ledger-chunks"}}, "a worker would read its own replies as chunks"},
		{"OPENLINEAGE_ env with lineage on", map[string]any{"env": map[string]any{"OPENLINEAGE_URL": "http://evil"}}, "env OPENLINEAGE_URL contradicts lineage.enabled"},
		{"an ADHAR_BATCH_DISTRIBUTION env", map[string]any{"env": map[string]any{"ADHAR_BATCH_DISTRIBUTION": "partitioned"}}, "env ADHAR_BATCH_DISTRIBUTION is owned by the runner's contract"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := vcRender(t, bjWithParams(chXR(t), c.set), chNative(t), nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("refusal = %v, want %q", err, c.want)
			}
		})
	}
	// OPENLINEAGE_* in env is the caller's own business while lineage is off.
	off := bjWithParams(chXR(t), map[string]any{"lineage": map[string]any{"enabled": false}, "env": map[string]any{"OPENLINEAGE_URL": "http://marquez:5000"}})
	run, _ := vcTemplate(t, vcMustRender(t, off, chNative(t), nil))
	if _, env := vcContainerEnv(t, dig(run, "spec", "template").(map[string]any)); env["OPENLINEAGE_URL"] != "http://marquez:5000" {
		t.Errorf("with lineage off the caller's OPENLINEAGE_URL = %v", env["OPENLINEAGE_URL"])
	}
}

// TestChunkedRunReadsThePasswordTheQueueReferences lives in broker_credential_test.go
// (R15): the run and the Queue now each project the broker's key beside themselves.

// Lineage (D11): opt-in, the client's own variables, the platform's OpenMetadata
// receiver by default (whose OpenLineage path is not the client's default), the
// namespace from the hierarchy, the token optional, and a missing receiver a warning.
func TestLineageSetsTheOpenLineageClientsVariables(t *testing.T) {
	envOf := func(t *testing.T, xr map[string]any, cluster []map[string]any) (map[string]any, []map[string]any) {
		docs := vcMustRender(t, xr, cluster, nil)
		run, _ := vcTemplate(t, docs)
		for _, pod := range vcPods(run) {
			_, env := vcContainerEnv(t, pod)
			return env, docs
		}
		t.Fatal("no pod")
		return nil, nil
	}
	env, docs := envOf(t, chXR(t), chNative(t))
	for k, v := range map[string]string{
		"OPENLINEAGE_URL":       "http://openmetadata.adhar-system.svc.cluster.local:8585",
		"OPENLINEAGE_ENDPOINT":  "/api/v1/openlineage/lineage",
		"OPENLINEAGE_NAMESPACE": "adhar/acme/payments/shop/dev",
	} {
		if env[k] != v {
			t.Errorf("%s = %v, want %s", k, env[k], v)
		}
	}
	if got := tqJSON(env["OPENLINEAGE_API_KEY"]); got != `{"secretKeyRef":{"key":"token","name":"ledger-openmetadata","optional":true}}` {
		t.Errorf("OPENLINEAGE_API_KEY = %s; optional, so a missing token never keeps a run from starting", got)
	}
	if strings.Contains(fmt.Sprint(bjStatus(t, docs)["message"]), "lineage") {
		t.Errorf("a warning with the receiver present: %v", bjStatus(t, docs)["message"])
	}
	if reqs := calRequirements(docs); dig(reqs, "lineageReceiver", "matchName") != "openmetadata" || dig(reqs, "lineageReceiver", "namespace") != "adhar-system" {
		t.Errorf("the receiver is not looked up: %v", reqs["lineageReceiver"])
	}

	// No OpenMetadata: a warning, never a refusal, and the run is unchanged.
	env2, docs := envOf(t, chXR(t), chNative(t, "openmetadata"))
	if tqJSON(env2) != tqJSON(env) {
		t.Error("the run template depends on whether OpenMetadata is installed; it must not change under a run in flight")
	}
	if msg := fmt.Sprint(bjStatus(t, docs)["message"]); !strings.Contains(msg, "OpenMetadata is not installed") {
		t.Errorf("status.message = %q, want the missing-receiver warning", msg)
	}

	// Another receiver: its URL, no OpenMetadata path, no lookup.
	other := bjWithParams(chXR(t), map[string]any{"lineage": map[string]any{"enabled": true, "url": "http://marquez.data.svc:5000"}})
	env, docs = envOf(t, other, chNative(t, "openmetadata"))
	if env["OPENLINEAGE_URL"] != "http://marquez.data.svc:5000" || env["OPENLINEAGE_ENDPOINT"] != nil || env["OPENLINEAGE_API_KEY"] != nil {
		t.Errorf("with lineage.url: URL %v ENDPOINT %v API_KEY %v", env["OPENLINEAGE_URL"], env["OPENLINEAGE_ENDPOINT"], env["OPENLINEAGE_API_KEY"])
	}
	if _, asked := calRequirements(docs)["lineageReceiver"]; asked || strings.Contains(fmt.Sprint(bjStatus(t, docs)["message"]), "OpenMetadata") {
		t.Error("with lineage.url the platform's OpenMetadata is still looked up or warned about")
	}
	withPath := bjWithParams(chXR(t), map[string]any{"lineage": map[string]any{"enabled": true, "url": "http://marquez.data.svc:5000", "endpoint": "/api/v1/lineage"}})
	if env, _ = envOf(t, withPath, chNative(t)); env["OPENLINEAGE_ENDPOINT"] != "/api/v1/lineage" {
		t.Errorf("lineage.endpoint = %v", env["OPENLINEAGE_ENDPOINT"])
	}

	// Off (the default), and on any mode: single-mode jobs get lineage too.
	single := bjXR(t, "xr-scheduled.yaml")
	env, docs = envOf(t, single, bjCluster(t))
	for k := range env {
		if strings.HasPrefix(k, "OPENLINEAGE_") {
			t.Errorf("lineage is opt-in, and a job without it carries %s", k)
		}
	}
	if _, asked := calRequirements(docs)["lineageReceiver"]; asked {
		t.Error("a job without lineage looks up OpenMetadata")
	}
	single = bjWithParams(single, map[string]any{"lineage": map[string]any{"enabled": true}})
	env, _ = envOf(t, single, append(bjCluster(t), chExtras(t)...))
	if env["OPENLINEAGE_NAMESPACE"] != "adhar/acme/payments/shop/prod" || env["OPENLINEAGE_API_KEY"] != nil {
		t.Errorf("single-mode lineage: NAMESPACE %v API_KEY %v", env["OPENLINEAGE_NAMESPACE"], env["OPENLINEAGE_API_KEY"])
	}
}

// The fixtures a person would apply use only fields the strict XRD defines, down to
// the nested ones, and the XRD carries Phase 4's fields.
func TestBatchJobXRDCarriesThePhaseFourFields(t *testing.T) {
	x := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd/batchjob.xrd.yaml"))[0]
	v0 := dig(x, "spec", "versions").([]any)[0].(map[string]any)
	params := dig(v0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "parameters").(map[string]any)
	props := params["properties"].(map[string]any)
	if fmt.Sprint(dig(props, "mode", "enum")) != "[single partitioned chunked]" {
		t.Errorf("mode enum = %v", dig(props, "mode", "enum"))
	}
	if fmt.Sprint(dig(props, "chunking", "required")) != "[requestQueue replyQueue]" {
		t.Errorf("chunking requires %v", dig(props, "chunking", "required"))
	}
	for path, fields := range map[string][]string{"chunking": {"requestQueue", "replyQueue"}, "lineage": {"enabled", "url", "endpoint", "credentialsSecretRef"}} {
		for _, f := range fields {
			if dig(props, path, "properties", f) == nil {
				t.Errorf("%s.%s is missing; the composition renders it (D3)", path, f)
			}
		}
	}
	if fmt.Sprint(dig(props, "lineage", "properties", "enabled", "default")) != "false" {
		t.Error("lineage must be opt-in")
	}
	var walk func(path string, obj, schema map[string]any)
	walk = func(path string, obj, schema map[string]any) {
		sp, _ := schema["properties"].(map[string]any)
		for k, v := range obj {
			sub, ok := sp[k].(map[string]any)
			if !ok {
				t.Errorf("xr-chunked.yaml sets %s%s, which the XRD does not define", path, k)
				continue
			}
			if m, ok := v.(map[string]any); ok && sub["properties"] != nil {
				walk(path+k+".", m, sub)
			}
		}
	}
	walk("", dig(chXR(t), "spec", "parameters").(map[string]any), params)
}

// The XRD's rules, on a real API server: the XRD's schema installed as the CRD
// Crossplane generates from it (envtest), and objects created against it.
func TestBatchJobXRDRulesForChunkedOnARealAPIServer(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping the envtest API server")
	}
	x := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd/batchjob.xrd.yaml"))[0]
	v0 := dig(x, "spec", "versions").([]any)[0].(map[string]any)
	crd := map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "batchjobs.platform.adhar.io"},
		"spec": map[string]any{
			"group": "platform.adhar.io",
			"scope": "Namespaced",
			"names": map[string]any{"kind": "BatchJob", "plural": "batchjobs", "singular": "batchjob", "listKind": "BatchJobList"},
			"versions": []any{map[string]any{
				"name": "v1alpha1", "served": true, "storage": true,
				"schema": map[string]any{"openAPIV3Schema": dig(v0, "schema", "openAPIV3Schema")},
			}},
		},
	}
	env := &envtest.Environment{BinaryAssetsDirectory: envtestAssets(t)}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	ctx := context.Background()
	dyn := dynamic.NewForConfigOrDie(cfg)
	crdGVR := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	if _, err := dyn.Resource(crdGVR).Create(ctx, &unstructured.Unstructured{Object: crd}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("the XRD's schema is not a valid CRD schema: %v", err)
	}
	gvr := schema.GroupVersionResource{Group: "platform.adhar.io", Version: "v1alpha1", Resource: "batchjobs"}
	if _, err := kubernetes.NewForConfigOrDie(cfg).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	create := func(obj map[string]any) error {
		var err error
		for i := 0; i < 50; i++ { // the CRD is served once it is Established
			_, err = dyn.Resource(gvr).Namespace("dev").Create(ctx, &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}, FieldValidation: "Strict"})
			if err == nil || !strings.Contains(err.Error(), "the server could not find the requested resource") {
				return err
			}
			time.Sleep(200 * time.Millisecond)
		}
		return err
	}
	named := func(xr map[string]any) map[string]any {
		cp := bjWithMeta(xr, map[string]any{"namespace": "dev"})
		delete(cp, "status")
		return cp
	}
	for _, c := range []struct {
		name string
		xr   map[string]any
		want string // "" = admitted
	}{
		{"the chunked fixture", chXR(t), ""},
		{"Phase 2's partitioned fixture", vcXR(t, "xr-partitioned.yaml"), ""},
		{"Phase 1's single fixture", bjXR(t, "xr-scheduled.yaml"), ""},
		{"chunked with no chunking", bjWithParams(chXR(t), map[string]any{"chunking": nil}), "mode chunked needs chunking.requestQueue and chunking.replyQueue"},
		{"chunking on a partitioned job", bjWithParams(chXR(t), map[string]any{"mode": "partitioned"}), "chunking is read only in mode chunked"},
		{"chunking on a single job", bjWithParams(chXR(t), map[string]any{"mode": nil}), "chunking is read only in mode chunked"},
		{"chunked with a gridSize", bjWithParams(chXR(t), map[string]any{"gridSize": 8}), "a chunked run has none"},
		{"one Queue twice", bjWithParams(chXR(t), map[string]any{"chunking": map[string]any{"requestQueue": "q", "replyQueue": "q"}}), "must be two different Queues"},
		{"a reply Queue missing", bjWithParams(chXR(t), map[string]any{"chunking": map[string]any{"requestQueue": "q"}}), "replyQueue"},
		{"a Queue that is not a name", bjWithParams(chXR(t), map[string]any{"chunking": map[string]any{"requestQueue": "Q_1", "replyQueue": "r"}}), "requestQueue"},
		{"chunked with no workers", bjWithParams(chXR(t), map[string]any{"workers": 0}), "workers"},
		{"a mode the platform does not render", bjWithParams(chXR(t), map[string]any{"mode": "remote", "chunking": nil}), "mode"},
		{"lineage on a single job", bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"lineage": map[string]any{"enabled": true, "url": "https://marquez.example.com", "endpoint": "/api/v1/lineage", "credentialsSecretRef": map[string]any{"name": "t"}}}), ""},
		{"a lineage URL that is not one", bjWithParams(chXR(t), map[string]any{"lineage": map[string]any{"enabled": true, "url": "marquez:5000"}}), "lineage.url"},
		{"a lineage field the platform does not render", bjWithParams(chXR(t), map[string]any{"lineage": map[string]any{"enabled": true, "transport": "kafka"}}), "transport"},
		// A secretKeyRef the API server would refuse in the run's pod template (verifier).
		{"a lineage token Secret that is not a name", bjWithParams(chXR(t), map[string]any{"lineage": map[string]any{"enabled": true, "credentialsSecretRef": map[string]any{"name": "OpenMetadata Token"}}}), "credentialsSecretRef.name"},
		{"a lineage token key that is not a key", bjWithParams(chXR(t), map[string]any{"lineage": map[string]any{"enabled": true, "credentialsSecretRef": map[string]any{"name": "om", "key": "bot token"}}}), "credentialsSecretRef.key"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := create(named(c.xr))
			if c.want == "" {
				if err != nil {
					t.Errorf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("admitted or refused for another reason: %v, want %q", err, c.want)
			}
		})
	}
}

var chBranches = []struct {
	golden  string
	cluster func(t *testing.T) []map[string]any
}{
	{"golden-native-chunked.yaml", func(t *testing.T) []map[string]any { return chNative(t) }},
	{"golden-volcano-chunked.yaml", func(t *testing.T) []map[string]any { return chCluster(t) }},
}

// One golden render per path. ADHAR_UPDATE_GOLDEN=1 rewrites them; read the diff first.
func TestChunkedBatchJobGoldenRenders(t *testing.T) {
	for _, b := range chBranches {
		t.Run(b.golden, func(t *testing.T) {
			got := vcGolden(t, vcMustRender(t, chXR(t), b.cluster(t), nil))
			path := filepath.Join(chFixtureDir, b.golden)
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
				t.Errorf("%s differs from the render (ADHAR_UPDATE_GOLDEN=1 rewrites it; read the diff first):\n%s", b.golden, lineDiff(string(want), string(got)))
			}
		})
	}
}

// The real function containers: what Crossplane composes for a chunked job, on both
// paths and with a missing Queue, is what the in-process render composed.
func TestChunkedBatchJobRendersWithCrossplane(t *testing.T) {
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
	dir := t.TempDir()
	cases := []struct {
		name    string
		cluster []map[string]any
	}{
		{"native", chNative(t)},
		{"volcano", chCluster(t)},
		{"request-queue-missing", chNative(t, "ledger-chunks")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			xr := bjWithParams(chXR(t), map[string]any{"schedule": map[string]any{"cron": "0 2 * * *"}})
			write := func(file string, docs ...map[string]any) string {
				var parts []string
				for _, d := range docs {
					y, _ := yaml.Marshal(d)
					parts = append(parts, string(y))
				}
				p := filepath.Join(dir, c.name+"-"+file)
				if err := os.WriteFile(p, []byte(strings.Join(parts, "---\n")), 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			}
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(bin, "render", write("xr.yaml", xr), filepath.Join(controlPlaneDir(t), bjComposition),
				filepath.Join(bjFixtureDir, "functions.yaml"), "--required-resources="+write("required.yaml", c.cluster...))
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := runRender(cmd); err != nil {
				t.Fatalf("crossplane render: %v\n%s", err, stderr.String())
			}
			real := map[string]map[string]any{}
			var status map[string]any
			for _, d := range strings.Split(stdout.String(), "\n---") {
				var m map[string]any
				if yaml.Unmarshal([]byte(d), &m) != nil || len(m) == 0 {
					continue
				}
				if m["kind"] == "BatchJob" {
					status, _ = m["status"].(map[string]any)
					continue
				}
				if n, _ := dig(m, "metadata", "annotations", "crossplane.io/composition-resource-name").(string); n != "" {
					real[n] = m
				}
			}
			docs := vcMustRender(t, xr, c.cluster, nil)
			inProcess := map[string]map[string]any{}
			for _, d := range docs {
				if n, _ := dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name").(string); n != "" {
					inProcess[n] = d
				}
			}
			var a, b []string
			for n := range real {
				a = append(a, n)
			}
			for n := range inProcess {
				b = append(b, n)
			}
			sort.Strings(a)
			sort.Strings(b)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("crossplane render composes %v, the in-process render %v", a, b)
			}
			parsed := func(m map[string]any) string {
				data, _ := m["data"].(map[string]any)
				cp := map[string]any{}
				for k, v := range data {
					cp[k] = v
				}
				if s, ok := cp["template.yaml"].(string); ok {
					var o any
					if err := yaml.Unmarshal([]byte(s), &o); err != nil {
						t.Fatalf("template.yaml does not parse: %v", err)
					}
					cp["template.yaml"] = o
				}
				return tqJSON(cp)
			}
			for n, d := range inProcess {
				if parsed(real[n]) != parsed(d) || tqJSON(real[n]["spec"]) != tqJSON(d["spec"]) {
					t.Errorf("%s differs between crossplane render and the in-process render", n)
				}
			}
			var reason any
			for _, cc := range asList(status["conditions"]) {
				if m := cc.(map[string]any); m["type"] == "PrerequisitesReady" {
					reason = m["reason"]
				}
			}
			if want := bjCondition(t, docs, "PrerequisitesReady")["reason"]; reason != want {
				t.Errorf("PrerequisitesReady through crossplane render = %v, in-process %v", reason, want)
			}
			if fmt.Sprint(status["message"]) != fmt.Sprint(bjStatus(t, docs)["message"]) {
				t.Errorf("status.message through crossplane render = %v", status["message"])
			}
		})
	}
}

/* ─────────── the run-integrity suites' fixtures (core/adhar-batch/policy-tests) ─────────── */

func chIntegrityDir(t *testing.T) string {
	return filepath.Join(queuePolicyDir(t), "policy-tests", "chunked-run-integrity")
}

// chTemplateText is the composed template.yaml of the chunked fixture on a path.
func chTemplateText(t *testing.T, cluster []map[string]any) string {
	t.Helper()
	cm := bjMustResource(t, vcMustRender(t, chXR(t), cluster, nil), "run-template")
	return dig(cm, "data", "template.yaml").(string)
}

// chValuesTemplate is the templateYaml a suite's values.yaml hands the policy.
func chValuesTemplate(t *testing.T, file string) string {
	t.Helper()
	docs := readYAMLDocs(t, file)
	pols := asList(dig(docs[0], "policies"))
	if len(pols) != 1 {
		t.Fatalf("%s mocks %d policies", file, len(pols))
	}
	rules := asList(pols[0].(map[string]any)["rules"])
	if len(rules) != 1 {
		t.Fatalf("%s mocks %d rules", file, len(rules))
	}
	s, _ := dig(rules[0].(map[string]any), "values", "templateYaml").(string)
	return s
}

// The suites prove the policies against what a chunked BatchJob really composes. The
// native suite's run is that template as a real API server admits it (a server-side
// dry run on envtest, every default the policy must tolerate included): with
// ADHAR_UPDATE_GOLDEN=1 this test writes job-as-admitted.json and run-template.json,
// then generate-resources.sh builds the suites from them.
func TestChunkedRunIntegrityFixturesAreTheComposedTemplates(t *testing.T) {
	native := chTemplateText(t, chNative(t))
	volcano := chTemplateText(t, chCluster(t))
	dir := chIntegrityDir(t)
	if os.Getenv("ADHAR_UPDATE_GOLDEN") == "1" {
		for suite, text := range map[string]string{"native": native, "volcano": volcano} {
			var run map[string]any
			if err := yaml.Unmarshal([]byte(text), &run); err != nil {
				t.Fatal(err)
			}
			b, _ := json.MarshalIndent(run, "", "  ")
			if err := os.WriteFile(filepath.Join(dir, suite, "run-template.json"), append(b, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		admitted := chAdmitted(t, native)
		b, _ := json.MarshalIndent(admitted, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "native", "job-as-admitted.json"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("bash", filepath.Join(dir, "generate-resources.sh")).CombinedOutput(); err != nil {
			t.Fatalf("generate-resources.sh: %v\n%s", err, out)
		}
	}
	for file, want := range map[string]string{
		"native/values.yaml":        native,
		"volcano/values.yaml":       volcano,
		"volcano/run-template.json": volcano,
		"native/run-template.json":  native,
	} {
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		got := string(raw)
		if strings.HasSuffix(file, "values.yaml") {
			got = chValuesTemplate(t, filepath.Join(dir, file))
		}
		if canonicalJSON(t, file, []byte(got)) != canonicalJSON(t, "the composed template.yaml", []byte(want)) {
			t.Errorf("policy-tests/chunked-run-integrity/%s is not the run template the composition renders for xr-chunked.yaml; run with ADHAR_UPDATE_GOLDEN=1 (it regenerates the suites) and read the diff", file)
		}
	}
	// The native run is the template as admitted: the template's own fields survive.
	raw, err := os.ReadFile(filepath.Join(dir, "native", "job-as-admitted.json"))
	if err != nil {
		t.Fatal(err)
	}
	var admitted, tpl map[string]any
	_ = json.Unmarshal(raw, &admitted)
	_ = yaml.Unmarshal([]byte(native), &tpl)
	ac, _ := vcContainerEnv(t, dig(admitted, "spec", "template").(map[string]any))
	tc, _ := vcContainerEnv(t, dig(tpl, "spec", "template").(map[string]any))
	if tqJSON(ac["env"]) != tqJSON(tc["env"]) || tqJSON(ac["envFrom"]) != tqJSON(tc["envFrom"]) {
		t.Error("native/job-as-admitted.json does not carry the composed env and envFrom; run with ADHAR_UPDATE_GOLDEN=1")
	}
	// Both suites run the shipped policies (the CLI copies the generators keep in step).
	for file, policy := range map[string]string{
		"native/kyverno-test.yaml":  "../../run-integrity/policy-under-test.yaml",
		"volcano/kyverno-test.yaml": "../../volcano-run-integrity/policy-under-test.yaml",
	} {
		docs := readYAMLDocs(t, filepath.Join(dir, file))
		if fmt.Sprint(docs[0]["policies"]) != "["+policy+"]" {
			t.Errorf("%s runs %v, want %s", file, docs[0]["policies"], policy)
		}
	}
}

// chAdmitted is the native template as a kube-apiserver admits it: a server-side dry
// run of `create` on envtest, the generated identity fields reduced to what a client
// sees in an admission request.
func chAdmitted(t *testing.T, templateText string) map[string]any {
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
		t.Fatalf("the API server refuses the chunked run: %v", err)
	}
	out := got.Object
	delete(out, "status")
	md := out["metadata"].(map[string]any)
	for _, k := range []string{"uid", "creationTimestamp", "resourceVersion", "managedFields", "generation"} {
		delete(md, k)
	}
	// The controller uid is random per request; fix it so the file is stable.
	uid := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	b, _ := json.Marshal(out)
	b = uid.ReplaceAll(b, []byte("ffee02c4-eea5-4e1f-8549-4661a269f063"))
	var fixed map[string]any
	_ = json.Unmarshal(b, &fixed)
	return fixed
}
