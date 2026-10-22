package civo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/civo/civogo"
	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
)

// civoWorkerAddrs are the addresses a node registered from this instance may
// carry.
func civoWorkerAddrs(inst civogo.Instance) []string {
	var addrs []string
	for _, a := range []string{inst.PrivateIP, inst.PublicIP} {
		if a != "" {
			addrs = append(addrs, a)
		}
	}
	return addrs
}

// civoWorkerYoung reports whether an instance is too young to judge. An
// instance with no creation time cannot be dated, and what cannot be dated is
// never discarded.
func civoWorkerYoung(inst civogo.Instance, now time.Time) bool {
	return inst.CreatedAt.IsZero() || now.Sub(inst.CreatedAt) < provider.OrphanGrace
}

// civoWorkerVMs is one node group's worker instances as a scale decision sees
// them. Instances of another group (another hostname prefix) are not members
// and must neither count nor be discarded here.
func civoWorkerVMs(prefix string, workers []civogo.Instance, now time.Time) []provider.WorkerVM {
	var vms []provider.WorkerVM
	for _, inst := range workers {
		if !strings.HasPrefix(inst.Hostname, prefix) {
			continue
		}
		vms = append(vms, provider.WorkerVM{Name: inst.Hostname, Addrs: civoWorkerAddrs(inst), Young: civoWorkerYoung(inst, now)})
	}
	return vms
}

// civoInstancesToDiscard resolves the planner's discard NAMES to instances.
// Names are hostnames and need not be unique, so a name alone could resolve
// to a live node's instance: only an instance that is past the grace period
// and whose addresses no node carries is ever returned.
func civoInstancesToDiscard(names []string, workers []civogo.Instance, joined provider.JoinedNodes, now time.Time) []civogo.Instance {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []civogo.Instance
	for _, inst := range workers {
		if !want[inst.Hostname] || civoWorkerYoung(inst, now) || joined.Has("", civoWorkerAddrs(inst)...) {
			continue
		}
		out = append(out, inst)
	}
	return out
}

// prepareAndJoinCivoWorker takes a fresh instance through node prep, the
// kubelet cloud-provider flags and kubeadm join.
//
// Both flags come from the one constant, matching the create path. The chain
// this protects against is real but points the other way in compute mode: no
// CCM means no .spec.providerID, which means the CSI node plugin cannot
// identify its instance and publishes no CSINode, which means
// node.adhar.io/csi-not-ready is never lifted and the node stays Ready and
// completely empty. So compute mode asks for neither. An earlier comment here
// justified `true` by saying cloud_integration.go installs the Civo CCM and
// CSI — it did once, and stopped (see civoComputeHasExternalCCM); the argument
// outlived the code.
func prepareAndJoinCivoWorker(ctx context.Context, signer ssh.Signer, inst *civogo.Instance, joinCmd string) error {
	if err := provider.WaitForNodePrep(ctx, signer, computeSSHUser, inst.PublicIP, 15*time.Minute); err != nil {
		return fmt.Errorf("not ready: %w", err)
	}
	if err := provider.EnableExternalCloudProvider(signer, computeSSHUser, inst.PublicIP, inst.PrivateIP, civoComputeHasExternalCCM, civoComputeHasExternalCCM); err != nil {
		return err
	}
	return provider.KubeadmJoinWorker(signer, computeSSHUser, inst.PublicIP, joinCmd)
}
