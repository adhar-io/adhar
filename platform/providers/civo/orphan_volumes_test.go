package civo

// Civo must be able to sweep orphaned volumes with NO live cluster.
//
// `adhar down --purge-orphaned-volumes` resolves the provider and asks for
// helpers.OrphanVolumeSweeper. Civo did not implement it, so the flag printed
// "provider civo cannot sweep orphaned volumes without a cluster yet" and
// deleted nothing — the exact moment an operator reads the hint that
// sweepClusterVolumes prints and tries to act on it.
//
// Same gap as the GCP one that left 74 disks / 771 GB billing (2026-09-26),
// fixed then for GCP, Azure and DigitalOcean. Civo was missed, and a deleted
// mum1 cluster left 36 volumes / 317 GB behind on 2026-10-09 — 36 of a
// 40-volume account quota, which stops the next bring-up before RAM does.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/civo/civogo"

	"adhar-io/adhar/cmd/helpers"
)

// The interface assertion is the guard: if PurgeOrphanedVolumes is removed or
// its signature drifts, this stops compiling.
var _ helpers.OrphanVolumeSweeper = (*Provider)(nil)

func TestCivoImplementsTheOrphanVolumeSweeper(t *testing.T) {
	var p any = &Provider{}
	if _, ok := p.(helpers.OrphanVolumeSweeper); !ok {
		t.Fatal("the Civo provider does not implement helpers.OrphanVolumeSweeper, so " +
			"`adhar down --purge-orphaned-volumes` cannot clean up after a deleted cluster")
	}
}

// A nil client must report a problem, not panic — PurgeOrphanedVolumes is
// reachable from a teardown whose provider never connected.
func TestPurgeOrphanedVolumesWithoutAClientIsSafe(t *testing.T) {
	p := &Provider{}
	deleted, errs := p.PurgeOrphanedVolumes(context.Background())
	if deleted != 0 {
		t.Errorf("deleted = %d with no client, want 0", deleted)
	}
	if len(errs) == 0 {
		t.Error("expected a reported problem when the civo client is not initialised")
	}
}

// The standalone sweep passes an EMPTY cluster identity, and that must not turn
// into "delete everything". Only unattached pvc-* volumes may go; a volume
// attached to a surviving instance, a bootable root disk, and a volume tagged
// for another Kubernetes cluster all have to be kept by the same pure function
// the teardown path uses — otherwise a cluster being rebuilt right now loses
// its storage to a sweep aimed at a deleted one.
func TestStandaloneSweepOnlyTakesUnattachedPlatformVolumes(t *testing.T) {
	cases := []struct {
		name string
		vol  civogo.Volume
		want volumeAction
	}{
		{"unattached pvc-*", civogo.Volume{Name: "pvc-abc", ID: "1"}, volumeDeleteOrphan},
		{"attached pvc-*", civogo.Volume{Name: "pvc-def", ID: "2", InstanceID: "i-live"}, volumeKeep},
		{"bootable root disk", civogo.Volume{Name: "pvc-ghi", ID: "3", Bootable: true}, volumeKeep},
		{"not ours", civogo.Volume{Name: "someones-data", ID: "4"}, volumeKeep},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Exactly what PurgeOrphanedVolumes passes: no cluster name, no
			// instance ids, purging on.
			got, why := volumeDisposition(tc.vol, "", map[string]bool{}, true)
			if got != tc.want {
				t.Errorf("volumeDisposition(%s) = %v (%s), want %v", tc.name, got, why, tc.want)
			}
		})
	}
}

// Reaching PurgeOrphanedVolumes IS the operator's opt-in, so the method must
// force the purge flag on rather than trusting however the provider was built.
//
// `adhar down --purge-orphaned-volumes` does set providerOpts["purgeOrphanedVolumes"],
// so in that one path the flag is already true — which is exactly what makes
// this easy to drop and hard to notice. Any other caller (a test, a future
// `adhar cluster prune`, a provider built from a config file that does not set
// the key) gets a sweep that lists every volume, decides volumeKeep for all of
// them, and reports "0 deleted" as though the account were clean.
//
// This is a SOURCE guard, not a behaviour test, and deliberately so: p.client
// is a concrete *civogo.Client, so exercising the real sweep would need a client
// interface threaded through the whole provider. That refactor is not worth it
// for one assertion — but the regression is worth catching, and mutation-testing
// showed nothing else catches it.
func TestPurgeOrphanedVolumesForcesTheOptIn(t *testing.T) {
	raw, err := os.ReadFile("teardown.go")
	if err != nil {
		t.Fatalf("reading teardown.go: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "func (p *Provider) PurgeOrphanedVolumes(")
	if start < 0 {
		t.Fatal("PurgeOrphanedVolumes is gone from teardown.go")
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "p.config.PurgeOrphanedVolumes = true") {
		t.Error("PurgeOrphanedVolumes does not force p.config.PurgeOrphanedVolumes = true, so the " +
			"sweep keeps every volume and silently reports 0 deleted whenever the provider was " +
			"built without the flag")
	}
	if !strings.Contains(body, "p.config.PurgeOrphanedVolumes = prev") {
		t.Error("PurgeOrphanedVolumes does not restore the previous flag value; the provider is " +
			"reused after teardown and would stay in purge mode")
	}
}
