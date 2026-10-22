package gcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
)

// gcpCreationTime parses an instance's CreationTimestamp (RFC3339, e.g.
// "2026-10-22T01:02:03.456-07:00"). An empty or unparseable value is the zero
// time, which a scale decision reads as "age unknown".
func gcpCreationTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// gcpWorkerVMs is one node group's instances as a scale decision sees them.
// Only instances carrying the group's prefix are members of it; the others
// belong to another group and must neither count nor be discarded here. An
// instance that cannot be dated is Young: never discard what you cannot date.
func gcpWorkerVMs(prefix string, nodes []NodeInfo, now time.Time) []provider.WorkerVM {
	var vms []provider.WorkerVM
	for _, n := range nodes {
		if !strings.HasPrefix(n.InstanceName, prefix) {
			continue
		}
		var addrs []string
		for _, a := range []string{n.PrivateIP, n.PublicIP} {
			if a != "" {
				addrs = append(addrs, a)
			}
		}
		vms = append(vms, provider.WorkerVM{
			Name:  n.InstanceName,
			Addrs: addrs,
			Young: n.CreatedAt.IsZero() || now.Sub(n.CreatedAt) < provider.OrphanGrace,
		})
	}
	return vms
}

// withoutWorker returns the workers minus the named one (as a NEW slice, so a
// failed delete leaves the caller's list intact) and the zone that instance
// lives in, falling back to the provider's zone.
func withoutWorker(nodes []NodeInfo, name, defaultZone string) ([]NodeInfo, string) {
	zone := defaultZone
	kept := make([]NodeInfo, 0, len(nodes))
	for _, w := range nodes {
		if w.InstanceName == name {
			if w.Zone != "" {
				zone = w.Zone
			}
			continue
		}
		kept = append(kept, w)
	}
	return kept, zone
}

// dateWorkers fills in the creation time of this group's workers that state
// recorded without one, so an orphan from before the field existed can still
// be judged. A lookup that fails leaves the time unknown (the worker stays
// Young).
func (p *Provider) dateWorkers(ctx context.Context, prefix string, nodes []NodeInfo) {
	if p.instanceClient == nil {
		return
	}
	for i := range nodes {
		if !nodes[i].CreatedAt.IsZero() || !strings.HasPrefix(nodes[i].InstanceName, prefix) {
			continue
		}
		zone := nodes[i].Zone
		if zone == "" {
			zone = p.config.Zone
		}
		if _, _, created, err := p.instanceDetails(ctx, nodes[i].InstanceName, zone); err == nil {
			nodes[i].CreatedAt = created
		}
	}
}

// prepareAndJoinGCPWorker takes a fresh instance through node prep, the
// external cloud-provider kubelet flags and kubeadm join — the same flags as a
// worker from `adhar up`: cloud-provider-gcp initialises the node, the PD CSI
// node plugin lifts the taint.
func prepareAndJoinGCPWorker(ctx context.Context, signer ssh.Signer, n *NodeInfo, joinCmd string) error {
	if err := provider.WaitForNodePrep(ctx, signer, gcpSSHUser, n.PublicIP, 15*time.Minute); err != nil {
		return fmt.Errorf("not ready: %w", err)
	}
	if err := provider.EnableExternalCloudProvider(signer, gcpSSHUser, n.PublicIP, n.PrivateIP, true, true); err != nil {
		return err
	}
	return provider.KubeadmJoinWorker(signer, gcpSSHUser, n.PublicIP, joinCmd)
}
