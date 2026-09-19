package gcp

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
)

// Cloud integration for a self-managed (kubeadm on Compute Engine) cluster:
// cloud-provider-gcp's controller manager, the Persistent Disk CSI driver, a
// default pd-balanced StorageClass and the CSI-startup-taint toleration.
// Neither ships as a Helm chart, so these are pinned manifests / remote
// kustomizations (git is installed on the control plane for the latter).
const (
	gcpCCMManifestURL = "https://raw.githubusercontent.com/kubernetes/cloud-provider-gcp/master/deploy/packages/default/manifest.yaml"
	// kubernetesServiceIP is the in-cluster address of the API server: the first
	// address of kubeadm's default service CIDR (10.96.0.0/12), and one the
	// apiserver's serving certificate always covers.
	kubernetesServiceIP = "10.96.0.1"

	// gcpCloudConfigPath is where the upstream CCM manifest already mounts a
	// cloud provider config from the host, so this is the path to write.
	gcpCloudConfigPath = "/etc/kubernetes/cloud.config"

	gcpPDCSIRef = "github.com/kubernetes-sigs/gcp-compute-persistent-disk-csi-driver/deploy/kubernetes/overlays/stable-master?ref=v1.15.0"
)

// serviceAccountKeyJSON returns the service-account key the PD CSI driver
// authenticates with (kube-system/cloud-sa), from the provider config.
func (p *Provider) serviceAccountKeyJSON() (string, error) {
	if p.config.ServiceAccountKey != "" {
		return p.config.ServiceAccountKey, nil
	}
	if p.config.ServiceAccountKeyPath != "" {
		b, err := os.ReadFile(p.config.ServiceAccountKeyPath)
		if err != nil {
			return "", fmt.Errorf("reading service account key %s: %w", p.config.ServiceAccountKeyPath, err)
		}
		return string(b), nil
	}
	return "", nil
}

// cloudConfigStep writes the GCE provider configuration the
// cloud-controller-manager reads. The values cannot be inferred: the provider
// needs the node network tag to build the load balancer's firewall rule, and it
// needs the network and subnetwork by name because the cluster does not use the
// project's default VPC.
func (p *Provider) cloudConfigStep(clusterName string) provider.IntegrationStep {
	subnet := p.config.SubnetName
	if subnet == "" {
		subnet = clusterName + "-subnet"
	}
	network := p.config.VPCName
	if network == "" {
		network = clusterName + "-network"
	}
	// token-url = nil makes the provider use the instance's own service account
	// through the metadata server, which is why nodes are given one.
	body := fmt.Sprintf(`[global]
token-url = nil
project-id = %s
network-name = %s
subnetwork-name = %s
node-tags = %s
node-instance-prefix = %s-
local-zone = %s
`, p.config.ProjectID, network, subnet, instanceTag(clusterName), clusterName, p.config.Zone)

	return provider.IntegrationStep{
		Desc: "GCE cloud provider configuration",
		Cmd:  fmt.Sprintf("cat > %s <<'ADHAR_CLOUD_CONFIG'\n%sADHAR_CLOUD_CONFIG\nchmod 0644 %s", gcpCloudConfigPath, body, gcpCloudConfigPath),
	}
}

// persistentVolumeDiskType is the disk type for PersistentVolumes. It mirrors
// the node disk type so both draw on the same GCP quota, and defaults to
// pd-balanced when nothing is configured (the better choice when SSD quota
// allows it).
func (p *Provider) persistentVolumeDiskType() string {
	switch p.config.DiskType {
	case "":
		return "pd-balanced"
	default:
		return p.config.DiskType
	}
}

// ccmRBACStep grants the cloud-controller-manager the permissions the upstream
// manifest omits. With --use-service-account-credentials each controller acts as
// its own service account, and the service controller could not write back the
// address it had allocated: "services ... is forbidden: User
// system:serviceaccount:kube-system:cloud-provider cannot patch resource
// services/status". The load balancer stayed <pending> forever as a result.
func (p *Provider) ccmRBACStep() provider.IntegrationStep {
	const manifest = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: adhar-cloud-provider-extra
rules:
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: [""]
    resources: ["services/status"]
    verbs: ["get", "update", "patch"]
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: [""]
    resources: ["nodes/status"]
    verbs: ["get", "update", "patch"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: adhar-cloud-provider-extra
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: adhar-cloud-provider-extra
subjects:
  - kind: ServiceAccount
    name: cloud-provider
    namespace: kube-system
  - kind: ServiceAccount
    name: cloud-controller-manager
    namespace: kube-system
`
	return provider.StepManifest("cloud-controller-manager RBAC", manifest)
}

// ccmStep installs the GCE cloud-controller-manager.
//
// The upstream manifest CANNOT be applied as published. It ships
// `args: [] # args must be replaced by tooling` — it is a template for their own
// packaging — so applying it raw started the binary with no flags at all. It
// crash-looped with `"cloud-controller-manager" does not take any arguments`,
// and because nothing then set node providerIDs or watched Services, the
// Gateway's LoadBalancer stayed <pending> and the platform was unreachable.
//
// The flags below are what a kubeadm cluster running Cilium needs:
//   - allocate-node-cidrs / configure-cloud-routes are OFF because Cilium owns
//     pod IPAM and routing; leaving them on makes the CCM fight the CNI.
//   - use-service-account-credentials lets each controller use its own token.
//   - cluster-name namespaces the load balancer resources it creates, so two
//     clusters in one project do not collide.
func (p *Provider) ccmStep() provider.IntegrationStep {
	args := []string{
		"--cloud-provider=gce",
		"--use-service-account-credentials=true",
		// node-ipam is DISABLED, not merely told not to allocate: setting
		// --allocate-node-cidrs=false makes that controller refuse to start and
		// the whole manager exits with "the AllocateNodeCIDRs is not enabled".
		// Cilium owns pod IPAM, so the controller has no work here either way.
		"--controllers=*,-node-ipam-controller",
		"--configure-cloud-routes=false",
		// Without this the GCE provider has no node tags and refuses to create
		// the load balancer's firewall rule: "no node tags supplied and also
		// failed to parse the given lists of hosts for tags. Abort creating
		// firewall rule". The upstream manifest already mounts this path from the
		// host, so writing the file is all that is needed.
		"--cloud-config=" + gcpCloudConfigPath,
		"--leader-elect=true",
		"--v=2",
	}
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, fmt.Sprintf("%q", a))
	}
	replacement := fmt.Sprintf("args: [%s]", strings.Join(quoted, ", "))

	// sed over the fetched manifest rather than a kubectl patch afterwards: the
	// DaemonSet must never run once with empty args, or it crash-loops and the
	// rollout has to be waited out twice.
	// The manifest also hardcodes KUBERNETES_SERVICE_HOST=127.0.0.1, a GKE
	// assumption where the control plane is fronted locally. On kubeadm that
	// fails twice over: the apiserver listens on 6443 rather than 443, and its
	// serving certificate does not include 127.0.0.1 at all ("certificate is
	// valid for 10.96.0.1, <node IP>, <public IP>, not 127.0.0.1"). Pointing at
	// the in-cluster Service address satisfies both, since the ClusterIP is in
	// the cert by default.
	//
	// Raw string: the sed expressions are full of backslashes that Go would
	// otherwise try to interpret as escapes.
	const sedTemplate = `curl -fsSL %s ` +
		`| sed -e 's|^\( *\)args: \[\].*$|\1%s|' ` +
		`-e 's|^\( *\)value: "127.0.0.1"$|\1value: "%s"|' ` +
		`| %s apply -f -`
	cmd := fmt.Sprintf(sedTemplate,
		provider.ShellQuote(gcpCCMManifestURL), replacement, kubernetesServiceIP, provider.KubectlAdmin)
	return provider.IntegrationStep{Desc: "GCP cloud-controller-manager", Cmd: cmd}
}

func (p *Provider) cloudIntegrationSteps(clusterName string) ([]provider.IntegrationStep, error) {
	steps := []provider.IntegrationStep{
		provider.StepEnsureGit(),
		// The config must exist BEFORE the CCM starts reading it.
		p.cloudConfigStep(clusterName),
		p.ccmRBACStep(),
		p.ccmStep(),
	}
	// The PD CSI controller needs a key; with application-default credentials
	// (the instances' own service account) the secret is left out and the
	// driver uses the metadata server.
	if key, err := p.serviceAccountKeyJSON(); err != nil {
		return nil, err
	} else if key != "" {
		steps = append(steps, provider.StepSecret("GCP service-account key for the PD CSI driver", "gce-pd-csi-driver", "cloud-sa", map[string]string{"cloud-sa.json": key}))
	}
	steps = append(steps,
		provider.StepApplyKustomize("Persistent Disk CSI driver", gcpPDCSIRef),
		provider.StepWaitDaemonSet("gce-pd-csi-driver", "csi-gce-pd-node"),
		provider.StepTolerateCSIStartupTaint("gce-pd-csi-driver", "csi-gce-pd-node"),
		// The PersistentVolume disk type follows the NODE disk type rather than
		// being hardcoded to pd-balanced. They draw on different quotas —
		// pd-balanced and pd-ssd against SSD_TOTAL_GB (250 GB by default),
		// pd-standard against DISKS_TOTAL_GB (2 TB) — so a cluster whose nodes
		// were put on pd-standard to fit the SSD quota still created every
		// PersistentVolume as pd-balanced, exhausted that 250 GB after ~35
		// volumes, and left every remaining stateful app Pending on
		// "binding volumes: context deadline exceeded" with nothing naming quota.
		provider.StepDefaultStorageClass("adhar-block", "pd.csi.storage.gke.io", map[string]string{"type": p.persistentVolumeDiskType()}),
	)
	return steps, nil
}

func (p *Provider) installCloudIntegration(signer ssh.Signer, masterIP, clusterName string) error {
	steps, err := p.cloudIntegrationSteps(clusterName)
	if err != nil {
		return fmt.Errorf("GCP cloud integration: %w", err)
	}
	if err := provider.ApplyCloudIntegration(signer, gcpSSHUser, masterIP, steps); err != nil {
		return fmt.Errorf("GCP cloud integration: %w", err)
	}
	return nil
}
