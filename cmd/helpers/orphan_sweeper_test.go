package helpers_test

import (
	"testing"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/providers/azure"
	"adhar-io/adhar/platform/providers/digitalocean"
	"adhar-io/adhar/platform/providers/gcp"
)

// Every provider whose CSI driver leaves volumes behind must be able to remove
// them WITHOUT a live cluster.
//
// The teardown prints "they are kept, remove them with
// `adhar down ... --purge-orphaned-volumes`", and that advice was impossible to
// act on: the purge only ran inside cluster deletion, so by the time an operator
// read the warning the cluster was gone, the lookup found nothing, and the flag
// was a no-op. 74 GCP disks (771 GB, ~$31/month) sat billing behind it
// (2026-09-26). This test is what keeps the printed remedy true.
func TestProvidersThatLeakVolumesCanSweepThemWithoutACluster(t *testing.T) {
	cases := []struct {
		name     string
		provider any
	}{
		{"gcp", &gcp.Provider{}},
		{"digitalocean", &digitalocean.Provider{}},
		{"azure", &azure.Provider{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.provider.(helpers.OrphanVolumeSweeper); !ok {
				t.Fatalf("%s does not implement helpers.OrphanVolumeSweeper, so "+
					"`adhar down --purge-orphaned-volumes` cannot clear its leaked "+
					"volumes once the cluster is gone", tc.name)
			}
		})
	}
}
