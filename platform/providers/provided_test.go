package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"adhar-io/adhar/platform/types"
)

const twoContextKubeconfig = `apiVersion: v1
kind: Config
current-context: other
clusters:
- name: mine
  cluster:
    server: https://mine.example.com:6443
- name: theirs
  cluster:
    server: https://theirs.example.com:6443
contexts:
- name: wanted
  context:
    cluster: mine
    user: me
- name: other
  context:
    cluster: theirs
    user: them
users:
- name: me
  user:
    token: mine
- name: them
  user:
    token: theirs
`

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(twoContextKubeconfig), 0o600); err != nil {
		t.Fatalf("writing kubeconfig: %v", err)
	}
	return path
}

// The named context — not whichever one is current — decides which cluster the
// platform installs onto, and nothing else in the file comes along.
//
// Both halves matter. Installing onto "whichever context was current" is how a
// platform lands on the wrong cluster, and the kubeconfig this returns is
// written out as KUBECONFIG for the whole bootstrap AND merged into the
// operator's own default kubeconfig, so carrying their other clusters into it is
// both noise and a second chance to target the wrong one.
func TestProvidedModeInstallsOntoTheNamedContextOnly(t *testing.T) {
	path := writeKubeconfig(t)
	p, err := NewProvidedProvider("custom", "", map[string]interface{}{
		"kubeconfig":  path,
		"kubeContext": "wanted",
	})
	if err != nil {
		t.Fatalf("NewProvidedProvider: %v", err)
	}
	if err := p.selectContext(); err != nil {
		t.Fatalf("selectContext: %v", err)
	}

	if p.kubeContext != "wanted" {
		t.Errorf("selected context = %q, want %q", p.kubeContext, "wanted")
	}
	if p.restConfig == nil || p.restConfig.Host != "https://mine.example.com:6443" {
		t.Errorf("client points at %v, want the server of the named context", p.restConfig)
	}
	if strings.Contains(p.kubeconfig, "theirs") {
		t.Errorf("the kubeconfig handed to the bootstrap still contains the other cluster:\n%s", p.kubeconfig)
	}
	if !strings.Contains(p.kubeconfig, "current-context: wanted") {
		t.Errorf("the kubeconfig handed to the bootstrap does not make the named context current:\n%s", p.kubeconfig)
	}
}

// An unknown context must be refused by name rather than silently falling back
// to the current one.
func TestProvidedModeRefusesAnUnknownContext(t *testing.T) {
	p, err := NewProvidedProvider("custom", "", map[string]interface{}{
		"kubeconfig":  writeKubeconfig(t),
		"kubeContext": "typo",
	})
	if err != nil {
		t.Fatalf("NewProvidedProvider: %v", err)
	}
	err = p.selectContext()
	if err == nil {
		t.Fatal("selectContext accepted a context that is not in the file")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Errorf("error does not name the missing context: %v", err)
	}
}

// A kubeconfig that is not there is a configuration error, reported before
// anything else happens — not a nil client discovered half-way through a
// bootstrap.
func TestProvidedModeRefusesAMissingKubeconfig(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	_, err := NewProvidedProvider("custom", "", map[string]interface{}{
		"kubeconfig": filepath.Join(t.TempDir(), "nope"),
	})
	if err == nil {
		t.Fatal("NewProvidedProvider accepted a kubeconfig path that does not exist")
	}
	if !strings.Contains(err.Error(), "not readable") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// $KUBECONFIG is the fallback when the config names no path, and only its FIRST
// entry: a kubeconfig list has no single cluster.
func TestProvidedModeFallsBackToKUBECONFIG(t *testing.T) {
	path := writeKubeconfig(t)
	t.Setenv("KUBECONFIG", path+string(os.PathListSeparator)+filepath.Join(t.TempDir(), "second"))
	p, err := NewProvidedProvider("custom", "", map[string]interface{}{})
	if err != nil {
		t.Fatalf("NewProvidedProvider: %v", err)
	}
	if p.kubeconfigPath != path {
		t.Errorf("kubeconfig = %q, want the first $KUBECONFIG entry %q", p.kubeconfigPath, path)
	}
}

// THE guard of this whole mode: nothing here deletes, scales or rebuilds a
// cluster the platform did not create. Every one of these is a path that, on a
// cloud provider, destroys or bills infrastructure.
func TestProvidedProviderRefusesEveryLifecycleCall(t *testing.T) {
	p := &ProvidedProvider{providerName: "custom", client: fake.NewSimpleClientset()}
	ctx := context.Background()

	for name, call := range map[string]func() error{
		"DeleteCluster":      func() error { return p.DeleteCluster(ctx, "adhar-prod") },
		"UpdateCluster":      func() error { return p.UpdateCluster(ctx, "adhar-prod", &types.ClusterSpec{}) },
		"UpgradeCluster":     func() error { return p.UpgradeCluster(ctx, "adhar-prod", "1.38.0") },
		"ScaleNodeGroup":     func() error { return p.ScaleNodeGroup(ctx, "adhar-prod", "workers", 9) },
		"AddNodeGroup":       func() error { _, err := p.AddNodeGroup(ctx, "adhar-prod", nil); return err },
		"RemoveNodeGroup":    func() error { return p.RemoveNodeGroup(ctx, "adhar-prod", "workers") },
		"DeleteVPC":          func() error { return p.DeleteVPC(ctx, "vpc-1") },
		"DeleteLoadBalancer": func() error { return p.DeleteLoadBalancer(ctx, "lb-1") },
		"DeleteStorage":      func() error { return p.DeleteStorage(ctx, "vol-1") },
	} {
		if err := call(); err == nil {
			t.Errorf("%s succeeded on a provided cluster; it must refuse — the platform did not create this infrastructure", name)
		} else if !strings.Contains(err.Error(), ClusterModeProvided) {
			t.Errorf("%s refused without saying why (the mode): %v", name, err)
		}
	}

	// And the refusal must be reachable through the shared predicate, which is
	// what every caller outside this file checks.
	if ClusterLifecycleIsOurs(ClusterModeProvided) {
		t.Error("ClusterLifecycleIsOurs(provided) is true — every destructive path is gated on it")
	}
}

// CreateCluster adopts: it must report the cluster that is already there and
// must not have asked a cloud for anything.
func TestProvidedCreateClusterAdoptsRatherThanCreates(t *testing.T) {
	p := &ProvidedProvider{
		providerName: "custom",
		kubeContext:  "wanted",
		client:       fake.NewSimpleClientset(),
	}
	spec := &types.ClusterSpec{}
	spec.Name = "adhar-prod"

	cluster, err := p.CreateCluster(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateCluster: %v", err)
	}
	if cluster.Name != "adhar-prod" {
		t.Errorf("cluster name = %q, want the platform's name for it", cluster.Name)
	}
	if cluster.Status != types.ClusterStatusRunning {
		t.Errorf("status = %q, want %q: the cluster was already running", cluster.Status, types.ClusterStatusRunning)
	}
	if mode, _ := cluster.Metadata["mode"].(string); mode != ClusterModeProvided {
		t.Errorf("metadata mode = %q, want %q — operations read this back to decide what they may touch",
			mode, ClusterModeProvided)
	}
}

// ListClusters must not invent a cluster. `adhar down` finds what to delete by
// listing every configured provider and matching a name, so a provider that
// answered "yes, that's mine" for any name would hand a teardown a live cluster.
func TestProvidedListClustersInventsNothing(t *testing.T) {
	p := &ProvidedProvider{providerName: "custom"}
	clusters, err := p.ListClusters(context.Background())
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	if len(clusters) != 0 {
		t.Errorf("ListClusters returned %d cluster(s) before any was adopted", len(clusters))
	}
}

func ciliumDaemonSet(namespace string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "cilium", Namespace: namespace}}
}

func ciliumConfig(namespace, gatewayAPI string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cilium-config", Namespace: namespace},
		Data:       map[string]string{"enable-gateway-api": gatewayAPI},
	}
}

// The CNI decision. A cluster running someone else's CNI must be REFUSED, not
// converted: the platform's data path is Cilium with kubeProxyReplacement and
// its Gateway is a Cilium Gateway, so installing means replacing the networking
// of a cluster the operator never handed over.
func TestProvidedPreflightDecidesOnTheExistingCNI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		objects []runtimeObject
		status  CheckStatus
		mention string
	}{
		{
			name:    "no CNI at all: the platform installs Cilium",
			objects: nil,
			status:  CheckPass,
			mention: "no CNI",
		},
		{
			name:    "Cilium already there, Gateway API on: reused untouched",
			objects: []runtimeObject{ciliumDaemonSet("kube-system"), ciliumConfig("kube-system", "true")},
			status:  CheckPass,
			mention: "already runs",
		},
		{
			name:    "Calico: refused",
			objects: []runtimeObject{&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "calico-node", Namespace: "kube-system"}}},
			status:  CheckFail,
			mention: "Calico",
		},
		{
			name:    "the Amazon VPC CNI: refused",
			objects: []runtimeObject{&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "aws-node", Namespace: "kube-system"}}},
			status:  CheckFail,
			mention: "Amazon VPC CNI",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &ProvidedProvider{client: fake.NewSimpleClientset(toObjects(tc.objects)...)}
			checks := p.cniChecks(context.Background(), p.client)
			if len(checks) == 0 {
				t.Fatal("no CNI check was produced")
			}
			if checks[0].Status != tc.status {
				t.Errorf("container network check = %s (%s), want %s",
					checks[0].Status, checks[0].Detail, tc.status)
			}
			if !strings.Contains(checks[0].Detail, tc.mention) {
				t.Errorf("detail does not mention %q: %s", tc.mention, checks[0].Detail)
			}
		})
	}
}

// An existing Cilium WITHOUT Gateway API support is fatal, not cosmetic: the
// platform Gateway would never be Programmed and not one platform URL would
// answer. Better to say so before the install than to hand over dead links.
func TestProvidedPreflightFailsWhenTheirCiliumHasNoGatewayAPI(t *testing.T) {
	p := &ProvidedProvider{client: fake.NewSimpleClientset(
		ciliumDaemonSet("kube-system"), ciliumConfig("kube-system", "false"))}

	checks := p.cniChecks(context.Background(), p.client)
	var gateway *Check
	for i := range checks {
		if strings.Contains(checks[i].Name, "Gateway API") {
			gateway = &checks[i]
		}
	}
	if gateway == nil {
		t.Fatal("no Gateway API check was produced for a cluster that already runs Cilium")
	}
	if gateway.Status != CheckFail {
		t.Errorf("Gateway API check = %s, want fail: without it the platform has no data path for any URL", gateway.Status)
	}
	if !strings.Contains(gateway.Fix, "gatewayAPI.enabled=true") {
		t.Errorf("the fix does not name the setting to change: %q", gateway.Fix)
	}
}

// The storage check: the platform changes no storage defaults on a cluster it
// does not own, so a missing default class has to be reported — ~90 of its
// PersistentVolumeClaims name no class.
func TestProvidedPreflightWarnsWithoutADefaultStorageClass(t *testing.T) {
	ctx := context.Background()
	withDefault := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
		Name:        "block",
		Annotations: map[string]string{defaultStorageClassAnnotation: "true"},
	}}
	plain := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "block"}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}

	find := func(checks []Check) Check {
		for _, c := range checks {
			if c.Name == "default StorageClass" {
				return c
			}
		}
		t.Fatal("no default StorageClass check")
		return Check{}
	}

	p := &ProvidedProvider{client: fake.NewSimpleClientset(node, plain)}
	if got := find(p.Preflight(ctx, nil)); got.Status != CheckWarn {
		t.Errorf("no default class: check = %s, want warn", got.Status)
	}
	p = &ProvidedProvider{client: fake.NewSimpleClientset(node, withDefault)}
	if got := find(p.Preflight(ctx, nil)); got.Status != CheckPass {
		t.Errorf("default class present: check = %s (%s), want pass", got.Status, got.Detail)
	}
}

// A cluster with no nodes cannot host the platform, and the preflight is where
// that is cheap to learn.
func TestProvidedPreflightFailsOnAnEmptyCluster(t *testing.T) {
	p := &ProvidedProvider{client: fake.NewSimpleClientset()}
	for _, c := range p.Preflight(context.Background(), nil) {
		if c.Name == "nodes" {
			if c.Status != CheckFail {
				t.Errorf("nodes check on a cluster with no nodes = %s, want fail", c.Status)
			}
			return
		}
	}
	t.Error("no nodes check was produced")
}

// runtimeObject keeps the table above readable; the fake clientset wants
// runtime.Object and the test writes typed API objects.
type runtimeObject = runtime.Object

func toObjects(in []runtimeObject) []runtime.Object {
	out := make([]runtime.Object, 0, len(in))
	out = append(out, in...)
	return out
}
