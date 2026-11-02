package down

import (
	"testing"

	pfactory "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/providers/aws"
	"adhar-io/adhar/platform/providers/azure"
	"adhar-io/adhar/platform/providers/civo"
	"adhar-io/adhar/platform/providers/digitalocean"
	"adhar-io/adhar/platform/providers/gcp"
)

// The volume step reaches a provider through a type assertion, so a cloud that
// drops the capability is not a compile error here — it is a teardown that
// silently stops finding volumes, which is how they used to leak. Every
// provider that builds a cluster must answer.
func TestEveryCloudProviderCanFindAndDeleteItsClustersVolumes(t *testing.T) {
	for name, p := range map[string]any{
		"aws":          &aws.Provider{},
		"azure":        &azure.Provider{},
		"gcp":          &gcp.Provider{},
		"digitalocean": &digitalocean.Provider{},
		"civo":         &civo.Provider{},
	} {
		if _, ok := p.(pfactory.ClusterVolumeManager); !ok {
			t.Errorf("%s does not implement providers.ClusterVolumeManager, so `adhar down` cannot find or delete "+
				"the block volumes its clusters leave behind", name)
		}
	}
}
