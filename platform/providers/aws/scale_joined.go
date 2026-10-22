package aws

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
)

// awsWorkerVMs is one node group's instances (keyed by Name tag) as a scale
// decision sees them. A node on AWS is named by the instance's private DNS
// name (ip-10-…), never its Name tag, so the planner can only find it by
// address: the private IP the kubelet registers with is always included. An
// instance with no LaunchTime is Young: never discard what you cannot date.
func awsWorkerVMs(prefix string, members map[string]NodeInfo, now time.Time) []provider.WorkerVM {
	names := make([]string, 0, len(members))
	for name := range members {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	vms := make([]provider.WorkerVM, 0, len(names))
	for _, name := range names {
		n := members[name]
		var addrs []string
		for _, a := range []string{n.PrivateIP, n.PublicIP, n.PrivateDNSName} {
			if a != "" {
				addrs = append(addrs, a)
			}
		}
		vms = append(vms, provider.WorkerVM{
			Name:  name,
			Addrs: addrs,
			Young: n.CreatedAt.IsZero() || now.Sub(n.CreatedAt) < provider.OrphanGrace,
		})
	}
	return vms
}

// awsNodeName is the Kubernetes node name of a worker: its private DNS name
// (what KubeadmJoinWorker registers it as), or the Name tag when unknown.
func awsNodeName(tagName string, n NodeInfo) string {
	if n.PrivateDNSName != "" {
		return n.PrivateDNSName
	}
	return tagName
}

// prepareAndJoinAWSWorker takes a fresh instance through node prep, the
// external cloud-provider kubelet flags and kubeadm join, registering it under
// its private DNS name.
//
// The EBS CSI driver is a platform addon on AWS, so a worker carries the CSI
// startup taint until its CSINode registers; no cloud-controller-manager is
// installed yet (docs/PROVIDERS.md). externalCCM MUST be true, exactly as it is
// for a worker created by `adhar up` (provider_cluster.go). It was false, and
// the whole chain downstream of it failed silently:
//
//	no --cloud-provider=external  ->  kubelet registers with no
//	uninitialized taint, so the CCM never touches the node  ->
//	.spec.providerID stays empty  ->  the EBS CSI node plugin cannot
//	identify its instance ("node providerID empty, cannot parse", after
//	IMDS also times out on the pod network) and crash-loops  ->  no
//	CSINode object is published  ->  the autoscaler never lifts
//	node.adhar.io/csi-not-ready:NoSchedule.
//
// The visible result is an autoscaled node that is Ready and completely EMPTY
// while pods stay Pending: measured at 5 nodes with two sitting at 3% CPU and
// 27 pods unschedulable.
func prepareAndJoinAWSWorker(ctx context.Context, signer ssh.Signer, node *NodeInfo, joinCmd string) error {
	if err := provider.WaitForNodePrep(ctx, signer, awsSSHUser, node.PublicIP, 15*time.Minute); err != nil {
		return fmt.Errorf("not ready: %w", err)
	}
	if err := provider.EnableExternalCloudProvider(signer, awsSSHUser, node.PublicIP, node.PrivateIP, true, true, node.PrivateDNSName); err != nil {
		return err
	}
	return provider.KubeadmJoinWorker(signer, awsSSHUser, node.PublicIP, joinCmd, node.PrivateDNSName)
}
