package up

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
)

func installIDEnv() *config.ResolvedEnvironmentConfig {
	return &config.ResolvedEnvironmentConfig{
		Name: "dev", ResolvedProvider: dnsDigitalOcean, ResolvedRegion: testRegion,
		ProviderConfig: &config.ConfigProviderConfig{Type: dnsDigitalOcean, Region: testRegion},
	}
}

func readInstallID(t *testing.T, c client.Client) string {
	t.Helper()
	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace}, cm); err != nil {
		t.Fatal(err)
	}
	return cm.Data[globals.ClusterSpecInstallIDKey]
}

// adhar-cluster-spec carries the install id the CLI recorded when it created
// the cluster, so the id is in the cluster as well as on the machine.
func TestTheClusterSpecRecordsTheInstallID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst, err := pfactory.RecordClusterInstall(dnsDigitalOcean, testRegion, "adhar")
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	if err := ensureClusterSpecConfigMap(context.Background(), c, installIDEnv(), "adhar", nil); err != nil {
		t.Fatal(err)
	}
	if got := readInstallID(t, c); got != inst.ID {
		t.Fatalf("adhar-cluster-spec installID = %q, want %q", got, inst.ID)
	}
}

// The cluster's own id wins, and this machine adopts it: a cluster created from
// another laptop tags its volumes with the id IT was given, and overwriting
// that with a stale local record would point every later teardown at volumes
// that do not exist.
func TestTheClustersOwnInstallIDWinsAndIsAdopted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stale, err := pfactory.RecordClusterInstall(dnsDigitalOcean, testRegion, "adhar")
	if err != nil {
		t.Fatal(err)
	}
	const clusters = "kkkkkkkkkkkkkkk1"
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace},
		Data:       map[string]string{globals.ClusterSpecInstallIDKey: clusters},
	}).Build()
	if err := ensureClusterSpecConfigMap(context.Background(), c, installIDEnv(), "adhar", nil); err != nil {
		t.Fatal(err)
	}
	if got := readInstallID(t, c); got != clusters {
		t.Fatalf("adhar-cluster-spec installID = %q; the cluster's own %q must not be overwritten", got, clusters)
	}
	active, _ := pfactory.ActiveClusterInstall(dnsDigitalOcean, testRegion, "adhar")
	if active == nil || active.ID != clusters {
		t.Fatalf("this machine did not adopt the cluster's id: %+v", active)
	}
	all, _ := pfactory.LoadClusterInstalls(dnsDigitalOcean, testRegion, "adhar")
	if len(all) != 2 || all[0].ID != stale.ID || all[0].Active() {
		t.Fatalf("the stale local record was dropped instead of kept as superseded: %+v", all)
	}
	if rec, adopt := resolveClusterInstallID("garbage value", "aaaaaaaaaaaaaaaa"); rec != "aaaaaaaaaaaaaaaa" || adopt != "" {
		t.Errorf("a malformed in-cluster value propagated: %q %q", rec, adopt)
	}
}
