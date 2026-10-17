package down

import (
	"os"
	"strings"
	"testing"
)

// The issued edge certificate is saved BEFORE the cluster is deleted; after
// DeleteCluster there is nothing left to read it from.
func TestTeardownCachesTheEdgeCertificateBeforeDeletingTheCluster(t *testing.T) {
	b, err := os.ReadFile("down.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	save := strings.Index(src, "saveEdgeCertificateBeforeTeardown(ctx, clusterName, detail)")
	del := strings.Index(src, "found.Provider.DeleteCluster(ctx, found.Cluster.ID)")
	if save < 0 || del < 0 || save > del {
		t.Error("down.go does not cache the edge certificate before DeleteCluster; the next cluster will order a new one")
	}
}
