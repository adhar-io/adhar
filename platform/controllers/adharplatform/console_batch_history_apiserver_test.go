package adharplatform

// The console's batch-history grant (design 0025 D12), checked where it takes
// effect: a real kube-apiserver (envtest, the version in bin/k8s), RBAC on, the
// console package's own RBAC applied, and the three requests the Strimzi
// consumer protocol makes sent AS the `console` ServiceAccount.
//
// console_batch_history_rbac_test.go reads the YAML. It cannot show the two
// things the grant rests on, and both are the API server's to decide: that a
// proxy request is authorised by the name IN THE URL (`<service>:<port>`, so a
// rule naming the bare Service matches nothing), and that POST and DELETE on
// the proxy are `create` and `delete`, not `get`. D12 says "`get` on
// services/proxy"; the last subtest is that sentence applied literally, and the
// first request of a history read refused.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The RBAC objects of the console package: what its ServiceAccount is given.
var consoleRBACResources = map[string]struct {
	gvr        schema.GroupVersionResource
	namespaced bool
}{
	"ServiceAccount":     {schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, true},
	"Role":               {schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, true},
	"RoleBinding":        {schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, true},
	"ClusterRole":        {schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, false},
	"ClusterRoleBinding": {schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, false},
}

// bridgeProxy is the URL of one bridge call through the service proxy.
func bridgeProxy(namespace, service, path string) string {
	return "/api/v1/namespaces/" + namespace + "/services/" + service + "/proxy" + path
}

func TestConsoleBridgeReadOnARealAPIServer(t *testing.T) {
	env := &envtest.Environment{BinaryAssetsDirectory: envtestAssets(t)}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	ctx := context.Background()
	admin := kubernetes.NewForConfigOrDie(cfg)
	dyn := dynamic.NewForConfigOrDie(cfg)
	for _, ns := range []string{hookNamespace, "payments-prod"} {
		if _, err := admin.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	// Everything the package gives the ServiceAccount, not the one Role: a
	// wider grant elsewhere in the file would make the refusals below pass.
	applied := 0
	for _, doc := range manifestDocs(readStackFile(t, "core/adhar-console/manifests/install.yaml")) {
		res, ok := consoleRBACResources[toString(doc["kind"])]
		if !ok {
			continue
		}
		client := dyn.Resource(res.gvr)
		if res.namespaced {
			_, err = client.Namespace(hookNamespace).Create(ctx, &unstructured.Unstructured{Object: doc}, metav1.CreateOptions{})
		} else {
			_, err = client.Create(ctx, &unstructured.Unstructured{Object: doc}, metav1.CreateOptions{})
		}
		if err != nil {
			t.Fatalf("creating %s/%s: %v", doc["kind"], metaName(doc), err)
		}
		applied++
	}
	if applied == 0 {
		t.Fatal("core/adhar-console/manifests/install.yaml carries no RBAC to apply")
	}

	// The bridge's Service, with no endpoints: an authorised request then ends
	// in 503 from the proxy itself, which only a request that passed
	// authorisation and found the Service's port reaches.
	svc, port, ok := strings.Cut(eventBridgeProxyName, ":")
	if !ok {
		t.Fatalf("%q is not <service>:<port>", eventBridgeProxyName)
	}
	bridge := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: svc},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "rest-api", Port: 8080}}},
	}
	for _, s := range []struct{ ns, name string }{{hookNamespace, svc}, {hookNamespace, "gitea-http"}, {"payments-prod", svc}} {
		o := bridge.DeepCopy()
		o.Name = s.name
		if _, err := admin.CoreV1().Services(s.ns).Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		// No controller runs here to write the Service's Endpoints, and the
		// proxy reads them: an empty object is "no endpoints".
		//nolint:staticcheck // the service proxy still resolves through v1 Endpoints
		if _, err := admin.CoreV1().Endpoints(s.ns).Create(ctx, &corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: s.name}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	as := func(serviceAccount string) rest.Interface {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{
			UserName: serviceAccountUser(serviceAccount),
			Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:" + hookNamespace, "system:authenticated"},
		}
		return kubernetes.NewForConfigOrDie(c).CoreV1().RESTClient()
	}
	// send answers with the API server's verdict on one proxy request.
	send := func(c rest.Interface, method, url string) error {
		req := c.Verb(method).AbsPath(url)
		if method == http.MethodPost || method == http.MethodPut {
			req = req.SetHeader("Content-Type", "application/vnd.kafka.v2+json").Body([]byte(`{}`))
		}
		return req.Do(ctx).Error()
	}
	const consumer = "/consumers/adhar-console-x1"
	own := bridgeProxy(hookNamespace, eventBridgeProxyName, consumer)
	console := as("console")

	// The authoriser learns of a new Role asynchronously; the first request of
	// a read shows when it has.
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := send(console, http.MethodPost, own)
		if err != nil && !apierrors.IsForbidden(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("as the console ServiceAccount, POST %s was never authorised (last answer: %v): /api/batch/history cannot create its bridge consumer", own, err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Run("the three requests of a history read are authorised", func(t *testing.T) {
		for _, r := range []struct{ method, path, what string }{
			{http.MethodPost, consumer, "create the consumer"},
			{http.MethodPost, consumer + "/instances/history-x1/subscription", "subscribe it"},
			{http.MethodGet, consumer + "/instances/history-x1/records", "poll it"},
			{http.MethodDelete, consumer + "/instances/history-x1", "delete it"},
		} {
			err := send(console, r.method, bridgeProxy(hookNamespace, eventBridgeProxyName, r.path))
			switch {
			case apierrors.IsForbidden(err):
				t.Errorf("%s (%s %s) is refused: %v", r.what, r.method, r.path, err)
			case !apierrors.IsServiceUnavailable(err):
				// Not a refusal, but not the proxy's "no endpoints" either: the
				// request did not get as far as this test means it to.
				t.Errorf("%s (%s %s) answered %v, want the proxy's 503 for a Service with no endpoints", r.what, r.method, r.path, err)
			}
		}
	})

	t.Run("and nothing next to them is", func(t *testing.T) {
		for _, r := range []struct{ method, url, why string }{
			{http.MethodPost, bridgeProxy(hookNamespace, svc, consumer), "the bare Service name is another name to the authoriser: the Role must carry the port, and so must the URL"},
			{http.MethodPost, bridgeProxy(hookNamespace, "http:"+svc+":"+port, consumer), "the scheme-qualified form is another name too"},
			{http.MethodPost, bridgeProxy(hookNamespace, "gitea-http:8080", "/api/v1/admin/users"), "the console must not POST to another Service through the API server"},
			{http.MethodDelete, bridgeProxy(hookNamespace, "gitea-http:8080", "/api/v1/repos/adhar/packages"), "nor DELETE on one"},
			{http.MethodPost, bridgeProxy("payments-prod", eventBridgeProxyName, consumer), "a Service of that name in another namespace is not the bridge"},
			{http.MethodPut, own, "the consumer protocol has no PUT: `update` is not part of the grant"},
		} {
			if err := send(console, r.method, r.url); !apierrors.IsForbidden(err) {
				t.Errorf("%s %s answered %v, want Forbidden: %s", r.method, r.url, err, r.why)
			}
		}
	})

	// D12 as written: "`get` on services/proxy for that bridge Service only".
	t.Run("`get` alone, as D12 words it, refuses the first request of a read", func(t *testing.T) {
		const literal = "d12-as-written"
		if _, err := admin.RbacV1().Roles(hookNamespace).Create(ctx, &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: literal},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"services/proxy"}, ResourceNames: []string{eventBridgeProxyName}, Verbs: []string{"get"}}},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.RbacV1().RoleBindings(hookNamespace).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: literal},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: literal},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: literal, Namespace: hookNamespace}},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		reader := as(literal)
		deadline := time.Now().Add(30 * time.Second)
		for {
			err := send(reader, http.MethodGet, bridgeProxy(hookNamespace, eventBridgeProxyName, consumer+"/instances/history-x1/records"))
			if err != nil && !apierrors.IsForbidden(err) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("a ServiceAccount with `get` on the bridge's proxy was never allowed its GET (last answer: %v)", err)
			}
			time.Sleep(200 * time.Millisecond)
		}
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			if err := send(reader, method, own); !apierrors.IsForbidden(err) {
				t.Errorf("with `get` alone, %s on the bridge's proxy answered %v, want Forbidden — if the API server now maps it to `get`, %s can drop create and delete and D12 stands as written", method, err, consoleBatchHistoryRole)
			}
		}
	})
}
