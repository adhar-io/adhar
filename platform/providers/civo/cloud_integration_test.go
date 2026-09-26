package civo

import (
	"strings"
	"testing"
)

func TestCloudIntegrationStepsInstallCivoCCMAndCSI(t *testing.T) {
	p := &Provider{config: &Config{Token: "tok", Region: "LON1"}}
	joined := ""
	for _, s := range p.cloudIntegrationSteps("dev", "adhar-cluster-dev") {
		joined += s.Cmd + "\n"
	}
	for _, want := range []string{
		"create secret generic civo-api-access", "--from-literal='api-key=tok'", "--from-literal='region=LON1'", "--from-literal='cluster-id=adhar-cluster-dev'",
		"apply -f '" + civoCCMManifestURL + "'", "apply -k '" + civoCSIRef + "'",
		"get daemonset civo-csi-node", "node.adhar.io/csi-not-ready",
		// civo-volume is DEMOTED, not made the default: a Civo instance takes a
		// small number of attached block volumes and the platform asks for ~90
		// claims, so node-local storage is the default and the block class is
		// there for whatever pins it.
		"patch storageclass 'civo-volume'", `"storageclass.kubernetes.io/is-default-class":"false"`,
		"provisioner: rancher.io/local-path",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Civo integration lacks %q", want)
		}
	}
	if strings.Contains(joined, `patch storageclass 'civo-volume' -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'`) {
		t.Error("civo-volume must not be re-marked as the default")
	}
}
