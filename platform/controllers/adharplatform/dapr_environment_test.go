package adharplatform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"text/template"

	sprig "github.com/go-task/slim-sprig/v3"
	"sigs.k8s.io/yaml"
)

// Every environment's Dapr building blocks are composed by the Environment
// composition (compositions/env/local.yaml): five Components, the adhar-dapr
// Configuration, a default Resiliency and the Role an app binds to read
// Secrets. Until 2026-10-27 the only Components were two in adhar-system —
// invisible to the dev/test/prod pods Dapr apps run in, one of them pointing at
// a Redis that did not exist.
//
// These render the composition the way function-go-templating does (the same
// template language, twice, the second time with the required resources), and
// through `crossplane render` when Docker is available.

const (
	daprEnvComposition = "compositions/env/local.yaml"
	daprKafkaBrokers   = "adhar-kafka-kafka-bootstrap.adhar-system.svc.cluster.local:9092"
	daprOTLPEndpoint   = "alloy.adhar-system.svc.cluster.local:4317"
)

// The Dapr objects every environment gets, by composition resource name.
var daprEnvResources = []string{"dapr-configstore", "dapr-configuration", "dapr-lockstore", "dapr-pubsub",
	"dapr-resiliency", "dapr-secret-reader", "dapr-secretstore", "dapr-statestore"}

func daprEnvXR(tier string, dapr map[string]any) map[string]any {
	p := map[string]any{"name": tier, "tier": tier, "environment": tier, "organisation": "adhar", "team": "platform",
		"project": "adhar", "access": "admin", "cpuQuota": "16", "memoryQuota": "32Gi", "podQuota": 100,
		"enableNetworkPolicy": true}
	if dapr != nil {
		p["dapr"] = dapr
	}
	return map[string]any{"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Environment",
		"metadata": map[string]any{"name": "adhar-" + tier, "namespace": "adhar-system"},
		"spec":     map[string]any{"parameters": p}}
}

// daprCluster is what the composition can find: Dapr's components CRD (Dapr
// is installed) and adhar-kafka's bootstrap Service (the platform runs Kafka).
func daprCluster(dapr, kafka bool) []map[string]any {
	var out []map[string]any
	if dapr {
		out = append(out, map[string]any{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
			"metadata": map[string]any{"name": "components.dapr.io"}})
	}
	if kafka {
		out = append(out, map[string]any{"apiVersion": "v1", "kind": "Service",
			"metadata": map[string]any{"name": "adhar-kafka-kafka-bootstrap", "namespace": "adhar-system"}})
	}
	return out
}

func daprEnvExecute(t *testing.T, req map[string]any) ([]map[string]any, error) {
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
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, "compositions", "env", "local.yaml")), &comp); err != nil {
		t.Fatalf("%s does not parse: %v", daprEnvComposition, err)
	}
	src := ""
	for _, s := range comp.Spec.Pipeline {
		if s.FunctionRef.Name == "function-go-templating" {
			src = s.Input.Inline.Template
		}
	}
	if src == "" {
		t.Fatalf("%s has no function-go-templating step", daprEnvComposition)
	}
	funcs := template.FuncMap{
		"getExtraResources": func(r map[string]any, name string) []any {
			for _, key := range []string{"requiredResources", "extraResources"} {
				if m, ok := r[key].(map[string]any); ok {
					if rs, ok := m[name].(map[string]any); ok {
						items, _ := rs["items"].([]any)
						return items
					}
				}
			}
			return nil
		},
	}
	tpl, err := template.New("environment").Funcs(sprig.TxtFuncMap()).Funcs(funcs).Parse(src)
	if err != nil {
		t.Fatalf("the template does not parse: %v", err)
	}
	raw, _ := json.Marshal(req) // the function hands the template protojson: numbers are float64
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, in); err != nil {
		return nil, err
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
	return docs, nil
}

// daprEnvRender runs the template as Crossplane does: once to learn what it
// requires, then again with those resources.
func daprEnvRender(t *testing.T, xr map[string]any, cluster []map[string]any) []map[string]any {
	t.Helper()
	req := map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": xr}}}
	docs, err := daprEnvExecute(t, req)
	if err != nil {
		t.Fatalf("the composition refused %v: %v", dig(xr, "metadata", "name"), err)
	}
	required := map[string]any{}
	for _, d := range docs {
		if d["kind"] != "ExtraResources" {
			continue
		}
		for key, raw := range d["requirements"].(map[string]any) {
			sel := raw.(map[string]any)
			var items []any
			for _, obj := range cluster {
				if obj["apiVersion"] == sel["apiVersion"] && obj["kind"] == sel["kind"] &&
					dig(obj, "metadata", "name") == sel["matchName"] &&
					fmt.Sprint(dig(obj, "metadata", "namespace")) == fmt.Sprint(sel["namespace"]) {
					items = append(items, map[string]any{"resource": obj})
				}
			}
			required[key] = map[string]any{"items": items}
		}
	}
	req["requiredResources"] = required
	docs, err = daprEnvExecute(t, req)
	if err != nil {
		t.Fatalf("the composition refused %v with its required resources: %v", dig(xr, "metadata", "name"), err)
	}
	return docs
}

// daprEnvManifests: the manifest of every composed Object, by resource name.
func daprEnvManifests(docs []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, d := range docs {
		if d["kind"] != "Object" {
			continue
		}
		name, _ := dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name").(string)
		m, _ := dig(d, "spec", "forProvider", "manifest").(map[string]any)
		out[name] = m
	}
	return out
}

func daprEnvStatus(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == "Environment" {
			s, _ := dig(d, "status", "dapr").(map[string]any)
			return s
		}
	}
	t.Fatal("the composition writes no status to the Environment")
	return nil
}

func daprResourceNames(objs map[string]map[string]any) []string {
	var out []string
	for n := range objs {
		if strings.HasPrefix(n, "dapr-") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// daprKafkaVersion parses "x.y.z" into a comparable number.
func daprKafkaVersion(t *testing.T, v string) int {
	t.Helper()
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		t.Fatalf("Kafka version %q is not x.y.z", v)
	}
	n := 0
	for _, p := range parts {
		i, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("Kafka version %q: %v", v, err)
		}
		n = n*1000 + i
	}
	return n
}

// The contract: names Adhar Kit and the Function composition rely on, each
// store isolated per namespace or per tier where Dapr does not isolate it, and
// the protocol details the platform's Kafka needs.
func TestEnvironmentComposesTheDaprContract(t *testing.T) {
	docs := daprEnvRender(t, daprEnvXR("dev", nil), daprCluster(true, true))
	objs := daprEnvManifests(docs)
	if got := strings.Join(daprResourceNames(objs), ","); got != strings.Join(daprEnvResources, ",") {
		t.Fatalf("an environment on a cluster with Dapr and Kafka composes %s, want %s", got, strings.Join(daprEnvResources, ","))
	}
	want := map[string][2]string{
		"dapr-pubsub": {"Component", "pubsub"}, "dapr-statestore": {"Component", "statestore"},
		"dapr-lockstore": {"Component", "lockstore"}, "dapr-configstore": {"Component", "configstore"},
		"dapr-secretstore": {"Component", "secretstore"}, "dapr-configuration": {"Configuration", "adhar-dapr"},
		"dapr-resiliency": {"Resiliency", "adhar-default"}, "dapr-secret-reader": {"Role", "dapr-secret-reader"},
	}
	actorStores := 0
	for res, kn := range want {
		m := objs[res]
		if m["kind"] != kn[0] || dig(m, "metadata", "name") != kn[1] || dig(m, "metadata", "namespace") != "dev" {
			t.Errorf("%s composes %v %v/%v, want %s dev/%s", res, m["kind"], dig(m, "metadata", "namespace"), dig(m, "metadata", "name"), kn[0], kn[1])
		}
		if dig(m, "metadata", "labels", "adhar.io/environment") != "dev" {
			t.Errorf("%s does not carry adhar.io/environment", res)
		}
		if m["kind"] == "Component" {
			md, secret := daprComponentMetadata(t, m)
			if secret {
				t.Errorf("%s reads a Secret; the environment's stores are credential-less", res)
			}
			if md["actorStateStore"] == "true" {
				actorStores++
			}
		}
	}
	if actorStores != 1 {
		t.Errorf("%d components are the actor state store; Dapr refuses more than one", actorStores)
	}

	ps, _ := daprComponentMetadata(t, objs["dapr-pubsub"])
	if dig(objs["dapr-pubsub"], "spec", "type") != "pubsub.kafka" || ps["brokers"] != daprKafkaBrokers || ps["authType"] != "none" {
		t.Errorf("with Kafka running, pubsub must be pubsub.kafka on %s with authType none (the plain listener): %v", daprKafkaBrokers, ps)
	}
	// Dapr defaults the protocol version to 2.0.0; Kafka 4 accepts nothing
	// below 2.1 (KIP-896) and the platform's cluster runs 4.x.
	if v := ps["version"]; v == "" || daprKafkaVersion(t, v) < daprKafkaVersion(t, "2.1.0") {
		t.Errorf("pubsub.kafka version = %q; leaving it unset speaks Kafka 2.0.0, below what Kafka 4 accepts", v)
	}
	if !strings.Contains(ps["consumerID"], "{namespace}") {
		t.Errorf("pubsub consumerID = %q; without {namespace} every environment shares the same Kafka topics (dev's events reach prod)", ps["consumerID"])
	}

	ss, _ := daprComponentMetadata(t, objs["dapr-statestore"])
	if dig(objs["dapr-statestore"], "spec", "type") != "state.redis" || ss["redisHost"] != daprStateHost || ss["keyPrefix"] != "namespace" || ss["actorStateStore"] != "true" {
		t.Errorf("statestore must be state.redis on dapr-state, keyPrefix namespace, the actor store: %v", ss)
	}
	ls, _ := daprComponentMetadata(t, objs["dapr-lockstore"])
	if dig(objs["dapr-lockstore"], "spec", "type") != "lock.redis" || ls["redisHost"] != daprStateHost || !strings.Contains(ls["keyPrefix"], "{namespace}") {
		t.Errorf("lockstore must be lock.redis on dapr-state with a {namespace} keyPrefix (Dapr's default prefix is the app-id alone): %v", ls)
	}
	cs, _ := daprComponentMetadata(t, objs["dapr-configstore"])
	if dig(objs["dapr-configstore"], "spec", "type") != "configuration.redis" || cs["redisHost"] != daprStateHost || cs["redisDB"] == ss["redisDB"] {
		t.Errorf("configstore must be configuration.redis on dapr-state in a database apart from state (a Get-all is KEYS *): %v vs state %v", cs, ss["redisDB"])
	}
	if dig(objs["dapr-secretstore"], "spec", "type") != "secretstores.kubernetes" {
		t.Errorf("secretstore is %v", dig(objs["dapr-secretstore"], "spec", "type"))
	}
	rules, _ := dig(objs["dapr-secret-reader"], "rules").([]any)
	if raw, _ := json.Marshal(rules); string(raw) != `[{"apiGroups":[""],"resources":["secrets"],"verbs":["get","list"]}]` {
		t.Errorf("dapr-secret-reader grants %s; it is exactly what secretstore needs: get and list Secrets", raw)
	}

	conf := objs["dapr-configuration"]
	if dig(conf, "spec", "tracing", "otel", "endpointAddress") != daprOTLPEndpoint || dig(conf, "spec", "tracing", "otel", "protocol") != "grpc" ||
		dig(conf, "spec", "tracing", "otel", "isSecure") != false || dig(conf, "spec", "tracing", "samplingRate") == nil {
		t.Errorf("adhar-dapr must export traces to Alloy over OTLP gRPC with a sampling rate: %v", dig(conf, "spec", "tracing"))
	}
	if dig(conf, "spec", "metrics", "enabled") != true || dig(conf, "spec", "metrics", "recordErrorCodes") != true ||
		dig(conf, "spec", "metrics", "http", "increasedCardinality") != false {
		t.Errorf("adhar-dapr metrics = %v; want enabled, recordErrorCodes and increasedCardinality false (unset, Dapr 1.18 labels every raw path)", dig(conf, "spec", "metrics"))
	}
	if pm, _ := dig(conf, "spec", "metrics", "http", "pathMatching").([]any); len(pm) == 0 {
		t.Error("adhar-dapr has no pathMatching: with low cardinality every path label is empty")
	}
	if dig(conf, "spec", "mtls") != nil {
		t.Error("adhar-dapr sets mtls; Configuration/daprsystem governs it cluster-wide")
	}

	res := objs["dapr-resiliency"]
	if _, ok := dig(res, "spec", "targets").(map[string]any); !ok {
		t.Error("the Resiliency has no targets map (the CRD requires one)")
	}
	for _, kind := range []string{"timeouts", "retries", "circuitBreakers"} {
		ps, _ := dig(res, "spec", "policies", kind).(map[string]any)
		for name := range ps {
			if !strings.HasPrefix(name, "Default") {
				t.Errorf("Resiliency policy %s is not a Default* policy; only defaults leave an app's own Resiliency in charge", name)
			}
			if name == "DefaultRetryPolicy" || name == "DefaultAppRetryPolicy" {
				t.Errorf("%s retries every service invocation, POSTs included: replaying a request is the app's decision", name)
			}
		}
	}
	if s := daprEnvStatus(t, docs); s["enabled"] != true || s["pubsub"] != "kafka" || s["configuration"] != "adhar-dapr" {
		t.Errorf("status.dapr = %v", s)
	}
}

// The broker follows the cluster — Kafka when adhar-kafka runs, Redis streams
// on dapr-state otherwise — unless the Environment pins it.
func TestEnvironmentDaprPubsubFollowsTheClusterUnlessPinned(t *testing.T) {
	for _, tc := range []struct {
		kafka bool
		pin   string
		want  string
	}{
		{true, "", "kafka"}, {false, "", "redis"}, {true, "auto", "kafka"}, {false, "auto", "redis"},
		{true, "redis", "redis"}, {false, "kafka", "kafka"},
	} {
		t.Run(fmt.Sprintf("kafka=%v,pin=%q", tc.kafka, tc.pin), func(t *testing.T) {
			var dapr map[string]any
			if tc.pin != "" {
				dapr = map[string]any{"pubsub": tc.pin}
			}
			docs := daprEnvRender(t, daprEnvXR("test", dapr), daprCluster(true, tc.kafka))
			ps := daprEnvManifests(docs)["dapr-pubsub"]
			md, _ := daprComponentMetadata(t, ps)
			if typ := dig(ps, "spec", "type"); typ != "pubsub."+tc.want {
				t.Errorf("pubsub is %v, want pubsub.%s", typ, tc.want)
			}
			if !strings.Contains(md["consumerID"], "{namespace}") {
				t.Errorf("pubsub consumerID = %q; topics must be namespaced on every broker", md["consumerID"])
			}
			if tc.want == "redis" && (md["redisHost"] != daprStateHost || md["maxLenApprox"] == "") {
				t.Errorf("Redis-streams pubsub must use dapr-state and cap its streams (they share the store's maxmemory with state): %v", md)
			}
			if s := daprEnvStatus(t, docs); s["pubsub"] != tc.want {
				t.Errorf("status.dapr.pubsub = %v, want %s", s["pubsub"], tc.want)
			}
		})
	}
}

// The local profile does not install Dapr: an environment there must compose
// exactly what it always did. A Component on a cluster without the CRD is an
// Object that never becomes Ready, and an Environment (and its Project) that
// never does either.
func TestEnvironmentComposesNoDaprWhereDaprDoesNotRun(t *testing.T) {
	without := daprEnvRender(t, daprEnvXR("dev", nil), daprCluster(false, true))
	if names := daprResourceNames(daprEnvManifests(without)); len(names) != 0 {
		t.Errorf("without Dapr installed the environment composes %v", names)
	}
	for _, base := range []string{"namespace", "quota", "limits", "netpol", "netpol-gateway", "team-rbac", "org-admin-rbac"} {
		if daprEnvManifests(without)[base] == nil {
			t.Errorf("without Dapr the environment no longer composes %s", base)
		}
	}
	if s := daprEnvStatus(t, without); s["enabled"] != false || !strings.Contains(fmt.Sprint(s["message"]), "not installed") {
		t.Errorf("status.dapr without Dapr = %v", s)
	}
	off := daprEnvRender(t, daprEnvXR("dev", map[string]any{"enabled": false}), daprCluster(true, true))
	if names := daprResourceNames(daprEnvManifests(off)); len(names) != 0 {
		t.Errorf("with parameters.dapr.enabled false the environment composes %v", names)
	}
	// What decides it is a required resource, named in the template.
	docs := daprEnvRender(t, daprEnvXR("dev", nil), nil)
	found := map[string]bool{}
	for _, d := range docs {
		if d["kind"] == "ExtraResources" {
			for _, sel := range d["requirements"].(map[string]any) {
				found[fmt.Sprint(sel.(map[string]any)["matchName"])] = true
			}
		}
	}
	if !found["components.dapr.io"] || !found["adhar-kafka-kafka-bootstrap"] {
		t.Errorf("the composition must require components.dapr.io and adhar-kafka-kafka-bootstrap; it requires %v", found)
	}
}

// Dapr prefixes neither actor (or workflow) state nor configuration keys with
// the namespace, so the tier's database is what keeps dev's apart from prod's:
// the same app promoted through three environments has the same app-id in each.
func TestDaprTierDatabasesKeepEnvironmentsApart(t *testing.T) {
	conf := regexp.MustCompile(`(?m)^\s*databases (\d+)$`).FindStringSubmatch(readStackFile(t, "application/dapr/manifests/state-store.yaml"))
	if conf == nil {
		t.Fatal("dapr-state's valkey.conf sets no `databases`")
	}
	limit, _ := strconv.Atoi(conf[1])
	seen := map[string]string{"0": "the platform namespace (manifests/pubsub.yaml, statestore.yaml)"}
	for _, tier := range []string{"dev", "test", "prod"} {
		objs := daprEnvManifests(daprEnvRender(t, daprEnvXR(tier, map[string]any{"pubsub": "redis"}), daprCluster(true, false)))
		state, _ := daprComponentMetadata(t, objs["dapr-statestore"])
		cfg, _ := daprComponentMetadata(t, objs["dapr-configstore"])
		lock, _ := daprComponentMetadata(t, objs["dapr-lockstore"])
		stream, _ := daprComponentMetadata(t, objs["dapr-pubsub"])
		if lock["redisDB"] != state["redisDB"] || stream["redisDB"] != state["redisDB"] {
			t.Errorf("%s: locks (%s) and streams (%s) belong in the tier's state database (%s)", tier, lock["redisDB"], stream["redisDB"], state["redisDB"])
		}
		for what, db := range map[string]string{tier + " state": state["redisDB"], tier + " configuration": cfg["redisDB"]} {
			n, err := strconv.Atoi(db)
			if err != nil || n < 0 || n >= limit {
				t.Errorf("%s uses database %q; dapr-state has %d", what, db, limit)
			}
			if other, dup := seen[db]; dup {
				t.Errorf("%s shares database %s with %s", what, db, other)
			}
			seen[db] = what
		}
	}
}

// Dapr registers every pathMatching entry (plus its /v1.0/invoke/{app_id}/method
// variant) in a Go ServeMux at sidecar start, and ServeMux PANICS on a
// conflicting or malformed pattern — every sidecar in the environment would
// crash. This registers them exactly as Dapr 1.18.3 does
// (pkg/diagnostics/http_monitoring_path_matching.go) and checks the labels.
func TestDaprPathPatternsRegisterLikeDaprRegistersThem(t *testing.T) {
	conf := daprEnvManifests(daprEnvRender(t, daprEnvXR("dev", nil), daprCluster(true, false)))["dapr-configuration"]
	var patterns []string
	for _, p := range dig(conf, "spec", "metrics", "http", "pathMatching").([]any) {
		patterns = append(patterns, p.(string))
	}
	normalize := func(p string) string {
		for strings.Contains(p, "//") {
			p = strings.ReplaceAll(p, "//", "/")
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return p
	}
	var all []string
	for _, p := range patterns {
		if strings.Contains(p, "{app_id}") {
			t.Errorf("pattern %q uses {app_id}, the wildcard Dapr adds itself: a duplicate wildcard name panics", p)
		}
		p = normalize(p)
		all = append(all, p)
		if !strings.HasPrefix(p, "/v1.0/invoke/") {
			all = append(all, "/v1.0/invoke/{app_id}/method"+p)
		}
	}
	slices.Sort(all)
	all = slices.Compact(all)
	mux := http.NewServeMux()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Dapr's ServeMux panics on the adhar-dapr patterns — every sidecar would crash at start: %v", r)
			}
		}()
		for _, p := range all {
			mux.HandleFunc(p, func(http.ResponseWriter, *http.Request) {})
		}
	}()
	label := func(path string) string {
		req, _ := http.NewRequest(http.MethodGet, normalize(path), nil)
		_, pattern := mux.Handler(req)
		return pattern
	}
	for path, want := range map[string]string{
		"/api/v1/orders":                              "/api/{version}/{resource}",
		"/api/v1/orders/42":                           "/api/{version}/{resource}/{id}",
		"/api/v2/orders/42/items":                     "/api/{version}/{resource}/{id}/{subresource}",
		"/api/v1/orders/42/items/7":                   "/api/{version}/{rest...}",
		"/v1.0/invoke/orders/method/api/v1/orders/42": "/v1.0/invoke/{app_id}/method/api/{version}/{resource}/{id}",
		"/healthz": "",
	} {
		if got := label(path); got != want {
			t.Errorf("%s is labelled %q, want %q", path, got, want)
		}
	}
}

// What a sidecar in an environment namespace must reach, and what must reach
// it: dapr-state admits the environments' plane, the environment admits the
// platform namespace (Prometheus scrapes daprd there), and nothing restricts
// the environment's egress (dapr-state, Kafka, sentry/placement/scheduler/
// operator and Alloy all live in adhar-system). The Kafka listener the
// component dials is the open, PLAINTEXT one authType none assumes, and Alloy
// serves OTLP gRPC where the Configuration sends traces.
func TestDaprStoresAreReachableFromEnvironmentNamespaces(t *testing.T) {
	objs := daprEnvManifests(daprEnvRender(t, daprEnvXR("prod", nil), daprCluster(true, true)))
	plane, _ := dig(objs["namespace"], "metadata", "labels", "adhar.io/plane").(string)
	np := daprFind(t, daprDocs(t, "manifests/state-store.yaml"), "NetworkPolicy", "dapr-state")
	admitted := false
	for _, r := range dig(np, "spec", "ingress").([]any) {
		rule := r.(map[string]any)
		ports, _ := json.Marshal(rule["ports"])
		if !strings.Contains(string(ports), `"port":6379`) {
			continue
		}
		for _, f := range rule["from"].([]any) {
			exprs, _ := dig(f.(map[string]any), "namespaceSelector", "matchExpressions").([]any)
			for _, e := range exprs {
				m := e.(map[string]any)
				vals, _ := json.Marshal(m["values"])
				if m["key"] == "adhar.io/plane" && m["operator"] == "In" && strings.Contains(string(vals), `"`+plane+`"`) {
					admitted = true
				}
			}
		}
	}
	if plane == "" || !admitted {
		t.Errorf("an environment namespace (adhar.io/plane=%q) cannot reach dapr-state on 6379: its NetworkPolicy does not admit that plane", plane)
	}
	netpol := objs["netpol"]
	if types, _ := json.Marshal(dig(netpol, "spec", "policyTypes")); string(types) != `["Ingress"]` {
		t.Errorf("the environment's NetworkPolicy has policyTypes %s; restricting egress cuts every sidecar off from dapr-state, Kafka, the Dapr control plane and Alloy — admit those first", types)
	}
	if raw, _ := json.Marshal(dig(netpol, "spec", "ingress")); !strings.Contains(string(raw), `"kubernetes.io/metadata.name":"adhar-system"`) {
		t.Error("the environment no longer admits adhar-system: Prometheus cannot scrape daprd")
	}

	var kafka map[string]any
	for _, d := range readYAMLDocs(t, filepath.Join(stackPackagesDir(t), "data/kafka-operator/manifests/kafka-cluster.yaml")) {
		if d["kind"] == "Kafka" && dig(d, "metadata", "name") == "adhar-kafka" {
			kafka = d
		}
	}
	if kafka == nil {
		t.Fatal("no Kafka adhar-kafka in data/kafka-operator")
	}
	plain := false
	for _, l := range dig(kafka, "spec", "kafka", "listeners").([]any) {
		m := l.(map[string]any)
		if m["port"] == float64(9092) {
			plain = m["tls"] == false && m["authentication"] == nil && m["networkPolicyPeers"] == nil
		}
	}
	if !plain {
		t.Error("adhar-kafka's 9092 listener is no longer PLAINTEXT, unauthenticated and open to every namespace: the environments' pubsub (authType none) cannot connect")
	}

	alloy := false
	for _, d := range readYAMLDocs(t, filepath.Join(stackPackagesDir(t), "observability/alloy/manifests/install.yaml")) {
		if d["kind"] == "Service" && dig(d, "metadata", "name") == "alloy" {
			ports, _ := json.Marshal(dig(d, "spec", "ports"))
			alloy = strings.Contains(string(ports), `"name":"otlp-grpc"`) && strings.Contains(string(ports), `"port":4317`)
		}
	}
	if !alloy {
		t.Errorf("Service alloy no longer serves OTLP gRPC on 4317: adhar-dapr sends its traces to %s", daprOTLPEndpoint)
	}
}

// provider-kubernetes is the identity that applies every composed Object; a
// kind it may not write leaves the Object at "forbidden" and the Environment
// unready. It must also HOLD what the Role it creates grants (Kubernetes
// refuses to let a creator grant permissions it does not have).
func TestProviderKubernetesMayComposeTheDaprContract(t *testing.T) {
	docs := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "providers/provider-runtime-rbac.yaml"))
	role := daprFind(t, docs, "ClusterRole", "crossplane-provider-kubernetes-compose")
	held := map[string]map[string]bool{} // group/resource -> verbs
	for _, r := range role["rules"].([]any) {
		m := r.(map[string]any)
		if m["resourceNames"] != nil {
			continue
		}
		for _, g := range m["apiGroups"].([]any) {
			for _, res := range m["resources"].([]any) {
				key := g.(string) + "/" + res.(string)
				if held[key] == nil {
					held[key] = map[string]bool{}
				}
				for _, v := range m["verbs"].([]any) {
					held[key][v.(string)] = true
				}
			}
		}
	}
	plural := map[string]string{"Component": "components", "Configuration": "configurations", "Resiliency": "resiliencies", "Role": "roles"}
	objs := daprEnvManifests(daprEnvRender(t, daprEnvXR("dev", nil), daprCluster(true, true)))
	for _, name := range daprEnvResources {
		m := objs[name]
		group := ""
		if gv := m["apiVersion"].(string); strings.Contains(gv, "/") {
			group = strings.Split(gv, "/")[0]
		}
		key := group + "/" + plural[m["kind"].(string)]
		for _, v := range []string{"get", "create", "update", "patch", "delete"} {
			if !held[key][v] {
				t.Errorf("provider-kubernetes may not %s %s, which the environment composes (%s)", v, key, name)
			}
		}
	}
	for _, r := range objs["dapr-secret-reader"]["rules"].([]any) {
		m := r.(map[string]any)
		for _, res := range m["resources"].([]any) {
			for _, v := range m["verbs"].([]any) {
				if !held["/"+res.(string)][v.(string)] {
					t.Errorf("provider-kubernetes does not hold %s %s, so it may not create dapr-secret-reader, which grants it", v, res)
				}
			}
		}
	}
	binding := daprFind(t, docs, "ClusterRoleBinding", "crossplane-provider-kubernetes-compose")
	if raw, _ := json.Marshal(binding["subjects"]); !strings.Contains(string(raw), `"name":"provider-kubernetes"`) {
		t.Error("crossplane-provider-kubernetes-compose is not bound to the provider-kubernetes ServiceAccount")
	}
}

// The XRDs are structural: a parameter the schema lacks is rejected, and a
// status field it lacks is pruned without a word.
func TestEnvironmentXRDDeclaresTheDaprSwitchAndStatus(t *testing.T) {
	var xrd map[string]any
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, "xrd", "env.xrd.yaml")), &xrd); err != nil {
		t.Fatal(err)
	}
	schema := dig(xrd["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any), "schema", "openAPIV3Schema", "properties").(map[string]any)
	params, _ := dig(schema, "spec", "properties", "parameters", "properties", "dapr", "properties").(map[string]any)
	if params == nil {
		t.Fatal("env.xrd.yaml declares no spec.parameters.dapr")
	}
	if enum, _ := json.Marshal(dig(params, "pubsub", "enum")); string(enum) != `["auto","kafka","redis"]` || dig(params, "pubsub", "default") != "auto" {
		t.Errorf("spec.parameters.dapr.pubsub = enum %s default %v, want [auto kafka redis] default auto", enum, dig(params, "pubsub", "default"))
	}
	if dig(params, "enabled", "default") != true {
		t.Errorf("spec.parameters.dapr.enabled defaults to %v, want true", dig(params, "enabled", "default"))
	}
	status, _ := dig(schema, "status", "properties", "dapr", "properties").(map[string]any)
	for _, c := range []struct{ dapr, kafka bool }{{true, true}, {true, false}, {false, false}} {
		for key := range daprEnvStatus(t, daprEnvRender(t, daprEnvXR("dev", nil), daprCluster(c.dapr, c.kafka))) {
			if _, ok := status[key]; !ok {
				t.Errorf("the composition writes status.dapr.%s, which env.xrd.yaml does not declare (it is pruned)", key)
			}
		}
	}
}

// A Function with a topic trigger names the environment's Configuration and
// pub/sub by default (compositions/function/local-knative.yaml); renaming
// either here breaks every such function's sidecar.
func TestFunctionsNameTheEnvironmentsDaprContract(t *testing.T) {
	fn := readConfigurationFile(t, "compositions", "function", "local-knative.yaml")
	cfg := regexp.MustCompile(`dig "dapr" "config" "([^"]+)"`).FindStringSubmatch(fn)
	ps := regexp.MustCompile(`pubsubname: \{\{ default "([^"]+)" \(get \$t "pubsub"\)`).FindStringSubmatch(fn)
	if cfg == nil || ps == nil {
		t.Fatal("the Function composition no longer defaults dapr.io/config and the Subscription's pubsubname where this test looks")
	}
	objs := daprEnvManifests(daprEnvRender(t, daprEnvXR("dev", nil), daprCluster(true, true)))
	if got := dig(objs["dapr-configuration"], "metadata", "name"); got != cfg[1] {
		t.Errorf("functions default dapr.io/config to %q; the environment's Configuration is %v", cfg[1], got)
	}
	if got := dig(objs["dapr-pubsub"], "metadata", "name"); got != ps[1] {
		t.Errorf("functions default pubsubname to %q; the environment's pub/sub is %v", ps[1], got)
	}
}

// The same composition through the real function containers.
func TestEnvironmentCompositionRendersDaprWithCrossplane(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping `crossplane render`, which starts the function containers")
	}
	bin, err := exec.LookPath("crossplane")
	if err != nil {
		t.Skip("the crossplane CLI is not installed; the in-process render still ran")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available; `crossplane render` runs each function in a container")
	}
	dir := t.TempDir()
	write := func(name string, docs ...map[string]any) string {
		t.Helper()
		var b strings.Builder
		for _, d := range docs {
			y, err := yaml.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			b.WriteString("---\n")
			b.Write(y)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	xr := write("xr.yaml", daprEnvXR("prod", nil))
	// The same function packages the platform installs
	// (TestFunctionRenderFixturesTrackTheInstalledFunctions keeps them in step).
	functions := filepath.Join("testdata", "function", "functions.yaml")
	render := func(cluster []map[string]any) ([]string, map[string]any) {
		t.Helper()
		required := write(fmt.Sprintf("required-%d.yaml", len(cluster)), append(cluster,
			map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "unrelated", "namespace": "adhar-system"}})...)
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(bin, "render", xr, filepath.Join(controlPlaneDir(t), daprEnvComposition), functions, "--required-resources="+required)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("crossplane render: %v\n%s", err, stderr.String())
		}
		var names []string
		var status map[string]any
		for _, d := range strings.Split(stdout.String(), "\n---") {
			var m map[string]any
			if yaml.Unmarshal([]byte(d), &m) != nil || len(m) == 0 {
				continue
			}
			if n, _ := dig(m, "metadata", "annotations", "crossplane.io/composition-resource-name").(string); strings.HasPrefix(n, "dapr-") {
				names = append(names, n)
			}
			if m["kind"] == "Environment" {
				status, _ = dig(m, "status", "dapr").(map[string]any)
			}
		}
		sort.Strings(names)
		return names, status
	}
	names, status := render(daprCluster(true, false))
	if strings.Join(names, ",") != strings.Join(daprEnvResources, ",") || status["pubsub"] != "redis" {
		t.Errorf("with Dapr and no Kafka the real functions compose %v, status %v", names, status)
	}
	names, status = render(nil)
	if len(names) != 0 || status["enabled"] != false {
		t.Errorf("without Dapr the real functions compose %v, status %v", names, status)
	}
}
