package civo

import (
	"fmt"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"

	"adhar-io/adhar/globals"
)

// Cloud integration for a self-managed (kubeadm on Civo instances) cluster:
// the Civo cloud-controller-manager, the Civo CSI driver (its manifest ships
// the `civo-volume` StorageClass), the default-class mark and the
// CSI-startup-taint toleration. Both read the API key from
// kube-system/civo-api-access. Pinned; bump with a bring-up.
// Both refs were stale and BOTH 404ed on the first live Civo bring-up
// (2026-10-02), which is what "built and unit-tested but never run" looks like:
// the CCM was pinned to v0.0.24 when upstream's tags are v0.1.x, and its manifest
// had also moved from manifest/cloud-controller-manager.yaml to
// doc/yaml/ccm-install.yaml; the CSI was pinned to v0.2.0 when upstream is on
// v0.10.x. The resource names the steps below wait on — StorageClass civo-volume,
// DaemonSet civo-csi-node — were re-checked at these tags and are unchanged.
//
// Verify a bump by fetching the URL and diffing the names, not by assuming the
// path survived: a 404 here fails the create AFTER the instances exist and bill.
const (
	civoCCMManifestURL = "https://raw.githubusercontent.com/civo/civo-cloud-controller-manager/v0.1.9/doc/yaml/ccm-install.yaml"
	civoCSIRef         = "github.com/civo/civo-csi/deploy/kubernetes?ref=v0.10.7"
	civoAPIURL         = "https://api.civo.com"

	// The Civo CSI kustomization declares a VolumeSnapshotClass but does NOT ship
	// the CRDs that define one, so applying it on a fresh cluster fails with
	//
	//   no matches for kind "VolumeSnapshotClass" in version
	//   "snapshot.storage.k8s.io/v1" (ensure CRDs are installed first)
	//
	// and takes the whole create down after the instances exist. The CRDs belong to
	// external-snapshotter, which every CSI driver expects the cluster to provide;
	// DigitalOcean's integration installs its own equivalent "CSI CRDs" step for
	// the same reason. Applied BEFORE the CSI step below.
	csiSnapshotCRDBase = "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v8.2.0/client/config/crd"
)

// csiSnapshotCRDs are the three CRDs a CSI driver's snapshot support needs.
var csiSnapshotCRDs = []string{
	"snapshot.storage.k8s.io_volumesnapshotclasses.yaml",
	"snapshot.storage.k8s.io_volumesnapshotcontents.yaml",
	"snapshot.storage.k8s.io_volumesnapshots.yaml",
}

func (p *Provider) cloudIntegrationSteps(clusterName, clusterID string) []provider.IntegrationStep {
	return append([]provider.IntegrationStep{
		provider.StepEnsureGit(),
		provider.StepSecret("Civo API access for CCM/CSI", "kube-system", "civo-api-access", map[string]string{
			// p.token, NOT p.config.Token: see the Provider.token comment — the
			// latter is empty whenever the token came from the environment or a file.
			"api-key":    p.token,
			"api-url":    civoAPIURL,
			"region":     p.config.Region,
			"cluster-id": clusterID,
			"namespace":  "kube-system",
		}),
		// NO cloud-controller-manager, and no Civo block-storage CSI, in compute
		// mode. Both resolve CIVO_CLUSTER_ID against Civo's MANAGED Kubernetes API,
		// and a self-managed kubeadm cluster on plain instances has no such object:
		// the CCM nil-panics on "Unable to get kubernetes cluster" and the CSI
		// plugin never opens its socket (verified live on mum1, 2026-10-02).
		//
		// Installing them anyway was actively harmful rather than merely useless —
		// see civoComputeHasExternalCCM in compute.go for why an absent CCM leaves
		// every node permanently unschedulable.
		//
		// The platform does not need either: node-local storage is already the
		// default StorageClass on every self-managed cloud, and the Gateway is
		// reachable on its node ports. `clusterMode: k3s` is the path where Civo's
		// CCM and cloud LoadBalancers work.
	}, provider.StepNodeLocalStorageClass(globals.DefaultStorageClass)...)
}

func (p *Provider) installCloudIntegration(signer ssh.Signer, masterIP, clusterName, clusterID string) error {
	if err := provider.ApplyCloudIntegration(signer, computeSSHUser, masterIP, p.cloudIntegrationSteps(clusterName, clusterID)); err != nil {
		return fmt.Errorf("Civo cloud integration: %w", err)
	}
	return nil
}
