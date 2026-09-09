package provider

import (
	"strings"
	"testing"

	"adhar-io/adhar/globals"
)

// The kubeadm package stream must follow the platform-wide Kubernetes default
// so cloud clusters run the same minor as local Kind.
func TestKubeadmDefaultMinorMatchesGlobals(t *testing.T) {
	want := K8sMinorFromVersion(globals.DefaultKubernetesVersion)
	if KubeadmDefaultK8sMinor != want {
		t.Fatalf("KubeadmDefaultK8sMinor=%q but globals.DefaultKubernetesVersion=%q (minor %q)", KubeadmDefaultK8sMinor, globals.DefaultKubernetesVersion, want)
	}
	if !strings.HasPrefix(KubeadmNodePrepScript(KubeadmDefaultK8sMinor), "#!/bin/bash") {
		t.Fatal("node prep script must be a bash script")
	}
}
