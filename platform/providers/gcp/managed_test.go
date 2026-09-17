package gcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/api/container/v1"

	"adhar-io/adhar/platform/types"
)

func TestGKETaintEffectSpelling(t *testing.T) {
	for in, want := range map[string]string{"NoSchedule": "NO_SCHEDULE", "NoExecute": "NO_EXECUTE", "PreferNoSchedule": "PREFER_NO_SCHEDULE", "NO_EXECUTE": "NO_EXECUTE", "": "NO_SCHEDULE"} {
		if got := gkeTaintEffect(in); got != want {
			t.Errorf("gkeTaintEffect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGKENodePoolDefaultsAndAutoscaling(t *testing.T) {
	p := &Provider{config: &Config{MachineType: "n2-standard-4", DiskSize: 200, DiskType: "pd-ssd"}}
	pool := p.gkeNodePool(&types.NodeGroupSpec{Name: "workers"})
	if pool.InitialNodeCount != gkeDefaultNodeCount || pool.Config.MachineType != "n2-standard-4" || pool.Config.DiskSizeGb != 200 || pool.Config.DiskType != "pd-ssd" || pool.Autoscaling != nil {
		t.Errorf("unexpected default pool %+v / %+v", pool, pool.Config)
	}
	if len(pool.Config.OauthScopes) != 1 || pool.Config.OauthScopes[0] != gkeCloudPlatformScope {
		t.Errorf("nodes need the cloud-platform scope: %v", pool.Config.OauthScopes)
	}
	gpu := p.gkeNodePool(&types.NodeGroupSpec{Name: "gpu", Replicas: 3, InstanceType: "a2-highgpu-1g", Labels: map[string]string{"gpu": "true"},
		Taints: []types.TaintSpec{{Key: "gpu", Value: "true", Effect: "NoExecute"}}, AutoScaling: types.AutoScalingSpec{MinReplicas: 2, MaxReplicas: 9}})
	if gpu.InitialNodeCount != 3 || gpu.Config.MachineType != "a2-highgpu-1g" || gpu.Config.Labels["gpu"] != "true" {
		t.Errorf("unexpected gpu pool %+v", gpu.Config)
	}
	if len(gpu.Config.Taints) != 1 || gpu.Config.Taints[0].Effect != "NO_EXECUTE" {
		t.Errorf("unexpected taints %+v", gpu.Config.Taints)
	}
	if gpu.Autoscaling == nil || !gpu.Autoscaling.Enabled || gpu.Autoscaling.MinNodeCount != 2 || gpu.Autoscaling.MaxNodeCount != 9 {
		t.Errorf("unexpected autoscaling %+v", gpu.Autoscaling)
	}
	bare := (&Provider{config: &Config{}}).gkeNodePool(&types.NodeGroupSpec{Name: "w"})
	if bare.Config.MachineType != gkeDefaultMachineType || bare.Config.DiskSizeGb != gkeDefaultDiskSizeGB {
		t.Errorf("no configured machine type/disk falls back to defaults: %+v", bare.Config)
	}
}

func TestGKEToClusterMapsStatusVersionAndEndpoint(t *testing.T) {
	p := &Provider{config: &Config{ProjectID: "proj", Region: "europe-west1"}}
	for status, want := range map[string]types.ClusterStatus{"RUNNING": types.ClusterStatusRunning, "PROVISIONING": types.ClusterStatusCreating, "RECONCILING": types.ClusterStatusUpdating, "STOPPING": types.ClusterStatusDeleting, "ERROR": types.ClusterStatusError, "DEGRADED": types.ClusterStatusError, "STATUS_UNSPECIFIED": types.ClusterStatusUnknown} {
		c := p.gkeToCluster("gcp/proj/dev", &container.Cluster{Name: "dev", Status: status, CurrentMasterVersion: "1.33.2-gke.100", Endpoint: "34.1.2.3", CreateTime: "2026-09-17T00:00:00Z", Location: "europe-west1-b", ResourceLabels: map[string]string{"managed-by": "adhar"}})
		if c.Status != want || c.Version != "v1.33.2-gke.100" || c.Endpoint != "https://34.1.2.3" || c.CreatedAt.Year() != 2026 || c.Metadata["zone"] != "europe-west1-b" || c.Metadata["mode"] != clusterModeGKE {
			t.Errorf("%s: unexpected cluster %+v", status, c)
		}
	}
	if c := p.gkeToCluster("gcp/proj/dev", &container.Cluster{Name: "dev"}); c.Endpoint != "" || c.Version != "" {
		t.Errorf("no endpoint/version yet must stay empty: %+v", c)
	}
}

func TestGKEPoolToNodeGroupMapsStatus(t *testing.T) {
	for status, want := range map[string]types.NodeGroupStatus{"RUNNING": types.NodeGroupStatusReady, "PROVISIONING": types.NodeGroupStatusCreating, "RECONCILING": types.NodeGroupStatusScaling, "STOPPING": types.NodeGroupStatusDeleting, "ERROR": types.NodeGroupStatusError, "RUNNING_WITH_ERROR": types.NodeGroupStatusError} {
		g := gkePoolToNodeGroup(&container.NodePool{Name: "workers", InitialNodeCount: 3, Status: status, Config: &container.NodeConfig{MachineType: "e2-standard-4", Labels: map[string]string{"a": "b"}}})
		if g.Status != want || g.Replicas != 3 || g.InstanceType != "e2-standard-4" || g.Labels["a"] != "b" {
			t.Errorf("%s: unexpected node group %+v", status, g)
		}
	}
	if g := gkePoolToNodeGroup(&container.NodePool{Name: "bare"}); g.InstanceType != "" {
		t.Errorf("nil config must not panic: %+v", g)
	}
}

func TestIsManagedClusterReadsTheTrackerMode(t *testing.T) {
	p := &Provider{config: &Config{ProjectID: "proj", Zone: "z"}, resourceTrackers: map[string]*ResourceTracker{
		"gcp/proj/gke": {Mode: clusterModeGKE}, "gcp/proj/vm": {},
	}}
	if !p.isManagedCluster(context.Background(), "gcp/proj/gke") || p.isManagedCluster(context.Background(), "gcp/proj/vm") || p.isManagedCluster(context.Background(), "gcp/proj/none") {
		t.Error("managed-ness follows the tracker's recorded mode; unknown clusters without a client are not managed")
	}
}

func TestServiceAccountKeyJSONSources(t *testing.T) {
	if key, err := (&Provider{config: &Config{ServiceAccountKey: `{"type":"service_account"}`}}).serviceAccountKeyJSON(); err != nil || key != `{"type":"service_account"}` {
		t.Errorf("inline key: %q %v", key, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sa.json")
	if err := os.WriteFile(path, []byte(`{"from":"file"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if key, err := (&Provider{config: &Config{ServiceAccountKeyPath: path}}).serviceAccountKeyJSON(); err != nil || key != `{"from":"file"}` {
		t.Errorf("file key: %q %v", key, err)
	}
	if _, err := (&Provider{config: &Config{ServiceAccountKeyPath: filepath.Join(dir, "missing.json")}}).serviceAccountKeyJSON(); err == nil {
		t.Error("a missing key file is an error, not silently no key")
	}
	if key, err := (&Provider{config: &Config{UseApplicationDefault: true}}).serviceAccountKeyJSON(); err != nil || key != "" {
		t.Errorf("ADC means no key: %q %v", key, err)
	}
}
