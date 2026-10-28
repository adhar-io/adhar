package adharplatform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"text/template"

	sprig "github.com/go-task/slim-sprig/v3"
	"sigs.k8s.io/yaml"
)

// The Function composition (compositions/function/local-knative.yaml) turns a
// Function into a Knative Service, the edge in front of it and its event
// sources. Until 2026-10-27 it composed the Service alone, called the Function
// Ready the moment Knative accepted it, never wrote status.url, and ignored
// trigger and eventSource entirely.
//
// These tests render the composition the way function-go-templating does —
// in-process with the same template language, and through `crossplane render`
// when Docker is available — against testdata/function, which holds the same
// fixtures a person hands to `crossplane render` (see its README.md).

const (
	fnComposition   = "compositions/function/local-knative.yaml"
	fnFixtureDir    = "testdata/function"
	fnPlatformCM    = "https://argocd.cloud.example.com"
	fnOTLPEndpoint  = "http://alloy.adhar-system.svc.cluster.local:4318"
	fnKnativeNS     = "knative-serving"
	fnServiceObject = "knative-service"
)

// fnFixture reads one YAML document set from testdata/function.
func fnFixture(t *testing.T, name string) []map[string]any {
	t.Helper()
	return readYAMLDocs(t, filepath.Join(fnFixtureDir, name))
}

func fnXR(t *testing.T, name string) map[string]any {
	t.Helper()
	docs := fnFixture(t, name)
	if len(docs) != 1 || docs[0]["kind"] != "Function" {
		t.Fatalf("%s must hold exactly one Function", name)
	}
	return docs[0]
}

// fnCluster is what the composition may read from the cluster: the fixture
// namespaces and argocd-cm, with the platform URL replaced when url is not "".
func fnCluster(t *testing.T, url string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, d := range fnFixture(t, "required-resources.yaml") {
		if d["kind"] == "ConfigMap" {
			if url == "-" {
				continue
			}
			if url != "" {
				d["data"] = map[string]any{"url": url}
			}
		}
		out = append(out, d)
	}
	return out
}

// fnObserved keys observed composed resources by composition resource name,
// the way Crossplane hands them to a function.
func fnObserved(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, d := range docs {
		name, _ := dig(d, "metadata", "annotations", "crossplane.io/composition-resource-name").(string)
		if name == "" {
			t.Fatalf("observed resource %v has no crossplane.io/composition-resource-name", dig(d, "metadata", "name"))
		}
		out[name] = map[string]any{"resource": d}
	}
	return out
}

// fnRender runs the composition's go-templating step twice, as Crossplane
// does: once to learn the resources it requires, then again with them.
func fnRender(t *testing.T, xr map[string]any, cluster []map[string]any, observed map[string]any) ([]map[string]any, error) {
	t.Helper()
	req := map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": xr}, "resources": observed}}
	docs, err := fnExecute(t, req)
	if err != nil {
		return nil, err
	}
	required := map[string]any{}
	for _, d := range docs {
		if d["kind"] != "ExtraResources" {
			continue
		}
		reqs, _ := d["requirements"].(map[string]any)
		for key, raw := range reqs {
			sel, _ := raw.(map[string]any)
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
	if len(required) == 0 {
		t.Fatal("the composition requires no resources; it cannot learn its namespace or the platform domain")
	}
	req["requiredResources"] = required
	return fnExecute(t, req)
}

func fnExecute(t *testing.T, req map[string]any) ([]map[string]any, error) {
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
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, fnComposition)), &comp); err != nil {
		t.Fatalf("%s does not parse: %v", fnComposition, err)
	}
	src := ""
	for _, s := range comp.Spec.Pipeline {
		if s.FunctionRef.Name == "function-go-templating" {
			src = s.Input.Inline.Template
		}
	}
	if src == "" {
		t.Fatalf("%s has no function-go-templating step", fnComposition)
	}
	funcs := template.FuncMap{
		// function-go-templating's own helper; Crossplane v2 hands required
		// resources over as requiredResources.
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
	tpl, err := template.New("manifests").Funcs(sprig.TxtFuncMap()).Funcs(funcs).Parse(src)
	if err != nil {
		t.Fatalf("the template does not parse: %v", err)
	}
	// The function hands the template protojson, so numbers are float64.
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
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

func fnMustRender(t *testing.T, xr map[string]any, cluster []map[string]any, observed map[string]any) []map[string]any {
	t.Helper()
	docs, err := fnRender(t, xr, cluster, observed)
	if err != nil {
		t.Fatalf("the composition refused %s: %v", dig(xr, "metadata", "name"), err)
	}
	return docs
}

// fnObject is the composed Object with the given composition resource name.
func fnObject(docs []map[string]any, resource string) map[string]any {
	for _, d := range docs {
		if d["kind"] == "Object" && dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name") == resource {
			return d
		}
	}
	return nil
}

func fnManifest(t *testing.T, docs []map[string]any, resource string) map[string]any {
	t.Helper()
	o := fnObject(docs, resource)
	if o == nil {
		t.Fatalf("the composition did not compose %q", resource)
	}
	m, _ := dig(o, "spec", "forProvider", "manifest").(map[string]any)
	return m
}

func fnStatus(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == "Function" {
			s, _ := d["status"].(map[string]any)
			return s
		}
	}
	t.Fatal("the composition writes no status to the Function")
	return nil
}

func fnCondition(t *testing.T, docs []map[string]any, typ string) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] != "ClaimConditions" {
			continue
		}
		conds, _ := d["conditions"].([]any)
		for _, c := range conds {
			if m, _ := c.(map[string]any); m["type"] == typ {
				return m
			}
		}
	}
	t.Fatalf("the composition reports no %s condition", typ)
	return nil
}

func fnEnv(t *testing.T, ksvc map[string]any) map[string]string {
	t.Helper()
	cs, _ := dig(ksvc, "spec", "template", "spec", "containers").([]any)
	if len(cs) != 1 {
		t.Fatalf("the Knative Service has %d containers, want 1", len(cs))
	}
	out := map[string]string{}
	c, _ := cs[0].(map[string]any)
	env, _ := c["env"].([]any)
	for _, e := range env {
		m, _ := e.(map[string]any)
		if _, dup := out[m["name"].(string)]; dup {
			t.Errorf("env %v is set twice", m["name"])
		}
		out[m["name"].(string)] = fmt.Sprint(m["value"])
	}
	return out
}

func fnContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func fnWithParams(xr map[string]any, set map[string]any) map[string]any {
	raw, _ := json.Marshal(xr)
	var cp map[string]any
	_ = json.Unmarshal(raw, &cp)
	p := dig(cp, "spec", "parameters").(map[string]any)
	for k, v := range set {
		if v == nil {
			delete(p, k)
			continue
		}
		p[k] = v
	}
	return cp
}

func fnWithNamespace(xr map[string]any, ns string) map[string]any {
	raw, _ := json.Marshal(xr)
	var cp map[string]any
	_ = json.Unmarshal(raw, &cp)
	cp["metadata"].(map[string]any)["namespace"] = ns
	return cp
}

// The Knative Service carries the platform's identity on the Service AND on
// the revision template (pods are what dashboards and policies select), keeps
// the soft autoscaling target apart from the hard concurrency cap, and hands
// the function the environment it is documented to read.
func TestFunctionComposesTheKnativeServiceContract(t *testing.T) {
	docs := fnMustRender(t, fnXR(t, "xr-http.yaml"), fnCluster(t, ""), nil)
	ksvc := fnManifest(t, docs, fnServiceObject)

	if o := fnObject(docs, fnServiceObject); dig(o, "metadata", "name") != "orders-knative-service" {
		t.Errorf("the Knative Service's Object is %v; renaming it (or its resource name) replaces the Service and every revision", dig(o, "metadata", "name"))
	}
	if p := dig(fnObject(docs, fnServiceObject), "spec", "readiness", "policy"); p != "DeriveFromObject" {
		t.Errorf("the Knative Service Object's readiness is %v; anything but DeriveFromObject calls the Function Ready the moment Knative accepts it", p)
	}
	want := map[string]string{
		"adhar.io/organisation": "adhar", "adhar.io/team": "platform", "adhar.io/project": "shop",
		"adhar.io/environment": "dev", "adhar.io/application": "orders", "adhar.io/plane": "workload",
		"platform.adhar.io/function": "orders", "platform.adhar.io/runtime": "node22",
		"app.kubernetes.io/name": "orders",
	}
	for _, where := range [][]string{{"metadata", "labels"}, {"spec", "template", "metadata", "labels"}} {
		labels, _ := dig(ksvc, where...).(map[string]any)
		for k, v := range want {
			if labels[k] != v {
				t.Errorf("%s[%s] = %v, want %s", strings.Join(where, "."), k, labels[k], v)
			}
		}
	}
	if dig(ksvc, "metadata", "labels", "networking.knative.dev/visibility") != "cluster-local" {
		t.Error("the Knative Service is not cluster-local: Knative would publish a second, ungated name for it beside the Gateway's")
	}

	ann, _ := dig(ksvc, "spec", "template", "metadata", "annotations").(map[string]any)
	cc := dig(ksvc, "spec", "template", "spec", "containerConcurrency")
	if fmt.Sprint(cc) != "0" {
		t.Errorf("containerConcurrency = %v, want 0 (unlimited) when the Function sets none", cc)
	}
	if ann["autoscaling.knative.dev/target"] != "100" {
		t.Errorf("autoscaling target = %v, want the targetConcurrency 100", ann["autoscaling.knative.dev/target"])
	}
	if fmt.Sprint(cc) == fmt.Sprint(ann["autoscaling.knative.dev/target"]) {
		t.Error("containerConcurrency equals the autoscaling target: every pod refuses work exactly where the autoscaler aims")
	}
	if ann["autoscaling.knative.dev/min-scale"] != "0" || ann["autoscaling.knative.dev/max-scale"] != "20" {
		t.Errorf("scale bounds = %v..%v, want 0..20", ann["autoscaling.knative.dev/min-scale"], ann["autoscaling.knative.dev/max-scale"])
	}
	if _, dapr := ann["dapr.io/enabled"]; dapr {
		t.Error("Dapr is injected into a function that neither asked for it nor has a topic trigger")
	}
	if v := dig(ksvc, "spec", "template", "spec", "timeoutSeconds"); fmt.Sprint(v) != "30" {
		t.Errorf("timeoutSeconds = %v, want 30", v)
	}

	env := fnEnv(t, ksvc)
	for k, v := range map[string]string{
		"FUNCTION_NAME": "orders", "FUNCTION_RUNTIME": "node22", "FUNCTION_TRIGGER": "http",
		"ADHAR_ENVIRONMENT": "dev", "APP_ID": "orders",
		"OTEL_EXPORTER_OTLP_ENDPOINT": fnOTLPEndpoint, "OTEL_SERVICE_NAME": "orders",
	} {
		if env[k] != v {
			t.Errorf("env %s = %q, want %q", k, env[k], v)
		}
	}
	// Every environment namespace gets harbor-dockercfg (adhar-supply-chain's
	// pull-secret sync) and the kubelet needs it to pull from the platform's
	// Harbor; the scaffolded Rollouts name it the same way.
	secrets, _ := dig(ksvc, "spec", "template", "spec", "imagePullSecrets").([]any)
	if len(secrets) != 1 || dig(secrets[0].(map[string]any), "name") != "harbor-dockercfg" {
		t.Errorf("imagePullSecrets = %v, want harbor-dockercfg", secrets)
	}
}

// A JVM function must start inside the 512Mi an environment's LimitRange gives
// a container that names no limit. Paketo's memory calculator reserves its
// fixed regions before any heap — direct memory, metaspace (~55M for a small
// Quarkus or Spring app), the code cache and a stack per expected thread — and
// at its defaults (250 threads, a 240M code cache) that was 567M: every
// revision of a Java function exited with "unable to calculate memory
// configuration" and its URL answered 404 (Azure, 2026-10-28).
func TestJavaFunctionsFitTheEnvironmentsDefaultMemoryLimit(t *testing.T) {
	const limitMi, directMi, metaspaceMi, minHeapMi = 512, 10, 55, 256
	for _, runtime := range []string{"java21", "java25"} {
		env := fnEnv(t, fnManifest(t, fnMustRender(t, fnWithParams(fnXR(t, "xr-http.yaml"), map[string]any{"runtime": runtime}), fnCluster(t, ""), nil), fnServiceObject))
		threads, err := strconv.Atoi(env["BPL_JVM_THREAD_COUNT"])
		if err != nil {
			t.Errorf("%s: BPL_JVM_THREAD_COUNT = %q; the calculator then reserves stack for 250 threads", runtime, env["BPL_JVM_THREAD_COUNT"])
			continue
		}
		m := regexp.MustCompile(`-XX:ReservedCodeCacheSize=(\d+)M`).FindStringSubmatch(env["JAVA_TOOL_OPTIONS"])
		if m == nil {
			t.Errorf("%s: JAVA_TOOL_OPTIONS = %q names no code cache; the calculator reserves 240M", runtime, env["JAVA_TOOL_OPTIONS"])
			continue
		}
		codeCache, _ := strconv.Atoi(m[1])
		if fixed := directMi + metaspaceMi + codeCache + threads; limitMi-fixed < minHeapMi {
			t.Errorf("%s: %dM of fixed JVM regions leaves %dM of heap in %dMi; a function needs at least %dM",
				runtime, fixed, limitMi-fixed, limitMi, minHeapMi)
		}
	}
	// Not a JVM: nothing to tune.
	if env := fnEnv(t, fnManifest(t, fnMustRender(t, fnXR(t, "xr-http.yaml"), fnCluster(t, ""), nil), fnServiceObject)); env["BPL_JVM_THREAD_COUNT"] != "" || env["JAVA_TOOL_OPTIONS"] != "" {
		t.Errorf("a node22 function was given JVM settings: %q / %q", env["BPL_JVM_THREAD_COUNT"], env["JAVA_TOOL_OPTIONS"])
	}
	// The function's own env still wins.
	xr := fnWithParams(fnXR(t, "xr-http.yaml"), map[string]any{"runtime": "java25", "env": map[string]any{"BPL_JVM_THREAD_COUNT": "120"}})
	if got := fnEnv(t, fnManifest(t, fnMustRender(t, xr, fnCluster(t, ""), nil), fnServiceObject))["BPL_JVM_THREAD_COUNT"]; got != "120" {
		t.Errorf("BPL_JVM_THREAD_COUNT = %q, want the function's own 120", got)
	}
}

// Nothing reaches a function except through the platform Gateway: an
// HTTPRoute on adhar-gateway sends its host to Kourier, a ReferenceGrant in
// knative-serving admits that cross-namespace backend, and a DomainMapping
// (with its ClusterDomainClaim) gives Kourier the host.
func TestFunctionIsPublishedThroughThePlatformGateway(t *testing.T) {
	docs := fnMustRender(t, fnXR(t, "xr-http.yaml"), fnCluster(t, ""), nil)
	const host = "orders.dev.cloud.example.com"

	route := fnManifest(t, docs, "route")
	parents, _ := dig(route, "spec", "parentRefs").([]any)
	if len(parents) != 1 || dig(parents[0].(map[string]any), "name") != "adhar-gateway" || dig(parents[0].(map[string]any), "namespace") != "adhar-system" {
		t.Errorf("the HTTPRoute attaches to %v, want adhar-system/adhar-gateway", parents)
	}
	if hs, _ := dig(route, "spec", "hostnames").([]any); len(hs) != 1 || hs[0] != host {
		t.Errorf("the HTTPRoute serves %v, want [%s]", hs, host)
	}
	rules, _ := dig(route, "spec", "rules").([]any)
	backends, _ := dig(rules[0].(map[string]any), "backendRefs").([]any)
	b, _ := backends[0].(map[string]any)
	if b["name"] != "kourier" || b["namespace"] != fnKnativeNS || fmt.Sprint(b["port"]) != "80" {
		t.Errorf("the HTTPRoute's backend is %v, want Service kourier in %s on port 80", b, fnKnativeNS)
	}
	if q, _ := dig(fnObject(docs, "route"), "spec", "readiness", "celQuery").(string); !strings.Contains(q, "ResolvedRefs") {
		t.Error("the route is Ready on Accepted alone; a backend the ReferenceGrant does not admit is still Accepted, and answers 500")
	}

	grant := fnManifest(t, docs, "route-grant")
	if dig(grant, "metadata", "namespace") != fnKnativeNS {
		t.Errorf("the ReferenceGrant is in %v; it must be in %s, beside the Service it admits", dig(grant, "metadata", "namespace"), fnKnativeNS)
	}
	from, _ := dig(grant, "spec", "from").([]any)
	to, _ := dig(grant, "spec", "to").([]any)
	if len(from) != 1 || dig(from[0].(map[string]any), "kind") != "HTTPRoute" || dig(from[0].(map[string]any), "namespace") != "dev" ||
		len(to) != 1 || dig(to[0].(map[string]any), "name") != "kourier" {
		t.Errorf("the ReferenceGrant admits %v -> %v, want HTTPRoutes in dev -> Service kourier", from, to)
	}

	dm := fnManifest(t, docs, "domain-mapping")
	if dig(dm, "metadata", "name") != host || dig(dm, "metadata", "namespace") != "dev" || dig(dm, "spec", "ref", "name") != "orders" {
		t.Errorf("the DomainMapping maps %v/%v -> %v, want dev/%s -> orders", dig(dm, "metadata", "namespace"), dig(dm, "metadata", "name"), dig(dm, "spec", "ref", "name"), host)
	}
	claim := fnObject(docs, "domain-claim")
	if dig(claim, "spec", "forProvider", "manifest", "spec", "namespace") != "dev" {
		t.Error("the ClusterDomainClaim does not claim the host for the function's namespace")
	}
	// Create-only: a host another namespace holds is never taken over, and a
	// deleted function's claim stays with its namespace (Knative's own
	// autocreate behaves the same way).
	if pol := fmt.Sprint(dig(claim, "spec", "managementPolicies")); pol != "[Observe Create]" {
		t.Errorf("the ClusterDomainClaim's managementPolicies are %s, want [Observe Create]", pol)
	}
	if q, _ := dig(claim, "spec", "readiness", "celQuery").(string); !strings.Contains(q, "'dev'") {
		t.Errorf("the claim's readiness %q does not check that the claim is THIS namespace's", q)
	}

	if url := fnStatus(t, docs)["url"]; url != "https://"+host {
		t.Errorf("status.url = %v, want https://%s", url, host)
	}
}

// The host follows the platform route rule the scaffolder uses
// (cmd/apps/scaffold_hosts.go, platformRouteHost): <name>.<env>.<domain>, or
// <name>-<env>.<domain> when the domain's first label is the environment,
// because `orders.test.test.adhar.io` repeats a label and the Gateway has no
// listener for it.
func TestFunctionHostFollowsThePlatformRouteRule(t *testing.T) {
	xr := fnXR(t, "xr-http.yaml")
	for _, c := range []struct {
		name, platformURL, namespace string
		params                       map[string]any
		wantHost, wantURL            string
	}{
		{"cloud", "https://argocd.cloud.example.com", "dev", nil, "orders.dev.cloud.example.com", "https://orders.dev.cloud.example.com"},
		{"local keeps the port in the URL only", "https://argocd.adhar.localtest.me:8443", "dev", nil, "orders.dev.adhar.localtest.me", "https://orders.dev.adhar.localtest.me:8443"},
		{"environment repeats the domain's first label", "https://argocd.dev.example.io", "dev", nil, "orders-dev.dev.example.io", "https://orders-dev.dev.example.io"},
		{"outside an environment namespace", "https://argocd.cloud.example.com", "team-media", map[string]any{"environment": "test"}, "orders.test.cloud.example.com", "https://orders.test.cloud.example.com"},
		{"explicit hostname", "https://argocd.adhar.localtest.me:8443", "dev", map[string]any{"hostname": "api.example.org"}, "api.example.org", "https://api.example.org"},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := fnWithParams(fnWithNamespace(xr, c.namespace), c.params)
			docs := fnMustRender(t, x, fnCluster(t, c.platformURL), nil)
			hs, _ := dig(fnManifest(t, docs, "route"), "spec", "hostnames").([]any)
			if len(hs) != 1 || hs[0] != c.wantHost {
				t.Errorf("hostnames = %v, want [%s] (a Gateway API hostname carries no port)", hs, c.wantHost)
			}
			if got := fnStatus(t, docs)["url"]; got != c.wantURL {
				t.Errorf("status.url = %v, want %s", got, c.wantURL)
			}
		})
	}

	t.Run("no platform domain yet", func(t *testing.T) {
		docs := fnMustRender(t, xr, fnCluster(t, "-"), nil)
		if fnObject(docs, "route") != nil || fnObject(docs, "domain-mapping") != nil {
			t.Error("an edge was composed for a host nobody could know")
		}
		if c := fnCondition(t, docs, "EdgeReady"); c["status"] != "False" || c["reason"] != "DomainUnknown" {
			t.Errorf("EdgeReady = %v/%v, want False/DomainUnknown", c["status"], c["reason"])
		}
		if url := fnStatus(t, docs)["url"]; url != "http://orders.dev.svc.cluster.local" {
			t.Errorf("status.url = %v, want the in-cluster address while there is no public one", url)
		}
	})

	t.Run("visibility cluster", func(t *testing.T) {
		docs := fnMustRender(t, fnWithParams(xr, map[string]any{"visibility": "cluster"}), fnCluster(t, ""), nil)
		for _, r := range []string{"route", "route-grant", "domain-mapping", "domain-claim"} {
			if fnObject(docs, r) != nil {
				t.Errorf("a cluster-only function composed its %s", r)
			}
		}
		if c := fnCondition(t, docs, "EdgeReady"); c["status"] != "True" || c["reason"] != "ClusterLocal" {
			t.Errorf("EdgeReady = %v/%v, want True/ClusterLocal", c["status"], c["reason"])
		}
	})
}

// Each trigger type composes its own event source; a topic trigger turns Dapr
// on and keeps one pod warm, because Dapr delivers from a sidecar that a
// function scaled to zero does not have; a bucket trigger is accepted and
// reported, not silently dropped.
func TestFunctionTriggersComposeTheirEventSources(t *testing.T) {
	docs := fnMustRender(t, fnXR(t, "xr-events.yaml"), fnCluster(t, ""), nil)

	ping := fnManifest(t, docs, "trigger-nightly")
	if ping["kind"] != "PingSource" || dig(ping, "spec", "schedule") != "0 3 * * *" || dig(ping, "spec", "timezone") != "Europe/Berlin" ||
		dig(ping, "spec", "data") != `{"sweep": true}` || dig(ping, "spec", "contentType") != "application/json" ||
		dig(ping, "spec", "sink", "ref", "name") != "thumbnails" {
		t.Errorf("the cron trigger composed %v", ping)
	}

	trig := fnManifest(t, docs, "trigger-cloudevent-1")
	if trig["kind"] != "Trigger" || dig(trig, "spec", "broker") != "default" ||
		dig(trig, "spec", "filter", "attributes", "type") != "com.acme.media.uploaded" ||
		dig(trig, "spec", "subscriber", "ref", "name") != "thumbnails" || dig(trig, "spec", "subscriber", "uri") != "/events" {
		t.Errorf("the cloudevent trigger composed %v", trig)
	}
	broker := fnObject(docs, "broker-default")
	if broker == nil || dig(broker, "spec", "forProvider", "manifest", "kind") != "Broker" {
		t.Fatal("no Broker was composed for the cloudevent trigger")
	}
	if pol := fmt.Sprint(dig(broker, "spec", "managementPolicies")); pol != "[Observe Create]" {
		t.Errorf("the Broker's managementPolicies are %s: it is the namespace's, shared by other functions' triggers, so it is created when absent and never updated or deleted", pol)
	}

	sub := fnManifest(t, docs, "trigger-topic-2")
	if sub["apiVersion"] != "dapr.io/v2alpha1" || sub["kind"] != "Subscription" ||
		dig(sub, "spec", "pubsubname") != "kafka-pubsub" || dig(sub, "spec", "topic") != "media.resized" ||
		dig(sub, "spec", "routes", "default") != "/" || dig(sub, "spec", "deadLetterTopic") != "media.dead" {
		t.Errorf("the topic trigger composed %v", sub)
	}
	if scopes, _ := sub["scopes"].([]any); len(scopes) != 1 || scopes[0] != "thumbnails" {
		t.Errorf("the Subscription's scopes are %v; unscoped, every Dapr app in the namespace receives the topic", sub["scopes"])
	}

	ksvc := fnManifest(t, docs, fnServiceObject)
	ann, _ := dig(ksvc, "spec", "template", "metadata", "annotations").(map[string]any)
	for k, v := range map[string]string{
		"dapr.io/enabled": "true", "dapr.io/app-id": "thumbnails", "dapr.io/app-port": "8001",
		"dapr.io/config": "adhar-dapr", "dapr.io/enable-metrics": "true", "dapr.io/log-as-json": "true",
		"autoscaling.knative.dev/min-scale": "1",
	} {
		if ann[k] != v {
			t.Errorf("annotation %s = %v, want %s", k, ann[k], v)
		}
	}
	// Knative's queue-proxy serves autoscaling metrics on 9090, Dapr's default
	// metrics port, in the same pod network namespace.
	if mp := ann["dapr.io/metrics-port"]; mp == nil || mp == "9090" {
		t.Errorf("dapr.io/metrics-port = %v; the queue-proxy already holds 9090", mp)
	}
	if cc := dig(ksvc, "spec", "template", "spec", "containerConcurrency"); fmt.Sprint(cc) != "4" || ann["autoscaling.knative.dev/target"] != "10" {
		t.Errorf("containerConcurrency/target = %v/%v, want 4/10", cc, ann["autoscaling.knative.dev/target"])
	}
	env := fnEnv(t, ksvc)
	if env["FUNCTION_TRIGGER"] != "cron,cloudevent,topic,bucket" {
		t.Errorf("FUNCTION_TRIGGER = %q", env["FUNCTION_TRIGGER"])
	}
	if env["OTEL_SERVICE_NAME"] != "media-thumbnails" || env["LOG_LEVEL"] != "debug" {
		t.Errorf("the caller's env did not override the platform's: %v", env)
	}
	if _, set := dig(ksvc, "spec", "template", "spec").(map[string]any)["imagePullSecrets"]; set {
		t.Error("imagePullSecrets: [] still names a pull secret")
	}

	// bucket: nothing composed, said out loud, and the Function still Synced.
	for _, d := range docs {
		if r := dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name"); r == "trigger-bucket-3" {
			t.Error("something was composed for a bucket trigger, which is not implemented")
		}
	}
	if c := fnCondition(t, docs, "TriggersReady"); c["status"] != "False" || c["reason"] != "Unsupported" {
		t.Errorf("TriggersReady = %v/%v, want False/Unsupported", c["status"], c["reason"])
	}
	st := fnStatus(t, docs)
	if !strings.Contains(fmt.Sprint(st["message"]), "bucket triggers are not implemented yet") {
		t.Errorf("status.message does not say the bucket trigger is unsupported: %v", st["message"])
	}
	triggers, _ := st["triggers"].([]any)
	if len(triggers) != 4 || dig(triggers[3].(map[string]any), "ready") != "Unsupported" {
		t.Errorf("status.triggers = %v", triggers)
	}
	if fnObject(docs, "ingress-policy") != nil {
		t.Error("a NetworkPolicy was composed into a namespace that denies nothing; it would isolate the function's pods")
	}
}

// The first API carried one trigger as `trigger` + `eventSource`; the
// templates and the console still write it, so it keeps working.
func TestFunctionKeepsTheOneTriggerShorthand(t *testing.T) {
	base := fnXR(t, "xr-legacy.yaml")
	for _, c := range []struct {
		trigger, source, resource string
		want                      map[string]any // manifest paths "a.b" -> value
	}{
		{"topic", "topic://pubsub/billing.v1", "trigger-topic", map[string]any{"spec.pubsubname": "pubsub", "spec.topic": "billing.v1"}},
		{"topic", "kafka://orders", "trigger-topic", map[string]any{"spec.pubsubname": "pubsub", "spec.topic": "orders"}},
		{"cron", "cron://*/5 * * * *", "trigger-cron", map[string]any{"spec.schedule": "*/5 * * * *"}},
		{"cron", "@hourly", "trigger-cron", map[string]any{"spec.schedule": "@hourly"}},
		{"cloudevent", "type=com.acme.order.created, source=shop", "trigger-cloudevent", map[string]any{"spec.filter.attributes.type": "com.acme.order.created", "spec.filter.attributes.source": "shop"}},
		{"cloudevent", "com.acme.order.paid", "trigger-cloudevent", map[string]any{"spec.filter.attributes.type": "com.acme.order.paid", "spec.broker": "default"}},
	} {
		t.Run(c.trigger+" "+c.source, func(t *testing.T) {
			docs := fnMustRender(t, fnWithParams(base, map[string]any{"trigger": c.trigger, "eventSource": c.source}), fnCluster(t, ""), nil)
			m := fnManifest(t, docs, c.resource)
			for path, v := range c.want {
				if got := dig(m, strings.Split(path, ".")...); got != v {
					t.Errorf("%s = %v, want %v", path, got, v)
				}
			}
		})
	}
	t.Run("bucket", func(t *testing.T) {
		docs := fnMustRender(t, fnWithParams(base, map[string]any{"trigger": "bucket", "eventSource": "bucket://media"}), fnCluster(t, ""), nil)
		if c := fnCondition(t, docs, "TriggersReady"); c["reason"] != "Unsupported" {
			t.Errorf("TriggersReady reason = %v, want Unsupported", c["reason"])
		}
	})
	t.Run("a triggers list wins", func(t *testing.T) {
		docs := fnMustRender(t, fnWithParams(base, map[string]any{"triggers": []any{map[string]any{"type": "cron", "schedule": "@daily"}}}), fnCluster(t, ""), nil)
		if fnObject(docs, "trigger-topic") != nil {
			t.Error("the shorthand was composed although triggers is set")
		}
		if dig(fnManifest(t, docs, "trigger-cron-0"), "spec", "schedule") != "@daily" {
			t.Error("the declared cron trigger was not composed as trigger-cron-0")
		}
	})
}

// Status is what the cluster reports, not what was asked for: the revision
// Knative is serving, where the edge stands, and why not when it is not.
func TestFunctionStatusComesFromWhatTheClusterReports(t *testing.T) {
	xr := fnXR(t, "xr-http.yaml")
	observedDocs := fnFixture(t, "observed-orders.yaml")

	docs := fnMustRender(t, xr, fnCluster(t, ""), fnObserved(t, observedDocs))
	st := fnStatus(t, docs)
	if st["readyRevision"] != "orders-00002" || st["latestRevision"] != "orders-00002" {
		t.Errorf("revisions = %v/%v, want orders-00002", st["readyRevision"], st["latestRevision"])
	}
	if st["address"] != "http://orders.dev.svc.cluster.local" {
		t.Errorf("status.address = %v", st["address"])
	}
	if st["message"] != "serving orders-00002 at https://orders.dev.cloud.example.com" {
		t.Errorf("status.message = %v", st["message"])
	}
	if c := fnCondition(t, docs, "ServiceReady"); c["status"] != "True" {
		t.Errorf("ServiceReady = %v", c)
	}
	if c := fnCondition(t, docs, "EdgeReady"); c["status"] != "True" || c["reason"] != "Published" {
		t.Errorf("EdgeReady = %v", c)
	}

	// mutate edits the observed fixture and re-renders.
	mutate := func(edit func(by map[string]map[string]any)) []map[string]any {
		raw, _ := json.Marshal(observedDocs)
		var cp []map[string]any
		_ = json.Unmarshal(raw, &cp)
		by := map[string]map[string]any{}
		for _, d := range cp {
			by[dig(d, "metadata", "annotations", "crossplane.io/composition-resource-name").(string)] = d
		}
		edit(by)
		return fnMustRender(t, xr, fnCluster(t, ""), fnObserved(t, cp))
	}

	docs = mutate(func(by map[string]map[string]any) {
		dig(by["domain-claim"], "status", "atProvider", "manifest", "spec").(map[string]any)["namespace"] = "prod"
	})
	if c := fnCondition(t, docs, "EdgeReady"); c["status"] != "False" || c["reason"] != "HostClaimed" || !strings.Contains(fmt.Sprint(c["message"]), "prod") {
		t.Errorf("a host claimed by another namespace reads as %v", c)
	}

	docs = mutate(func(by map[string]map[string]any) {
		parents := dig(by["route"], "status", "atProvider", "manifest", "status", "parents").([]any)
		conds := parents[0].(map[string]any)["conditions"].([]any)
		conds[1] = map[string]any{"type": "ResolvedRefs", "status": "False", "reason": "RefNotPermitted", "message": "backend not permitted"}
	})
	if c := fnCondition(t, docs, "EdgeReady"); c["status"] != "False" || c["reason"] != "RouteNotReady" {
		t.Errorf("an unresolved route reads as %v", c)
	}

	docs = mutate(func(by map[string]map[string]any) {
		status := dig(by[fnServiceObject], "status", "atProvider", "manifest", "status").(map[string]any)
		status["latestCreatedRevisionName"] = "orders-00003"
		status["conditions"] = []any{map[string]any{"type": "Ready", "status": "False", "reason": "RevisionFailed", "message": `Revision "orders-00003" failed with message: Unable to fetch image`}}
	})
	if c := fnCondition(t, docs, "ServiceReady"); c["status"] != "False" || c["reason"] != "RevisionFailed" {
		t.Errorf("a failed revision reads as %v", c)
	}
	if st := fnStatus(t, docs); !strings.Contains(fmt.Sprint(st["message"]), "Unable to fetch image") || st["latestRevision"] != "orders-00003" {
		t.Errorf("status after a failed revision = %v", st)
	}
}

// An environment namespace IS the environment: its labels decide where a
// Function runs, and a parameter that disagrees is reported, not obeyed.
// Outside one, the parameters apply, then the platform defaults.
func TestFunctionPlacementFollowsTheEnvironmentNamespace(t *testing.T) {
	xr := fnWithParams(fnXR(t, "xr-http.yaml"), map[string]any{"environment": "prod", "team": "payments"})
	docs := fnMustRender(t, xr, fnCluster(t, ""), nil)
	labels, _ := dig(fnManifest(t, docs, fnServiceObject), "metadata", "labels").(map[string]any)
	if labels["adhar.io/environment"] != "dev" || labels["adhar.io/team"] != "platform" {
		t.Errorf("a Function in the dev namespace was labelled %v/%v", labels["adhar.io/environment"], labels["adhar.io/team"])
	}
	c := fnCondition(t, docs, "Placement")
	if c["reason"] != "ParameterOverridden" || !strings.Contains(fmt.Sprint(c["message"]), `environment "prod"`) {
		t.Errorf("Placement = %v; an overridden parameter must be reported", c)
	}

	bare := fnWithParams(fnWithNamespace(fnXR(t, "xr-http.yaml"), "team-media"), map[string]any{"project": nil})
	docs = fnMustRender(t, bare, fnCluster(t, ""), nil)
	labels, _ = dig(fnManifest(t, docs, fnServiceObject), "metadata", "labels").(map[string]any)
	for k, v := range map[string]string{"adhar.io/organisation": "adhar", "adhar.io/team": "platform", "adhar.io/environment": "dev"} {
		if labels[k] != v {
			t.Errorf("outside an environment namespace %s = %v, want the default %s", k, labels[k], v)
		}
	}
	if _, set := labels["adhar.io/project"]; set {
		t.Error("a Function with no project was labelled with one")
	}
}

// Anything Knative's admission webhook would refuse is refused here, with a
// message that names the fix, instead of a Function that is Synced and never
// gets a Service.
func TestFunctionRefusesWhatKnativeWouldReject(t *testing.T) {
	xr := fnXR(t, "xr-http.yaml")
	for _, c := range []struct {
		name string
		xr   map[string]any
		want string
	}{
		{"reserved env", fnWithParams(xr, map[string]any{"env": map[string]any{"PORT": "9000"}}), "parameters.port"},
		{"name that is not a DNS-1035 label", func() map[string]any {
			x := fnWithParams(xr, nil)
			x["metadata"].(map[string]any)["name"] = "1orders"
			return x
		}(), "DNS-1035"},
		{"duplicate trigger names", fnWithParams(xr, map[string]any{"triggers": []any{
			map[string]any{"type": "cron", "name": "tick", "schedule": "@hourly"},
			map[string]any{"type": "cron", "name": "tick", "schedule": "@daily"},
		}}), "distinct name"},
		{"host label too long", fnWithParams(xr, map[string]any{"hostname": strings.Repeat("a", 64) + ".example.com"}), "at most 63"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := fnRender(t, c.xr, fnCluster(t, ""), nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("rendered without the expected refusal %q: %v", c.want, err)
			}
		})
	}
}

// provider-kubernetes applies every composed object with its own identity, so
// each kind the composition can emit needs a grant — create to make it, patch
// because the provider computes desired state with a server-side dry run even
// for an Object it may only observe. A missing grant is a 403 the Function
// reports only as a generic sync error.
func TestFunctionComposedKindsAreGrantedToProviderKubernetes(t *testing.T) {
	type gk struct{ group, resource string }
	emitted := map[gk]bool{}
	for _, fixture := range []string{"xr-http.yaml", "xr-events.yaml"} {
		for _, d := range fnMustRender(t, fnXR(t, fixture), fnCluster(t, ""), nil) {
			m, _ := dig(d, "spec", "forProvider", "manifest").(map[string]any)
			if m == nil {
				continue
			}
			group := ""
			if api, _ := m["apiVersion"].(string); strings.Contains(api, "/") {
				group = api[:strings.LastIndex(api, "/")]
			}
			kind := strings.ToLower(m["kind"].(string))
			res := kind + "s"
			if strings.HasSuffix(kind, "y") {
				res = strings.TrimSuffix(kind, "y") + "ies"
			}
			emitted[gk{group, res}] = true
		}
	}
	if len(emitted) < 10 {
		t.Fatalf("only %d kinds emitted; the fixtures no longer exercise the composition", len(emitted))
	}

	var rules []struct {
		APIGroups []string `json:"apiGroups"`
		Resources []string `json:"resources"`
		Verbs     []string `json:"verbs"`
	}
	for _, d := range readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "providers/provider-runtime-rbac.yaml")) {
		if d["kind"] == "ClusterRole" && dig(d, "metadata", "name") == "crossplane-provider-kubernetes-compose" {
			raw, _ := json.Marshal(d["rules"])
			if err := json.Unmarshal(raw, &rules); err != nil {
				t.Fatal(err)
			}
		}
	}
	verbsFor := func(k gk) map[string]bool {
		out := map[string]bool{}
		for _, r := range rules {
			if fnContains(r.APIGroups, k.group) && fnContains(r.Resources, k.resource) {
				for _, v := range r.Verbs {
					out[v] = true
				}
			}
		}
		return out
	}
	var keys []gk
	for k := range emitted {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].group+keys[i].resource < keys[j].group+keys[j].resource })
	for _, k := range keys {
		v := verbsFor(k)
		for _, need := range []string{"get", "list", "watch", "create", "patch"} {
			if !v[need] {
				t.Errorf("provider-kubernetes may not %s %s.%s, which the Function composition emits", need, k.resource, k.group)
			}
		}
	}
	// The domain claim is create-only by design; a grant to delete it would
	// let one namespace release a host another holds.
	if v := verbsFor(gk{"networking.internal.knative.dev", "clusterdomainclaims"}); v["delete"] || v["update"] {
		t.Error("clusterdomainclaims are granted update/delete; the composition never needs either")
	}
}

// v1alpha1 stays compatible: nothing an existing Function says becomes
// invalid, and the fields the console and the templates write still exist.
func TestFunctionXRDStaysCompatible(t *testing.T) {
	docs := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd/function.xrd.yaml"))
	v0 := dig(docs[0], "spec", "versions").([]any)[0].(map[string]any)
	params := dig(v0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "parameters", "properties").(map[string]any)

	runtimes := map[string]bool{}
	for _, r := range dig(params, "runtime", "enum").([]any) {
		runtimes[r.(string)] = true
	}
	for _, r := range []string{"node20", "python312", "go122", "custom", "node22", "node24", "python313", "go125", "java21", "java25", "deno2"} {
		if !runtimes[r] {
			t.Errorf("runtime %s is not accepted", r)
		}
	}
	if fmt.Sprint(dig(params, "trigger", "enum")) != "[http cloudevent topic bucket cron]" || dig(params, "eventSource") == nil {
		t.Error("the one-trigger shorthand (trigger + eventSource) changed")
	}
	for _, f := range []string{"image", "port", "minScale", "maxScale", "targetConcurrency", "timeoutSeconds", "env", "labels", "annotations", "organisation", "team", "project", "environment"} {
		if params[f] == nil {
			t.Errorf("parameter %s was removed", f)
		}
	}
	// The namespace decides these; a schema default could not be told apart
	// from a deliberate value.
	for _, f := range []string{"organisation", "team", "environment"} {
		if _, d := params[f].(map[string]any)["default"]; d {
			t.Errorf("parameters.%s has a schema default again", f)
		}
	}
	status := dig(v0, "schema", "openAPIV3Schema", "properties", "status", "properties").(map[string]any)
	for _, f := range []string{"url", "address", "readyRevision", "latestRevision", "message", "triggers"} {
		if status[f] == nil {
			t.Errorf("status.%s is not in the schema; the composition writes it and the API server would prune it", f)
		}
	}
}

// Argo CD treats an unknown kind as Healthy on creation, so a Function
// delivered through GitOps (and the Kargo verification behind it) would pass
// before it had served a request. All four files: the install manifests are
// generated from the values.
func TestArgoCDAssessesFunctionHealth(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"platform/controllers/adharplatform/resources/argocd/install.yaml",
		"platform/controllers/adharplatform/resources/argocd/install-ha.yaml",
		"hack/argocd/values.yaml",
		"hack/argocd/values-ha.yaml",
	} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		i := strings.Index(s, "resource.customizations.health.platform.adhar.io_Function: |")
		if i < 0 {
			t.Errorf("%s has no health check for platform.adhar.io/Function", f)
			continue
		}
		body := s[i:]
		if j := strings.Index(body[1:], "resource.customizations."); j > 0 {
			body = body[:j]
		}
		for _, want := range []string{`synced.reason == "ReconcileError"`, `c.type == "ServiceReady"`, `service.status == "False"`, `"HostClaimed"`, `ready.status == "True"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the Function health check does not consider %s", f, want)
			}
		}
	}
}

// The render fixtures run the same function packages the platform installs,
// and the composition's pipeline uses exactly those.
func TestFunctionRenderFixturesTrackTheInstalledFunctions(t *testing.T) {
	installed := map[string]string{}
	for _, d := range readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "functions/functions.yaml")) {
		installed[dig(d, "metadata", "name").(string)], _ = dig(d, "spec", "package").(string)
	}
	fixtures := map[string]string{}
	for _, d := range fnFixture(t, "functions.yaml") {
		fixtures[dig(d, "metadata", "name").(string)], _ = dig(d, "spec", "package").(string)
	}
	var comp map[string]any
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, fnComposition)), &comp); err != nil {
		t.Fatal(err)
	}
	for _, s := range dig(comp, "spec", "pipeline").([]any) {
		name := dig(s.(map[string]any), "functionRef", "name").(string)
		if installed[name] == "" {
			t.Errorf("the composition runs %s, which the platform does not install", name)
		}
		if fixtures[name] != installed[name] {
			t.Errorf("testdata runs %s as %q, the platform as %q", name, fixtures[name], installed[name])
		}
	}
}

// The in-process render above uses the same template language; this one runs
// the real function containers through `crossplane render`, the same path
// Crossplane takes, so a difference between the two engines cannot hide.
func TestFunctionCompositionRendersWithCrossplane(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping `crossplane render`, which starts the function containers")
	}
	bin, err := exec.LookPath("crossplane")
	if err != nil {
		t.Skip("the crossplane CLI is not installed; skipping the render through the real functions (the in-process render still ran)")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available; `crossplane render` runs each function in a container")
	}
	composition := filepath.Join(controlPlaneDir(t), fnComposition)
	render := func(xr string, extra ...string) []map[string]any {
		t.Helper()
		args := append([]string{"render", filepath.Join(fnFixtureDir, xr), composition, filepath.Join(fnFixtureDir, "functions.yaml"),
			"--required-resources=" + filepath.Join(fnFixtureDir, "required-resources.yaml")}, extra...)
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("crossplane render %s: %v\n%s", xr, err, stderr.String())
		}
		var docs []map[string]any
		for _, d := range strings.Split(stdout.String(), "\n---") {
			var m map[string]any
			if err := yaml.Unmarshal([]byte(d), &m); err == nil && len(m) > 0 {
				docs = append(docs, m)
			}
		}
		return docs
	}
	readyOf := func(docs []map[string]any) map[string]any {
		for _, d := range docs {
			if d["kind"] != "Function" {
				continue
			}
			for _, c := range dig(d, "status", "conditions").([]any) {
				if m := c.(map[string]any); m["type"] == "Ready" {
					return m
				}
			}
		}
		t.Fatal("the rendered Function has no Ready condition")
		return nil
	}
	resourceNames := func(docs []map[string]any) []string {
		var out []string
		for _, d := range docs {
			if n, _ := dig(d, "metadata", "annotations", "crossplane.io/composition-resource-name").(string); n != "" {
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out
	}

	first := render("xr-http.yaml")
	if got := strings.Join(resourceNames(first), ","); got != "domain-claim,domain-mapping,ingress-policy,knative-service,route,route-grant" {
		t.Errorf("an HTTP function composes %s", got)
	}
	if r := readyOf(first); r["status"] != "False" {
		t.Errorf("before anything is observed the Function is Ready=%v", r["status"])
	}
	second := render("xr-http.yaml", "--observed-resources="+filepath.Join(fnFixtureDir, "observed-orders.yaml"))
	if r := readyOf(second); r["status"] != "True" {
		t.Errorf("with every composed resource Ready the Function is Ready=%v: %v", r["status"], r["message"])
	}
	for _, d := range second {
		if d["kind"] == "Function" && (dig(d, "status", "readyRevision") != "orders-00002" || dig(d, "status", "url") != "https://orders.dev.cloud.example.com") {
			t.Errorf("rendered status = %v", d["status"])
		}
	}
	events := render("xr-events.yaml")
	if got := strings.Join(resourceNames(events), ","); got != "broker-default,domain-claim,domain-mapping,knative-service,route,route-grant,trigger-cloudevent-1,trigger-nightly,trigger-topic-2" {
		t.Errorf("the event-driven function composes %s", got)
	}
	legacy := render("xr-legacy.yaml")
	if got := strings.Join(resourceNames(legacy), ","); got != "ingress-policy,knative-service,trigger-topic" {
		t.Errorf("the shorthand topic function composes %s", got)
	}
}
