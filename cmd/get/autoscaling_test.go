package get

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var asNow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func asAgo(d time.Duration) *metav1.Time { t := metav1.NewTime(asNow.Add(-d)); return &t }

func asView() *NodeAutoscalingView {
	return &NodeAutoscalingView{
		Spec: &v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 2, MaxWorkers: 6,
			BatchPool: &v1alpha1.BatchPoolSpec{MinNodes: 1, MaxNodes: 4}},
		Status: &v1alpha1.AutoscalingStatus{
			Enabled: true, Workers: 3, LastScaleUp: asAgo(2 * time.Hour), LastReason: "steady: 3 workers",
			BatchPool: &v1alpha1.BatchPoolStatus{Nodes: 2, LastScaleUp: asAgo(5 * time.Minute),
				UnderutilizedSince: asAgo(90 * time.Second), LastReason: "pool idle"},
		},
	}
}

// The batch pool (status.autoscaling.batchPool) is printed beside the workers, with
// its own node count, bounds, clocks and last decision.
func TestGetStatusPrintsTheBatchPool(t *testing.T) {
	rows := autoscalingRows(asView(), asNow)
	want := [][]string{
		{"workers", "3", "2–6", "2.0h ago", "never", "-", "steady: 3 workers"},
		{"batch (batch pool)", "2", "1–4", "5m ago", "never", "2m ago", "pool idle"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %q, want %q", rows, want)
	}
	for i := range want {
		if strings.Join(rows[i], "|") != strings.Join(want[i], "|") {
			t.Errorf("row %d = %q, want %q", i, rows[i], want[i])
		}
	}
	out := renderAutoscaling(asView(), asNow)
	for _, s := range []string{"GROUP", "batch (batch pool)", "1–4", "pool idle", "workers", "steady: 3 workers"} {
		if !strings.Contains(out, s) {
			t.Errorf("the rendered section lacks %q:\n%s", s, out)
		}
	}
}

func TestGetStatusBatchPoolEdgeCases(t *testing.T) {
	// No pool declared or reported: the workers only.
	v := asView()
	v.Spec.BatchPool, v.Status.BatchPool = nil, nil
	if rows := autoscalingRows(v, asNow); len(rows) != 1 || rows[0][0] != "workers" {
		t.Errorf("without a pool: %q", rows)
	}
	// Declared, not yet decided about.
	v = asView()
	v.Status.BatchPool = nil
	if rows := autoscalingRows(v, asNow); len(rows) != 2 || strings.Join(rows[1], "|") != "batch (batch pool)|-|1–4|never|never|-|no decision yet" {
		t.Errorf("a declared pool with no status: %q", rows)
	}
	// A custom node group name, and the autoscaler off: bounds read "off".
	v = asView()
	v.Spec.Enabled, v.Status.Enabled = false, false
	v.Spec.BatchPool.NodeGroup = "nightly"
	rows := autoscalingRows(v, asNow)
	if rows[0][2] != "off" || rows[1][0] != "nightly (batch pool)" || rows[1][2] != "off" {
		t.Errorf("autoscaler off: %q", rows)
	}
	// Reported but no longer declared: still shown, and said so.
	v = asView()
	v.Spec.BatchPool = nil
	if rows := autoscalingRows(v, asNow); len(rows) != 2 || !strings.HasPrefix(rows[1][6], "no longer declared: ") {
		t.Errorf("a pool only the status remembers: %q", rows)
	}
	// Nothing configured: no section at all.
	if out := renderAutoscaling(nil, asNow); out != "" {
		t.Errorf("no autoscaler renders %q", out)
	}
}

// The section is wired into `adhar get status`: the table output prints it, and
// `-o json|yaml` carries status.autoscaling.batchPool.
func TestGetStatusWiresTheAutoscalingSection(t *testing.T) {
	status := &PlatformStatus{OverallStatus: "Healthy", LastUpdated: asNow, Autoscaling: asView()}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	err = displayStatusTable(status)
	os.Stdout = stdout
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"Node Autoscaling", "batch (batch pool)", "pool idle"} {
		if !strings.Contains(string(out), s) {
			t.Errorf("`adhar get status` does not print %q", s)
		}
	}
	b, _ := json.Marshal(status)
	if !strings.Contains(string(b), `"batchPool":{"nodes":2`) {
		t.Errorf("-o json lacks the batch pool: %s", b)
	}
}

// What `adhar get status` reads is the real object: an AdharPlatform on a real API
// server (envtest, the shipped CRD) carrying the pool in spec and status, read back
// through the same kubeconfig path the command uses.
func TestGetStatusReadsTheBatchPoolOffTheAdharPlatform(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping the envtest API server")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		dirs, _ := filepath.Glob(filepath.Join("..", "..", "bin", "k8s", "*"))
		sort.Strings(dirs)
		for i := len(dirs) - 1; i >= 0 && assets == ""; i-- {
			if _, err := os.Stat(filepath.Join(dirs[i], "kube-apiserver")); err == nil {
				assets = dirs[i]
			}
		}
	}
	if assets == "" {
		t.Fatal("no envtest binaries: set KUBEBUILDER_ASSETS or run `make setup-envtest`")
	}
	env := &envtest.Environment{
		BinaryAssetsDirectory: assets,
		CRDInstallOptions:     envtest.CRDInstallOptions{Paths: []string{filepath.Join("..", "..", "platform", "controllers", "resources", "platform.adhar.io_adharplatforms.yaml")}},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	user, err := env.AddUser(envtest.User{Name: "admin", Groups: []string{"system:masters"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	kc, err := user.KubeConfig()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, kc, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)

	cl, err := getControllerRuntimeClient()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: globals.AdharSystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	view := asView()
	p := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{Name: "adhar", Namespace: globals.AdharSystemNamespace},
		Spec: v1alpha1.AdharPlatformSpec{Autoscaling: view.Spec}}
	if err := cl.Create(ctx, p); err != nil {
		t.Fatalf("creating the AdharPlatform: %v", err)
	}
	p.Status.Autoscaling = view.Status
	if err := cl.Status().Update(ctx, p); err != nil {
		t.Fatalf("writing its status: %v", err)
	}

	status := &PlatformStatus{}
	attachPlatformHealth(status)
	if status.Autoscaling == nil || status.Autoscaling.Status == nil || status.Autoscaling.Status.BatchPool == nil {
		t.Fatalf("`adhar get status` read no batch pool off the AdharPlatform: %+v", status.Autoscaling)
	}
	if got := status.Autoscaling.Status.BatchPool; got.Nodes != 2 || got.LastReason != "pool idle" {
		t.Errorf("batch pool status read back as %+v", got)
	}
	rows := autoscalingRows(status.Autoscaling, asNow)
	if len(rows) != 2 || rows[1][0] != "batch (batch pool)" || rows[1][2] != "1–4" {
		t.Errorf("rows from the live object: %q", rows)
	}
}
