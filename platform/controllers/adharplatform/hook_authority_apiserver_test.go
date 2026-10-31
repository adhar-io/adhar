package adharplatform

// The Roles and the admission policy that bound what the platform's hooks may
// do, checked where they take effect: a real kube-apiserver (envtest, the
// version in bin/k8s), with RBAC on and the shipped objects applied, and every
// request made AS the hook's ServiceAccount — by client-go and, for the calls
// the scripts make, by the real kubectl. Reading the YAML cannot show that
// `resourceNames` covers the LIST and WATCH `kubectl rollout status` makes, that
// a server-side-apply create is authorised by name while a POST is not, or that
// a ValidatingAdmissionPolicy refuses a DELETE the Role allows.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const hookNamespace = "adhar-system"

// The objects of a hook manifest this test applies; everything else (the Job,
// generators, ExternalSecrets) needs controllers or CRDs the test server lacks.
var hookObjectResources = map[string]struct {
	gvr        schema.GroupVersionResource
	namespaced bool
}{
	"ServiceAccount":                   {schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, true},
	"Role":                             {schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, true},
	"RoleBinding":                      {schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, true},
	"ValidatingAdmissionPolicy":        {schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicies"}, false},
	"ValidatingAdmissionPolicyBinding": {schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicybindings"}, false},
}

// envtestAssets: KUBEBUILDER_ASSETS when set (make test), else the newest
// envtest download in bin/k8s, so the test also runs from an IDE.
func envtestAssets(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("KUBEBUILDER_ASSETS"); dir != "" {
		return dir
	}
	dirs, _ := filepath.Glob(filepath.Join("..", "..", "..", "bin", "k8s", "*"))
	sort.Strings(dirs)
	for i := len(dirs) - 1; i >= 0; i-- {
		if _, err := os.Stat(filepath.Join(dirs[i], "kube-apiserver")); err == nil {
			return dirs[i]
		}
	}
	t.Fatal("no envtest binaries: set KUBEBUILDER_ASSETS or run `make setup-envtest`")
	return ""
}

type hookAPIServer struct {
	ctx     context.Context
	cfg     *rest.Config
	admin   kubernetes.Interface
	dyn     dynamic.Interface
	kubectl kubectlRunner // the real kubectl, as an administrator who may impersonate
}

// kubectlRunner is envtest's kubectl wrapper (its type is internal to envtest).
type kubectlRunner interface {
	Run(args ...string) (stdout, stderr io.Reader, err error)
}

func serviceAccountUser(name string) string {
	return "system:serviceaccount:" + hookNamespace + ":" + name
}

// as returns a client that makes every request as the hook's ServiceAccount.
func (s *hookAPIServer) as(serviceAccount string) kubernetes.Interface {
	c := rest.CopyConfig(s.cfg)
	c.Impersonate = rest.ImpersonationConfig{
		UserName: serviceAccountUser(serviceAccount),
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:" + hookNamespace, "system:authenticated"},
	}
	return kubernetes.NewForConfigOrDie(c)
}

// kubectlAs runs the real kubectl (bin/k8s) as the hook's ServiceAccount and
// returns stdout and stderr together.
func (s *hookAPIServer) kubectlAs(serviceAccount string, args ...string) (string, error) {
	stdout, stderr, err := s.kubectl.Run(append([]string{
		"--as=" + serviceAccountUser(serviceAccount),
		"--as-group=system:serviceaccounts", "--as-group=system:serviceaccounts:" + hookNamespace,
	}, args...)...)
	o, _ := io.ReadAll(stdout)
	e, _ := io.ReadAll(stderr)
	return strings.TrimSpace(string(o) + "\n" + string(e)), err
}

// apply creates the hook manifest's ServiceAccount, Roles, RoleBindings and
// admission policies exactly as the stack ships them.
func (s *hookAPIServer) apply(t *testing.T, rel string) {
	t.Helper()
	applied := 0
	for _, doc := range manifestDocs(readStackFile(t, rel)) {
		res, ok := hookObjectResources[toString(doc["kind"])]
		if !ok {
			continue
		}
		client := s.dyn.Resource(res.gvr)
		var err error
		if res.namespaced {
			_, err = client.Namespace(hookNamespace).Create(s.ctx, &unstructured.Unstructured{Object: doc}, metav1.CreateOptions{})
		} else {
			_, err = client.Create(s.ctx, &unstructured.Unstructured{Object: doc}, metav1.CreateOptions{})
		}
		if err != nil {
			t.Fatalf("%s: creating %s/%s: %v", rel, doc["kind"], metaName(doc), err)
		}
		applied++
	}
	if applied == 0 {
		t.Fatalf("%s: nothing to apply", rel)
	}
}

func (s *hookAPIServer) secret(t *testing.T, name string) {
	t.Helper()
	_, err := s.admin.CoreV1().Secrets(hookNamespace).Create(s.ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Data:       map[string][]byte{"PLANE_APP_CLIENT_ID": []byte("plane-app"), "PLANE_APP_CLIENT_SECRET": []byte("x")},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating Secret %s: %v", name, err)
	}
}

func (s *hookAPIServer) pod(t *testing.T, name string, labels map[string]string) {
	t.Helper()
	_, err := s.admin.CoreV1().Pods(hookNamespace).Create(s.ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "registry.k8s.io/pause:3.10"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating Pod %s: %v", name, err)
	}
}

func (s *hookAPIServer) deployment(t *testing.T, name string) {
	t.Helper()
	labels := map[string]string{"app": name}
	_, err := s.admin.AppsV1().Deployments(hookNamespace).Create(s.ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "registry.k8s.io/pause:3.10"}}},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating Deployment %s: %v", name, err)
	}
}

// rolledOut writes the status the Deployment controller (not running here)
// writes once a rollout completes, so `kubectl rollout status` can finish.
func (s *hookAPIServer) rolledOut(t *testing.T, name string) {
	t.Helper()
	d, err := s.admin.AppsV1().Deployments(hookNamespace).Get(s.ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if _, err := s.admin.AppsV1().Deployments(hookNamespace).UpdateStatus(s.ctx, d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// TestHookAuthorityOnARealAPIServer: every hook may do what its script does,
// and the next thing over is refused.
func TestHookAuthorityOnARealAPIServer(t *testing.T) {
	env := &envtest.Environment{BinaryAssetsDirectory: envtestAssets(t)}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	operator, err := env.AddUser(envtest.User{Name: "hook-authority-test", Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	kubectl, err := operator.Kubectl()
	if err != nil {
		t.Fatal(err)
	}
	s := &hookAPIServer{ctx: context.Background(), cfg: cfg, kubectl: kubectl,
		admin: kubernetes.NewForConfigOrDie(cfg), dyn: dynamic.NewForConfigOrDie(cfg)}
	if _, err := s.admin.CoreV1().Namespaces().Create(s.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: hookNamespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if v, err := s.admin.Discovery().ServerVersion(); err == nil {
		t.Logf("kube-apiserver %s", v.GitVersion)
	}

	// clickhouse-unstick restarts the operator by deleting its pod. RBAC cannot
	// narrow `delete` to one label; the shipped ValidatingAdmissionPolicy must.
	t.Run("posthog-clickhouse-unstick deletes the ClickHouse operator's pod and no other", func(t *testing.T) {
		s.apply(t, "application/posthog/manifests/clickhouse-unstick.yaml")
		_, script := clickhouseUnstickScript(t)
		m := regexp.MustCompile(`delete pod -l (\S+)=(\S+)`).FindStringSubmatch(script)
		if m == nil {
			t.Fatal("the unstick script no longer deletes the operator by `delete pod -l <label>=<value>`; point this test at how it restarts the operator now")
		}
		const operator, bystander, impostor = "clickhouse-operator-7c9d8-x2v4q", "posthog-web-6d5f9-k8m2n", "clickhouse-operator-impostor"
		s.pod(t, operator, map[string]string{m[1]: m[2]})
		s.pod(t, bystander, map[string]string{m[1]: "posthog"})
		s.pod(t, impostor, map[string]string{"app.kubernetes.io/name": "clickhouse-operator"})
		sa := s.as("posthog-clickhouse-unstick")

		// The API server loads a new policy asynchronously; a dry-run delete
		// (admission runs, nothing is deleted) shows when it is in force.
		deadline := time.Now().Add(30 * time.Second)
		for {
			err := sa.CoreV1().Pods(hookNamespace).Delete(s.ctx, bystander, metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}})
			if apierrors.IsForbidden(err) && strings.Contains(err.Error(), "ValidatingAdmissionPolicy") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("as posthog-clickhouse-unstick, a dry-run delete of pod %s was never refused by an admission policy (last answer: %v): the Role alone lets this ServiceAccount delete ANY pod in %s", bystander, err, hookNamespace)
			}
			time.Sleep(200 * time.Millisecond)
		}

		for _, name := range []string{bystander, impostor} {
			out, err := s.kubectlAs("posthog-clickhouse-unstick", "-n", hookNamespace, "delete", "pod", name, "--wait=false")
			switch {
			case err == nil:
				t.Errorf("as posthog-clickhouse-unstick, `kubectl delete pod %s` succeeded: the ServiceAccount may delete pods other than the operator's", name)
			case !strings.Contains(out, "may delete only the ClickHouse operator pod"):
				t.Errorf("deleting pod %s was refused, but not by the shipped policy: %s", name, out)
			default:
				t.Logf("refused, as it must be: %s", out)
			}
		}
		// The script's own command, impersonation aside.
		out, err := s.kubectlAs("posthog-clickhouse-unstick", "-n", hookNamespace, "delete", "pod", "-l", m[1]+"="+m[2], "--wait=false")
		if err != nil {
			t.Fatalf("the script's `kubectl delete pod -l %s=%s --wait=false` was refused: %v\n%s", m[1], m[2], err, out)
		}
		t.Logf("the script's own delete: %s", out)
		pods, err := s.admin.CoreV1().Pods(hookNamespace).List(s.ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		left := map[string]bool{}
		for _, p := range pods.Items {
			left[p.Name] = true
		}
		if left[operator] || !left[bystander] || !left[impostor] {
			t.Errorf("after the script's delete the pods left are %v; want the operator's gone and the other two kept", sortedKeys(left))
		}
		// The policy is about this ServiceAccount only.
		if err := s.admin.CoreV1().Pods(hookNamespace).Delete(s.ctx, bystander, metav1.DeleteOptions{}); err != nil {
			t.Errorf("the policy also refused an administrator's delete: %v", err)
		}
	})

	// plane-instance-setup reads keycloak-clients and writes plane-api-token.
	t.Run("plane-instance-setup reads and writes only its two Secrets", func(t *testing.T) {
		const rel = "application/plane/manifests/instance-setup.yaml"
		s.apply(t, rel)
		env, _ := containerEnv(t, readStackFile(t, rel), "Job", "setup")
		read, write := env["KEYCLOAK_CLIENTS_SECRET"], env["TOKEN_SECRET_NAME"]
		s.secret(t, read)
		s.secret(t, "gitea-credential")
		sa := s.as("plane-instance-setup")

		if _, err := sa.CoreV1().Secrets(hookNamespace).Get(s.ctx, read, metav1.GetOptions{}); err != nil {
			t.Errorf("the setup Job cannot read %s, where its Keycloak client waits: %v", read, err)
		}
		if _, err := sa.CoreV1().Secrets(hookNamespace).Get(s.ctx, "gitea-credential", metav1.GetOptions{}); !apierrors.IsForbidden(err) {
			t.Errorf("the setup Job can read Secret gitea-credential (err=%v); it needs only %s", err, read)
		}
		if _, err := sa.CoreV1().Secrets(hookNamespace).List(s.ctx, metav1.ListOptions{}); !apierrors.IsForbidden(err) {
			t.Errorf("the setup Job can list every Secret (err=%v)", err)
		}
		// The write the script used to make: a POST carries no name, so not
		// even the Secret the Role names may be created that way.
		if _, err := sa.CoreV1().Secrets(hookNamespace).Create(s.ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: write}}, metav1.CreateOptions{}); !apierrors.IsForbidden(err) {
			t.Errorf("a POST create of %s was not refused (err=%v): a create with no name in the request can only be granted on every Secret", write, err)
		} else {
			t.Logf("POST create of %s refused, as it must be: %v", write, err)
		}
		// The write the script makes now, the same request: server-side apply
		// of a JSON body to the Secret's own URL, first creating, then updating.
		ssa := func(name string) (int, error) {
			body, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
				"metadata": map[string]any{"name": name, "namespace": hookNamespace, "labels": map[string]string{"app.kubernetes.io/part-of": "plane"}},
				"data":     map[string]string{"PLANE_URL": "aHR0cDovL3BsYW5lLWFwaQ==", "PLANE_TOKEN": "dG9rZW4=", "PLANE_WORKSPACE": "YWRoYXI="}})
			code := 0
			err := sa.CoreV1().RESTClient().Patch(types.ApplyPatchType).Namespace(hookNamespace).Resource("secrets").Name(name).
				Param("fieldManager", "plane-instance-setup").Param("force", "true").Body(body).Do(s.ctx).StatusCode(&code).Error()
			return code, err
		}
		for i, want := range []int{201, 200} {
			if code, err := ssa(write); err != nil || code != want {
				t.Errorf("server-side apply of %s, run %d: HTTP %d, %v (want %d — the create the apply implies is authorised by the Secret's name)", write, i+1, code, err, want)
			} else {
				t.Logf("server-side apply of %s, run %d: HTTP %d", write, i+1, code)
			}
		}
		if _, err := ssa("plane-api-token-elsewhere"); !apierrors.IsForbidden(err) {
			t.Errorf("the setup Job can server-side apply a Secret it does not own (err=%v)", err)
		} else {
			t.Logf("server-side apply of another Secret refused: %v", err)
		}
	})

	// The reload hooks rewritten from exec to timestamps (and the AI runtime's,
	// which still asks the process) name the Secrets and the Deployment they
	// touch; rustfs's activation hook keeps an unnamed Role but must watch.
	for _, h := range []struct {
		rel, serviceAccount, deployment, selector string
		secrets                                   []string
		named, exec                               bool
	}{
		{"observability/kube-prometheus/manifests/grafana-oidc-reload.yaml", "grafana-oidc-reload", "prometheus-grafana",
			"app.kubernetes.io/instance=prometheus,app.kubernetes.io/name=grafana", []string{"grafana-oidc"}, true, false},
		{"application/coder/manifests/oidc-reload.yaml", "coder-oidc-reload", "coder",
			"app.kubernetes.io/instance=coder,app.kubernetes.io/name=coder", []string{"coder-oidc", "coder-gitea-oauth"}, true, false},
		{"ai/adhar-ai/manifests/oidc-reload.yaml", "adhar-ai-oidc-reload", "adhar-ai-runtime",
			"app.kubernetes.io/name=adhar-ai-runtime", []string{"keycloak-clients"}, true, true},
		{"data/rustfs/manifests/oidc.yaml", "rustfs-oidc-activate", "rustfs", "", []string{"rustfs-oidc"}, false, false},
	} {
		t.Run(h.serviceAccount, func(t *testing.T) {
			s.apply(t, h.rel)
			s.deployment(t, h.deployment)
			s.deployment(t, "argo-cd-argocd-server")
			s.secret(t, "gitea-credential")
			for _, name := range h.secrets {
				s.secret(t, name)
			}
			sa := s.as(h.serviceAccount)
			deployments := sa.AppsV1().Deployments(hookNamespace)
			byName := metav1.ListOptions{FieldSelector: "metadata.name=" + h.deployment}

			for _, name := range h.secrets {
				if _, err := sa.CoreV1().Secrets(hookNamespace).Get(s.ctx, name, metav1.GetOptions{}); err != nil {
					t.Errorf("cannot read Secret %s, which the script waits on: %v", name, err)
				}
			}
			if _, err := deployments.List(s.ctx, byName); err != nil {
				t.Errorf("cannot LIST Deployment %s by name, which `kubectl rollout status` does: %v", h.deployment, err)
			}
			if w, err := deployments.Watch(s.ctx, byName); err != nil {
				t.Errorf("cannot WATCH Deployment %s by name, which `kubectl rollout status` does (without it: \"Failed to watch … unknown\"): %v", h.deployment, err)
			} else {
				select {
				case ev := <-w.ResultChan():
					if ev.Type != watch.Added {
						t.Errorf("the watch on %s opened with %s, not the object", h.deployment, ev.Type)
					}
				case <-time.After(10 * time.Second):
					t.Errorf("the watch on %s delivered nothing", h.deployment)
				}
				w.Stop()
			}

			// The script's restart and wait, as the real kubectl makes them.
			if out, err := s.kubectlAs(h.serviceAccount, "-n", hookNamespace, "rollout", "restart", "deployment", h.deployment); err != nil {
				t.Errorf("`kubectl rollout restart deployment %s` refused: %v\n%s", h.deployment, err, out)
			}
			s.rolledOut(t, h.deployment)
			if out, err := s.kubectlAs(h.serviceAccount, "-n", hookNamespace, "rollout", "status", "deployment", h.deployment, "--timeout=30s"); err != nil || strings.Contains(out, "Failed to watch") || strings.Contains(out, "forbidden") {
				t.Errorf("`kubectl rollout status deployment %s`: %v\n%s", h.deployment, err, out)
			} else {
				t.Logf("rollout status as %s: %s", h.serviceAccount, out)
			}
			if h.selector != "" {
				if out, err := s.kubectlAs(h.serviceAccount, "-n", hookNamespace, "get", "pods", "-l", h.selector,
					"-o", `jsonpath={range .items[*]}{.status.startTime}{"\n"}{end}`); err != nil {
					t.Errorf("the script's pod list is refused: %v\n%s", err, out)
				}
			}
			if h.exec {
				review, err := s.admin.AuthorizationV1().SubjectAccessReviews().Create(s.ctx, &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
					User:               serviceAccountUser(h.serviceAccount),
					Groups:             []string{"system:serviceaccounts", "system:serviceaccounts:" + hookNamespace, "system:authenticated"},
					ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: hookNamespace, Verb: "create", Resource: "pods", Subresource: "exec"},
				}}, metav1.CreateOptions{})
				if err != nil || !review.Status.Allowed {
					t.Errorf("the hook asks the runtime for its env with `kubectl exec`, and may not (err=%v)", err)
				}
			}
			if !h.named {
				return
			}
			// What the Role no longer reaches.
			restart := []byte(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"now"}}}}}`)
			if _, err := deployments.Patch(s.ctx, "argo-cd-argocd-server", types.StrategicMergePatchType, restart, metav1.PatchOptions{}); !apierrors.IsForbidden(err) {
				t.Errorf("can patch Deployment argo-cd-argocd-server (err=%v); it restarts only %s", err, h.deployment)
			}
			if _, err := deployments.List(s.ctx, metav1.ListOptions{}); !apierrors.IsForbidden(err) {
				t.Errorf("can list every Deployment (err=%v)", err)
			}
			if _, err := sa.CoreV1().Secrets(hookNamespace).Get(s.ctx, "gitea-credential", metav1.GetOptions{}); !apierrors.IsForbidden(err) {
				t.Errorf("can read Secret gitea-credential (err=%v); it reads only %v", err, h.secrets)
			}
			if _, err := sa.CoreV1().Secrets(hookNamespace).List(s.ctx, metav1.ListOptions{}); !apierrors.IsForbidden(err) {
				t.Errorf("can list every Secret (err=%v)", err)
			}
		})
	}
}
