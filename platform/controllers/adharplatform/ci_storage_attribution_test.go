package adharplatform

import (
	"context"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
	provider "adhar-io/adhar/platform/providers"
)

// adhar-ci is a copy of adhar-block, and its volumes must carry the cluster's
// install id exactly as adhar-block's do: the CI workspace PVCs were five of the
// six EBS volumes the 2026-10-31 AWS teardown left behind with nothing saying
// whose they were. The id travels as a StorageClass PARAMETER (EBS
// tagSpecification_N, the GCP `labels`, the Azure `tags`), so the copy must keep
// the parameters, not just the provisioner.
func TestTheCIStorageClassCarriesTheBlockClassesVolumeAttribution(t *testing.T) {
	ctx := context.Background()
	s := storageScheme(t)
	for provisioner, params := range map[string]map[string]string{
		"ebs.csi.aws.com":       {"type": "gp3", "tagSpecification_1": provider.ClusterIDTag + "=a1b2c3d4e5f6g7h8"},
		"pd.csi.storage.gke.io": {"type": "pd-balanced", "labels": provider.ClusterIDLabel + "=a1b2c3d4e5f6g7h8"},
		"disk.csi.azure.com":    {"skuName": "StandardSSD_LRS", "tags": provider.ClusterIDLabel + "=a1b2c3d4e5f6g7h8"},
	} {
		block := class(globals.BlockStorageClass, provisioner, false)
		block.Parameters = params
		r := &AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
			class(globals.DefaultStorageClass, "rancher.io/local-path", true), block,
		).Build(), Scheme: s}
		if err := r.reconcileCIStorageClass(ctx); err != nil {
			t.Fatal(err)
		}
		got := &storagev1.StorageClass{}
		if err := r.Get(ctx, client.ObjectKey{Name: globals.CIStorageClass}, got); err != nil {
			t.Fatal(err)
		}
		for k, v := range params {
			if got.Parameters[k] != v {
				t.Errorf("%s: adhar-ci parameter %s = %q, want %q — its volumes would carry no install id", provisioner, k, got.Parameters[k], v)
			}
		}
	}
}
