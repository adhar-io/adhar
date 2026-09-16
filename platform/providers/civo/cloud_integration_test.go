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
		"get daemonset civo-csi-node", "node.adhar.io/csi-not-ready", "patch storageclass civo-volume",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Civo integration lacks %q", want)
		}
	}
}
