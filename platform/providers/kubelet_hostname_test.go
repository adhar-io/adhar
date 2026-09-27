package provider

import (
	"strings"
	"testing"
)

// The AWS cloud-controller-manager resolves a Node to an EC2 instance by matching
// the node's NAME against the instance's private DNS name, which is a FQDN
// (ip-10-0-1-80.ap-southeast-1.compute.internal). Ubuntu registers the short
// hostname by default (ip-10-0-1-80), and the consequence is total:
//
//	node_controller: failed to get instance metadata for node ip-10-0-1-80:
//	instance not found, requeuing
//
// The CCM then never clears node.cloudprovider.kubernetes.io/uninitialized:NoSchedule,
// so nothing schedules at all — measured on a live Singapore cluster where Argo CD
// and Gitea sat Pending for 20 minutes and `adhar get secrets` had no
// argocd-initial-admin-secret to show, because Argo CD had never started.
//
// The name is fixed when the node joins, so the flag has to be written before
// that; these tests pin the flag construction, which is the part that is testable
// without a cluster.
func TestKubeletFlagsIncludeTheHostnameOverrideWhenGiven(t *testing.T) {
	got := kubeletExtraArgs(true, true, "10.0.1.80", "ip-10-0-1-80.ap-southeast-1.compute.internal")
	for _, want := range []string{
		ExternalCloudProviderFlag,
		"--node-ip=10.0.1.80",
		"--hostname-override=ip-10-0-1-80.ap-southeast-1.compute.internal",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("kubelet args missing %q: %s", want, got)
		}
	}
}

// Every other provider matches nodes by short hostname (Azure by VM name, GCP by
// instance name), so an override must not appear unless one is asked for.
func TestKubeletFlagsOmitTheOverrideWhenNotGiven(t *testing.T) {
	if got := kubeletExtraArgs(true, false, "10.0.1.80"); strings.Contains(got, "hostname-override") {
		t.Errorf("no override was requested, yet one was written: %s", got)
	}
	if got := kubeletExtraArgs(true, false, "10.0.1.80", ""); strings.Contains(got, "hostname-override") {
		t.Errorf("an empty override must be ignored, not written: %s", got)
	}
	if got := kubeletExtraArgs(true, false, "10.0.1.80", "   "); strings.Contains(got, "hostname-override") {
		t.Errorf("a whitespace override must be ignored: %s", got)
	}
}

func TestKubeletFlagsAreEmptyWhenNothingApplies(t *testing.T) {
	if got := kubeletExtraArgs(false, false, ""); got != "" {
		t.Errorf("want no flags, got %q", got)
	}
}

// kubeadm derives the node name from the machine's hostname, so an overridden
// kubelet hostname has to reach kubeadm too. It did not, and `kubeadm init` died
// at the very last phase:
//
//	error execution phase mark-control-plane: nodes "ip-10-0-1-164" not found
//
// The kubelet had registered ip-10-0-1-164.ap-southeast-1.compute.internal while
// kubeadm looked for the short name. Both sides must agree.
func TestNodeNameArgIsTrimmedAndOptional(t *testing.T) {
	if got := nodeNameArg(); got != "" {
		t.Errorf("no argument should yield %q, got %q", "", got)
	}
	if got := nodeNameArg(""); got != "" {
		t.Errorf("an empty name must not become a flag, got %q", got)
	}
	if got := nodeNameArg("  "); got != "" {
		t.Errorf("whitespace must not become a flag, got %q", got)
	}
	want := "ip-10-0-1-164.ap-southeast-1.compute.internal"
	if got := nodeNameArg("  " + want + " "); got != want {
		t.Errorf("nodeNameArg = %q, want %q", got, want)
	}
}
