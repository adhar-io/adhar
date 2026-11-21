package adharplatform

// Design 0025 §18 R15 (resolved 2026-11-21): the shared RabbitMQ broker's password,
// key rabbitmq-password of Secret `rabbitmq`, exists in adhar-system only, and a pod
// reads Secrets of its own namespace only. A Queue outside adhar-system therefore
// composes ExternalSecret <queue>-broker and a chunked BatchJob composes
// <job>-batch-broker, each projecting that one key through ClusterSecretStore
// platform-messaging (security/external-secrets), whose ServiceAccount may `get`
// Secret `rabbitmq` and nothing else. In adhar-system (plane control, which the store
// does not serve) a Queue reads `rabbitmq` where it is; a BatchJob is refused there. Rendered in-process the way
// function-go-templating does and through `crossplane render` when Docker is
// available. Each guard was mutation-tested: the behaviour reverted in the
// composition or the store, the test seen failing, the fix restored.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"text/template"

	sprig "github.com/go-task/slim-sprig/v3"
	"sigs.k8s.io/yaml"
)

const queueComposition = "compositions/queue/local-rabbitmq.yaml"

// brQueue is a Queue XR of that name and namespace, as a person declares it.
func brQueue(namespace, name string) map[string]any {
	return map[string]any{
		"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Queue",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec":     map[string]any{"parameters": map[string]any{"queueName": strings.ReplaceAll(name, "-", ".")}},
	}
}

// brRenderQueue renders the Queue composition's template the way function-go-templating
// does. The template uses Sprig's `merge`, which slim-sprig lacks; for the flat dicts
// it merges here, Sprig's semantics (mergo without override: the first value of a key
// wins) are a loop.
func brRenderQueue(t *testing.T, xr map[string]any) []map[string]any {
	t.Helper()
	var comp struct {
		Spec struct {
			Pipeline []struct {
				FunctionRef struct {
					Name string `json:"name"`
				} `json:"functionRef"`
				Input struct {
					Inline struct {
						Template string `json:"template"`
					} `json:"inline"`
				} `json:"input"`
			} `json:"pipeline"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, queueComposition)), &comp); err != nil {
		t.Fatalf("%s does not parse: %v", queueComposition, err)
	}
	src := ""
	for _, s := range comp.Spec.Pipeline {
		if s.FunctionRef.Name == "function-go-templating" {
			src = s.Input.Inline.Template
		}
	}
	if src == "" {
		t.Fatalf("%s has no function-go-templating step", queueComposition)
	}
	funcs := template.FuncMap{
		"merge": func(dst map[string]any, srcs ...map[string]any) map[string]any {
			for _, s := range srcs {
				for k, v := range s {
					if _, set := dst[k]; !set {
						dst[k] = v
					}
				}
			}
			return dst
		},
	}
	tpl, err := template.New("manifests").Funcs(sprig.TxtFuncMap()).Funcs(funcs).Parse(src)
	if err != nil {
		t.Fatalf("the template does not parse: %v", err)
	}
	raw, _ := json.Marshal(map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": xr}}})
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	var out bytes.Buffer
	if err := tpl.Execute(&out, in); err != nil {
		t.Fatalf("the template does not render: %v", err)
	}
	var docs []map[string]any
	for _, d := range strings.Split(out.String(), "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(d), &m); err != nil {
			t.Fatalf("a rendered document is not YAML: %v\n%s", err, d)
		}
		if len(m) > 0 {
			docs = append(docs, m)
		}
	}
	return docs
}

// brQueueStatus is the status the Queue composition writes on its XR.
func brQueueStatus(docs []map[string]any) map[string]any {
	for _, d := range docs {
		if d["kind"] == "Queue" {
			st, _ := d["status"].(map[string]any)
			return st
		}
	}
	return nil
}

// brComposed is the composed object of that composition-resource-name, or nil.
func brComposed(docs []map[string]any, name string) map[string]any {
	return bjResource(docs, name)
}

// brSecretRefs lists every Secret a rendered object makes a pod (or a consumer) read
// in the object's own namespace: secretKeyRef, envFrom secretRef, secret volumes, an
// ExternalSecret's target, and the passwordSecret a connection Secret points at. A
// run template's template.yaml is parsed and walked too. remoteRef keys are not
// local reads (they name the Secret in the store's remote namespace).
func brSecretRefs(t *testing.T, doc map[string]any) []string {
	t.Helper()
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch m := v.(type) {
		case map[string]any:
			for k, sub := range m {
				switch k {
				case "secretKeyRef", "secretRef":
					if r, ok := sub.(map[string]any); ok {
						out = append(out, fmt.Sprint(r["name"]))
					}
				case "secret":
					if r, ok := sub.(map[string]any); ok && r["secretName"] != nil {
						out = append(out, fmt.Sprint(r["secretName"]))
					}
				case "passwordSecret":
					out = append(out, fmt.Sprint(sub))
				case "template.yaml":
					var run map[string]any
					if err := yaml.Unmarshal([]byte(fmt.Sprint(sub)), &run); err != nil {
						t.Fatalf("template.yaml does not parse: %v", err)
					}
					walk(run)
					continue
				}
				walk(sub)
			}
		case []any:
			for _, e := range m {
				walk(e)
			}
		}
	}
	walk(doc)
	if doc["kind"] == "ExternalSecret" {
		out = append(out, fmt.Sprint(dig(doc, "spec", "target", "name")))
	}
	return out
}

// brAssertBrokerES checks an ExternalSecret projects the broker's one key as `name`.
func brAssertBrokerES(t *testing.T, es map[string]any, namespace, name string) {
	t.Helper()
	if es == nil {
		t.Fatalf("no ExternalSecret %s/%s is composed", namespace, name)
	}
	if es["apiVersion"] != "external-secrets.io/v1" || es["kind"] != "ExternalSecret" ||
		dig(es, "metadata", "name") != name || dig(es, "metadata", "namespace") != namespace {
		t.Fatalf("the broker credential is %v %v %v/%v, want ExternalSecret %s/%s", es["apiVersion"], es["kind"], dig(es, "metadata", "namespace"), dig(es, "metadata", "name"), namespace, name)
	}
	if store := dig(es, "spec", "secretStoreRef"); !reflect.DeepEqual(store, map[string]any{"kind": "ClusterSecretStore", "name": "platform-messaging"}) {
		t.Errorf("read through %v, want ClusterSecretStore platform-messaging", store)
	}
	if got := tqJSON(dig(es, "spec", "target")); got != fmt.Sprintf(`{"creationPolicy":"Owner","name":%q}`, name) {
		t.Errorf("target = %s, want %s with creationPolicy Owner", got, name)
	}
	if got := tqJSON(dig(es, "spec", "data")); got != `[{"remoteRef":{"key":"rabbitmq","property":"rabbitmq-password"},"secretKey":"rabbitmq-password"}]` {
		t.Errorf("data = %s, want only rabbitmq-password from Secret rabbitmq", got)
	}
	if dig(es, "spec", "refreshInterval") != "1h" {
		t.Errorf("refreshInterval = %v, want 1h (as the D26 credential)", dig(es, "spec", "refreshInterval"))
	}
}

/* ─────────────────────────────── the Queue ─────────────────────────────── */

// A Queue in an environment namespace projects the broker's password beside itself
// and every reader of it — its own declare Job, and its consumers through the
// connection Secret's passwordSecret — names that copy.
func TestQueueProjectsTheBrokerCredentialBesideItself(t *testing.T) {
	for _, ns := range []string{"dev", "test", "prod"} {
		t.Run(ns, func(t *testing.T) {
			docs := brRenderQueue(t, brQueue(ns, "ledger-chunks"))
			brAssertBrokerES(t, brComposed(docs, "queue-broker"), ns, "ledger-chunks-broker")
			if l := dig(brComposed(docs, "queue-broker"), "metadata", "labels", "platform.adhar.io/queue"); l != "ledger-chunks" {
				t.Errorf("the ExternalSecret lacks the Queue's label: %v", l)
			}
			job := brComposed(docs, "queue-job")
			var pass any
			for _, c := range asList(dig(job, "spec", "template", "spec", "containers")) {
				for _, e := range asList(c.(map[string]any)["env"]) {
					if m := e.(map[string]any); m["name"] == "RABBIT_PASS" {
						pass = dig(m, "valueFrom", "secretKeyRef")
					}
				}
			}
			if tqJSON(pass) != `{"key":"rabbitmq-password","name":"ledger-chunks-broker"}` {
				t.Errorf("the declare Job reads the password from %s", tqJSON(pass))
			}
			conn := brComposed(docs, "queue-conn")
			if dig(conn, "stringData", "passwordSecret") != "ledger-chunks-broker" || dig(conn, "stringData", "passwordKey") != "rabbitmq-password" {
				t.Errorf("the connection Secret points at %v/%v", dig(conn, "stringData", "passwordSecret"), dig(conn, "stringData", "passwordKey"))
			}
			if dig(conn, "metadata", "name") != "ledger-chunks-queue" || brQueueStatus(docs)["connectionSecret"] != "ledger-chunks-queue" {
				t.Errorf("the connection Secret is %v (status %v), want ledger-chunks-queue: every chunked run mounts it by that name",
					dig(conn, "metadata", "name"), brQueueStatus(docs)["connectionSecret"])
			}
		})
	}
}

// In adhar-system the broker's Secret is already there and the store does not serve
// the namespace (plane control): no ExternalSecret, and everything reads `rabbitmq`.
func TestQueueInAdharSystemReadsTheBrokerSecretInPlace(t *testing.T) {
	docs := brRenderQueue(t, brQueue("adhar-system", "platform-events"))
	if es := brComposed(docs, "queue-broker"); es != nil {
		t.Errorf("a Queue in adhar-system composes %v, which platform-messaging would refuse to serve", dig(es, "metadata", "name"))
	}
	for _, d := range docs {
		if d["kind"] == "ExternalSecret" {
			t.Errorf("a Queue in adhar-system composes ExternalSecret %v", dig(d, "metadata", "name"))
		}
	}
	refs := map[string]bool{}
	for _, name := range []string{"queue-job", "queue-conn"} {
		for _, r := range brSecretRefs(t, brComposed(docs, name)) {
			refs[r] = true
		}
	}
	if !reflect.DeepEqual(refs, map[string]bool{"rabbitmq": true}) {
		t.Errorf("a Queue in adhar-system reads Secrets %v, want only rabbitmq", refs)
	}
}

/* ───────────────────────────── the BatchJob ───────────────────────────── */

// A chunked job projects the same one key beside itself as <job>-batch-broker, and
// every pod of the run, on both paths, reads it there.
func TestChunkedRunProjectsTheBrokerCredential(t *testing.T) {
	for _, c := range []struct {
		name    string
		cluster func(t *testing.T) []map[string]any
	}{{"native", func(t *testing.T) []map[string]any { return chNative(t) }}, {"volcano", func(t *testing.T) []map[string]any { return chCluster(t) }}} {
		t.Run(c.name, func(t *testing.T) {
			docs := vcMustRender(t, chXR(t), c.cluster(t), nil)
			es := brComposed(docs, "broker-credentials")
			brAssertBrokerES(t, es, "dev", "ledger-batch-broker")
			if dig(es, "metadata", "labels", "adhar.io/batch-job") != "ledger" {
				t.Errorf("the ExternalSecret lacks the job's labels: %v", dig(es, "metadata", "labels"))
			}
			if dig(es, "metadata", "annotations", "gotemplating.fn.crossplane.io/ready") != "False" {
				t.Error("an ExternalSecret not yet observed is declared ready")
			}
			run, _ := vcTemplate(t, docs)
			for role, pod := range vcPods(run) {
				_, env := vcContainerEnv(t, pod)
				if got := tqJSON(env["ADHAR_BATCH_CHUNKING_PASSWORD"]); got != `{"secretKeyRef":{"key":"rabbitmq-password","name":"ledger-batch-broker"}}` {
					t.Errorf("%s pod: ADHAR_BATCH_CHUNKING_PASSWORD = %s", role, got)
				}
			}
		})
	}
	observed := vcMustRender(t, chXR(t), chNative(t), map[string]any{"broker-credentials": map[string]any{"resource": map[string]any{"metadata": map[string]any{"uid": "u1"}}}})
	if dig(brComposed(observed, "broker-credentials"), "metadata", "annotations", "gotemplating.fn.crossplane.io/ready") != "True" {
		t.Error("the ExternalSecret is not ready once observed")
	}
}

// The run's password and the Queue's are the same credential: both projections read
// one remote key, and the key the run reads is the one the Queue's connection Secret
// says (passwordKey). If either composition moved the password, chunked runs would
// start without a broker.
func TestChunkedRunReadsThePasswordTheQueueReferences(t *testing.T) {
	q := brRenderQueue(t, brQueue("dev", "ledger-chunks"))
	b := vcMustRender(t, chXR(t), chNative(t), nil)
	qes, bes := brComposed(q, "queue-broker"), brComposed(b, "broker-credentials")
	if qes == nil || bes == nil {
		t.Fatalf("queue-broker %v, broker-credentials %v", qes != nil, bes != nil)
	}
	if a, z := tqJSON(dig(qes, "spec", "data")), tqJSON(dig(bes, "spec", "data")); a != z {
		t.Errorf("the Queue projects %s, the chunked job %s", a, z)
	}
	if a, z := tqJSON(dig(qes, "spec", "secretStoreRef")), tqJSON(dig(bes, "spec", "secretStoreRef")); a != z {
		t.Errorf("the Queue reads through %s, the chunked job through %s", a, z)
	}
	key := dig(brComposed(q, "queue-conn"), "stringData", "passwordKey")
	run, _ := vcTemplate(t, b)
	_, env := vcContainerEnv(t, dig(run, "spec", "template").(map[string]any))
	if got := dig(env["ADHAR_BATCH_CHUNKING_PASSWORD"].(map[string]any), "secretKeyRef", "key"); got != key {
		t.Errorf("the run reads key %v, the Queue's connection Secret names %v", got, key)
	}
}

// Only chunked jobs talk to the broker, so only they get its credential.
func TestOnlyChunkedJobsProjectTheBrokerCredential(t *testing.T) {
	for name, docs := range map[string][]map[string]any{
		"single":      bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil),
		"partitioned": vcMustRender(t, vcXR(t, "xr-partitioned.yaml"), vcNoVolcano(t), nil),
	} {
		if es := brComposed(docs, "broker-credentials"); es != nil {
			t.Errorf("a %s job composes %v", name, dig(es, "metadata", "name"))
		}
		for _, d := range docs {
			for _, r := range brSecretRefs(t, d) {
				if strings.HasSuffix(r, "-broker") || r == "rabbitmq" {
					t.Errorf("a %s job reads broker Secret %s", name, r)
				}
			}
		}
	}
}

// There is no adhar-system case for a BatchJob: the composition refuses one there
// (runs belong in an environment namespace), so a chunked job is always somewhere
// platform-messaging serves. Should that refusal ever go, this test says the broker
// projection needs an adhar-system branch, as the Queue's has.
func TestChunkedRunIsNeverInAdharSystem(t *testing.T) {
	xr := bjWithMeta(chXR(t), map[string]any{"namespace": "adhar-system"})
	var cluster []map[string]any // the fixture cluster, moved into adhar-system
	for _, d := range chNative(t) {
		if dig(d, "metadata", "namespace") == "dev" {
			d = bjWithMeta(d, map[string]any{"namespace": "adhar-system"})
		}
		if d["kind"] == "Namespace" && dig(d, "metadata", "name") == "dev" {
			d = bjWithMeta(d, map[string]any{"name": "adhar-system"})
		}
		cluster = append(cluster, d)
	}
	if _, err := vcRender(t, xr, cluster, nil); err == nil || !strings.Contains(err.Error(), "a BatchJob may not live in adhar-system") {
		t.Errorf("a chunked BatchJob in adhar-system: %v; if it is now allowed, it must read Secret rabbitmq in place (platform-messaging does not serve adhar-system)", err)
	}
}

/* ─────────────────────────────── the guards ─────────────────────────────── */

// The R15 guard: no Queue and no chunked BatchJob render, in any environment
// namespace and on either path, makes anything read a Secret named `rabbitmq`
// outside adhar-system, where no such Secret exists.
func TestNoBrokerRenderReadsRabbitmqOutsideAdharSystem(t *testing.T) {
	renders := map[string][]map[string]any{}
	for _, ns := range []string{"dev", "test", "prod"} {
		renders["Queue in "+ns] = brRenderQueue(t, brQueue(ns, "ledger-chunks"))
	}
	renders["Queue with exchange and no dead-letter queue"] = brRenderQueue(t, func() map[string]any {
		q := brQueue("dev", "orders")
		q["spec"].(map[string]any)["parameters"] = map[string]any{"exchange": map[string]any{"name": "orders"}, "deadLetter": map[string]any{"enabled": false}}
		return q
	}())
	renders["chunked BatchJob, native"] = vcMustRender(t, chXR(t), chNative(t), nil)
	renders["chunked BatchJob, Volcano"] = vcMustRender(t, chXR(t), chCluster(t), nil)
	renders["chunked BatchJob, scheduled"] = vcMustRender(t, bjWithParams(chXR(t), map[string]any{"schedule": map[string]any{"cron": "0 2 * * *"}}), chNative(t), nil)
	for what, docs := range renders {
		seen := 0
		for _, d := range docs {
			ns := fmt.Sprint(dig(d, "metadata", "namespace"))
			if d["kind"] == "Queue" || d["kind"] == "BatchJob" || d["kind"] == "ExtraResources" || ns == "adhar-system" {
				continue
			}
			for _, r := range brSecretRefs(t, d) {
				seen++
				if r == "rabbitmq" {
					t.Errorf("%s: %s %s/%v reads Secret rabbitmq, which exists only in adhar-system", what, d["kind"], ns, dig(d, "metadata", "name"))
				}
			}
		}
		if seen == 0 {
			t.Errorf("%s: no Secret reference was found at all; the walk is not looking where the composition writes them", what)
		}
	}
}

// Two Queues in one namespace (which chunked mode requires), and a Queue and a
// BatchJob of one name, compose no object in common: Crossplane does not adopt an
// object another XR controls, and ESO refuses a target another ExternalSecret owns.
func TestBrokerCredentialsArePerObject(t *testing.T) {
	type key struct{ apiVersion, kind, namespace, name string }
	owner := map[key]string{}
	targets := map[string]string{}
	add := func(who string, docs []map[string]any) {
		for _, d := range docs {
			if d["kind"] == "Queue" || d["kind"] == "BatchJob" || d["kind"] == "ExtraResources" || dig(d, "metadata", "name") == nil {
				continue
			}
			k := key{fmt.Sprint(d["apiVersion"]), fmt.Sprint(d["kind"]), fmt.Sprint(dig(d, "metadata", "namespace")), fmt.Sprint(dig(d, "metadata", "name"))}
			if prev, dup := owner[k]; dup {
				t.Errorf("%s and %s both compose %v", prev, who, k)
			}
			owner[k] = who
			if d["kind"] == "ExternalSecret" {
				tn := fmt.Sprint(dig(d, "spec", "target", "name"))
				if prev, dup := targets[tn]; dup {
					t.Errorf("%s and %s both write Secret %s", prev, who, tn)
				}
				targets[tn] = who
			}
		}
	}
	add("Queue ledger-chunks", brRenderQueue(t, brQueue("dev", "ledger-chunks")))
	add("Queue ledger-replies", brRenderQueue(t, brQueue("dev", "ledger-replies")))
	add("Queue ledger", brRenderQueue(t, brQueue("dev", "ledger")))
	add("BatchJob ledger", vcMustRender(t, chXR(t), chNative(t), nil))
	if len(targets) != 3+1 {
		t.Errorf("broker credentials written: %v, want one per Queue and one for the job", targets)
	}
}

// The store's half of R15: platform-messaging serves the environment namespaces
// (workload, preview) and only them, reads adhar-system, and its ServiceAccount may
// get Secret `rabbitmq` — which also holds the Erlang cookie — and no other Secret.
func TestThePlatformMessagingStoreReadsOnlyTheBrokerSecret(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	var store, role, binding map[string]any
	for _, d := range readYAMLDocs(t, filepath.Join(root, "platform/stack/packages/security/external-secrets/manifests/platform-store.yaml")) {
		switch {
		case d["kind"] == "ClusterSecretStore" && dig(d, "metadata", "name") == "platform-messaging":
			store = d
		case d["kind"] == "Role" && dig(d, "metadata", "name") == "eso-store-messaging":
			role = d
		case d["kind"] == "RoleBinding" && dig(d, "metadata", "name") == "eso-store-messaging":
			binding = d
		}
	}
	if store == nil || role == nil || binding == nil {
		t.Fatalf("security/external-secrets ships store %v, Role %v, RoleBinding %v", store != nil, role != nil, binding != nil)
	}
	if got := tqJSON(dig(store, "spec", "conditions")); got != `[{"namespaceSelector":{"matchExpressions":[{"key":"adhar.io/plane","operator":"In","values":["workload","preview"]}]}}]` {
		t.Errorf("platform-messaging serves %s, want exactly the adhar.io/plane workload and preview namespaces", got)
	}
	k := dig(store, "spec", "provider", "kubernetes").(map[string]any)
	if k["remoteNamespace"] != "adhar-system" || tqJSON(dig(k, "auth", "serviceAccount")) != `{"name":"eso-store-messaging","namespace":"adhar-system"}` {
		t.Errorf("the store reads %v as %s", k["remoteNamespace"], tqJSON(dig(k, "auth", "serviceAccount")))
	}
	if dig(role, "metadata", "namespace") != "adhar-system" {
		t.Errorf("the Role is in %v", dig(role, "metadata", "namespace"))
	}
	secretRules := 0
	for _, r := range asList(role["rules"]) {
		m := r.(map[string]any)
		if !strings.Contains(fmt.Sprint(m["resources"]), "secrets") && !strings.Contains(fmt.Sprint(m["resources"]), "*") {
			continue
		}
		secretRules++
		if tqJSON(m["resourceNames"]) != `["rabbitmq"]` || tqJSON(m["verbs"]) != `["get"]` || tqJSON(m["resources"]) != `["secrets"]` {
			t.Errorf("the store's Role grants %s on %s named %s, want get on secrets named [rabbitmq] only", tqJSON(m["verbs"]), tqJSON(m["resources"]), tqJSON(m["resourceNames"]))
		}
	}
	if secretRules != 1 {
		t.Errorf("the store's Role has %d rules on secrets, want exactly one", secretRules)
	}
	if tqJSON(binding["subjects"]) != `[{"kind":"ServiceAccount","name":"eso-store-messaging","namespace":"adhar-system"}]` ||
		dig(binding, "roleRef", "name") != "eso-store-messaging" || dig(binding, "roleRef", "kind") != "Role" {
		t.Errorf("the RoleBinding binds %s to %v", tqJSON(binding["subjects"]), binding["roleRef"])
	}
	// The source exists where the store looks: the chart's Secret rabbitmq, with the key.
	found := false
	for _, d := range readYAMLDocs(t, filepath.Join(root, "platform/stack/packages/data/rabbitmq/manifests/install.yaml")) {
		if d["kind"] == "Secret" && dig(d, "metadata", "name") == "rabbitmq" && dig(d, "metadata", "namespace") == "adhar-system" {
			_, found = dig(d, "data").(map[string]any)["rabbitmq-password"]
		}
	}
	if !found {
		t.Error("data/rabbitmq no longer ships Secret adhar-system/rabbitmq with key rabbitmq-password, which platform-messaging projects")
	}
}

/* ─────────────────────────── `crossplane render` ─────────────────────────── */

// The real function containers compose, for a Queue in dev and one in adhar-system,
// exactly what the in-process render composes.
func TestQueueBrokerCredentialRendersWithCrossplane(t *testing.T) {
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
	for _, ns := range []string{"dev", "adhar-system"} {
		t.Run(ns, func(t *testing.T) {
			xr := brQueue(ns, "ledger-chunks")
			y, _ := yaml.Marshal(xr)
			xrFile := filepath.Join(dir, ns+"-xr.yaml")
			if err := os.WriteFile(xrFile, y, 0o644); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(bin, "render", xrFile, filepath.Join(controlPlaneDir(t), queueComposition), filepath.Join(bjFixtureDir, "functions.yaml"))
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := runRender(cmd); err != nil {
				t.Fatalf("crossplane render: %v\n%s", err, stderr.String())
			}
			real := map[string]map[string]any{}
			for _, d := range strings.Split(stdout.String(), "\n---") {
				var m map[string]any
				if yaml.Unmarshal([]byte(d), &m) != nil || len(m) == 0 {
					continue
				}
				if n, _ := dig(m, "metadata", "annotations", "crossplane.io/composition-resource-name").(string); n != "" {
					real[n] = m
				}
			}
			inProcess := map[string]map[string]any{}
			for _, d := range brRenderQueue(t, xr) {
				if n, _ := dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name").(string); n != "" {
					inProcess[n] = d
				}
			}
			a, b := keysOf(real), keysOf(inProcess)
			sort.Strings(a)
			sort.Strings(b)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("crossplane render composes %v, the in-process render %v", a, b)
			}
			for n, d := range inProcess {
				for _, field := range []string{"spec", "stringData"} {
					if tqJSON(real[n][field]) != tqJSON(d[field]) {
						t.Errorf("%s.%s differs between crossplane render and the in-process render:\n%s\n%s", n, field, tqJSON(real[n][field]), tqJSON(d[field]))
					}
				}
			}
			_, hasES := real["queue-broker"]
			if hasES != (ns != "adhar-system") {
				t.Errorf("in %s crossplane render composes queue-broker = %v", ns, hasES)
			}
		})
	}
}
