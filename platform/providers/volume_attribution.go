package provider

// Block-volume attribution: which cloud volumes a cluster created, and how
// `adhar down` removes exactly those.
//
// THE PROBLEM. A cluster's CSI driver creates one cloud block volume per
// PersistentVolume, and the volume outlives the instance it was attached to.
// Nothing on the volume said WHICH cluster made it: the AWS teardown of
// 2026-10-31 left six EBS volumes in ap-southeast-1 tagged only `CSIVolumeName`
// and `ebs.csi.aws.com/cluster=true` (plus the PVC it was created for) — the
// tags EVERY EBS CSI driver writes — in an account that had run many clusters
// called "adhar" in that region. A week earlier 22 more had piled up the same
// way. A cluster NAME is not an identity: names are reused, so a name tag
// cannot tell this cluster's data from a previous incarnation's, or from data an
// operator deliberately kept.
//
// THE FIX, IN TWO HALVES.
//
//  1. Attribution at creation. `adhar up` generates an install id once, before
//     the cluster exists (NewClusterInstallID), records it in the CLI's state
//     (install_registry.go) and in the cluster's adhar-cluster-spec ConfigMap,
//     and each provider hands it to its CSI driver in the provider's own
//     vocabulary — ClusterIDTag on EBS, ClusterIDLabel on GCP and Azure,
//     DigitalOceanClusterIDTag on DigitalOcean. Managed clouds whose own CSI
//     driver stamps the cloud's cluster UUID on every volume (Civo's ClusterID,
//     DOKS's `k8s:<uuid>` tag) are attributed by that UUID instead.
//  2. Teardown. Once the cluster's instances are gone (so its volumes detach),
//     `adhar down` lists the volumes carrying the id, asks, deletes them, and
//     VERIFIES each is gone (ReapVolumes). Volumes from before this existed are
//     offered only when the provider can PROVE they were a platform cluster's
//     (LegacyVolumes), and never without an interactive confirmation.

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// ClusterIDTag is the attribution tag on AWS, where tag keys may carry '.'
	// and '/': `adhar.io/cluster-id=<install id>` — the platform's own label
	// convention.
	ClusterIDTag = "adhar.io/cluster-id"

	// ClusterIDLabel is the same key for clouds whose alphabets forbid '.' and
	// '/': GCP label keys (lowercase letters, digits, '-', '_', starting with a
	// letter) and Azure tag names (no '/').
	ClusterIDLabel = "adhar-cluster-id"
)

// KeepClusterVolumesOption is the provider-map key `adhar down` sets on every
// provider it builds. It tells DeleteCluster to leave the cluster's CSI volumes
// alone: the teardown's own volume step lists them, asks, deletes and VERIFIES
// them — and honours --keep-volumes, which a deletion buried inside
// DeleteCluster never could. Other callers (`adhar cluster delete`,
// `--recreate`) do not set it and keep their existing behaviour.
const KeepClusterVolumesOption = "keepClusterVolumes"

// KeepClusterVolumesRequested reads KeepClusterVolumesOption from a provider
// map.
func KeepClusterVolumesRequested(config map[string]interface{}) bool {
	v, ok := config[KeepClusterVolumesOption].(bool)
	return ok && v
}

// DigitalOceanClusterIDTag is the attribution tag on DigitalOcean, whose tags
// are bare strings (letters, digits, ':', '-', '_'). The `adhar-cluster-`
// prefix is deliberate: every existing DigitalOcean sweep already treats a
// volume tagged `adhar-cluster-*` as belonging to SOME cluster and leaves it
// alone, so a volume carrying another cluster's id is protected by code that
// predates the id.
func DigitalOceanClusterIDTag(installID string) string {
	return ClusterIDLabel + ":" + installID
}

// installIDPattern is the install id's shape: a lowercase letter, then 15
// lowercase letters or digits.
//
// It must survive every place it is written. A leading LETTER is not cosmetic:
// `helm --set` types a value that parses as an integer, so an id of decimal
// digits would reach the EBS driver as a number — and one with a leading zero
// would lose it, tagging every volume with an id nothing recorded. Lowercase
// alphanumerics are valid in a GCP label value, an Azure tag value, an AWS tag
// value and a DigitalOcean tag.
var installIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]{15}$`)

// ValidClusterInstallID reports whether id has the shape NewClusterInstallID
// produces.
func ValidClusterInstallID(id string) bool { return installIDPattern.MatchString(id) }

// NewClusterInstallID returns a fresh install id from crypto/rand: one
// lowercase letter and fifteen lowercase alphanumerics (~82 bits).
func NewClusterInstallID() (string, error) {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	const alnum = letters + "0123456789"
	pick := func(set string) (byte, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return 0, err
		}
		return set[n.Int64()], nil
	}
	out := make([]byte, 16)
	var err error
	if out[0], err = pick(letters); err != nil {
		return "", fmt.Errorf("generating a cluster install id: %w", err)
	}
	for i := 1; i < len(out); i++ {
		if out[i], err = pick(alnum); err != nil {
			return "", fmt.Errorf("generating a cluster install id: %w", err)
		}
	}
	return string(out), nil
}

// BlockVolume is one cloud block volume as the teardown reports it: enough to
// recognise it in the cloud console and to delete it by hand.
type BlockVolume struct {
	// ID is the cloud's own identifier — what the API takes and what the
	// DeleteCommand names (vol-…, the disk's ARM id, <zone>/<disk>, a UUID).
	ID string
	// Name is the volume's name where it differs from ID (pvc-…).
	Name string
	// SizeGiB is the provisioned size.
	SizeGiB int64
	// PVCNamespace and PVCName are the claim the volume was created for, when
	// the CSI driver recorded it.
	PVCNamespace string
	PVCName      string
	// Created is when the cloud created the volume; zero when unknown.
	Created time.Time
	// Location is the zone or region the volume lives in.
	Location string
	// AttachedTo lists what the volume is attached to. Empty means detached.
	AttachedTo []string
	// Evidence says why the volume is attributed to the cluster: the id it
	// carries, or — for a legacy volume — what proves it was a platform
	// cluster's.
	Evidence string
	// DeleteCommand is the cloud CLI line that deletes the volume by hand.
	DeleteCommand string
}

// Attached reports whether the volume is still attached to anything.
func (v BlockVolume) Attached() bool { return len(v.AttachedTo) > 0 }

// PVC is the claim as "namespace/name", or "-" when unknown.
func (v BlockVolume) PVC() string {
	switch {
	case v.PVCName == "":
		return "-"
	case v.PVCNamespace == "":
		return v.PVCName
	default:
		return v.PVCNamespace + "/" + v.PVCName
	}
}

// Label is how the volume is named in a message: its id, plus its name when
// the two differ.
func (v BlockVolume) Label() string {
	if v.Name == "" || v.Name == v.ID {
		return v.ID
	}
	return v.ID + " (" + v.Name + ")"
}

// TotalGiB sums the sizes of vols.
func TotalGiB(vols []BlockVolume) int64 {
	var n int64
	for _, v := range vols {
		n += v.SizeGiB
	}
	return n
}

// SortVolumes orders vols by creation time, then id, so every listing of the
// same set reads the same way.
func SortVolumes(vols []BlockVolume) {
	sort.SliceStable(vols, func(i, j int) bool {
		if !vols[i].Created.Equal(vols[j].Created) {
			return vols[i].Created.Before(vols[j].Created)
		}
		return vols[i].ID < vols[j].ID
	})
}

// VolumeOwner identifies whose volumes to look for.
type VolumeOwner struct {
	// ClusterName is the cluster's name (the `adhar up --name`).
	ClusterName string
	// InstallID is the install id `adhar up` gave the cluster; empty for a
	// cluster created before volumes were attributed.
	InstallID string
	// ProviderClusterID is the cloud's own id for a MANAGED cluster whose CSI
	// driver stamps it on every volume (a Civo or DOKS cluster UUID). Providers
	// ignore a value that is not a UUID: a compute cluster's "id" is its name tag,
	// and a name is exactly what cannot attribute a volume.
	ProviderClusterID string
}

// LegacyVolumes is the answer to "which volumes from before attribution were
// this cluster's?".
type LegacyVolumes struct {
	// Provable is true only when the provider can show the volumes belonged to a
	// platform cluster and to nothing still running — for AWS, that no instance
	// the platform (or any Kubernetes cluster) created remains in the region.
	Provable bool
	// Reason is the evidence when Provable, and why ownership cannot be proven
	// otherwise. It is printed either way.
	Reason string
	// Volumes are the candidates; empty unless Provable.
	Volumes []BlockVolume
}

// ClusterVolumeManager is implemented by providers whose clusters create cloud
// block volumes through a CSI driver. An OPTIONAL capability, like
// OrphanVolumeSweeper: a provider with no block storage (Kind, custom hosts,
// Civo compute mode) simply does not implement it.
//
// Every method is quiet — no log output — because the caller owns the terminal
// and prints a table of its own.
type ClusterVolumeManager interface {
	// ClusterVolumes lists every volume carrying the owner's attribution
	// (install id, or a managed cluster's UUID). An owner with neither returns
	// nothing: a name is not an identity.
	ClusterVolumes(ctx context.Context, owner VolumeOwner) ([]BlockVolume, error)
	// LegacyVolumes lists detached CSI volumes carrying NO install id, but only
	// when their ownership is provable; see LegacyVolumes.
	LegacyVolumes(ctx context.Context, owner VolumeOwner) (LegacyVolumes, error)
	// GetVolume re-reads one volume by ID. exists=false means it is gone; an
	// error means the cloud could not be asked, which proves nothing.
	GetVolume(ctx context.Context, id string) (v BlockVolume, exists bool, err error)
	// DeleteVolume deletes one detached volume. Deleting a volume that is
	// already gone is not an error.
	DeleteVolume(ctx context.Context, v BlockVolume) error
}

// ClusterVolumeFinalizer is implemented by providers with something left to do
// once the volume step has finished — Azure removes the cluster's resource
// group only after the disks inside it are gone, because deleting the group
// first would delete disks nobody had approved deleting.
type ClusterVolumeFinalizer interface {
	AfterClusterVolumes(ctx context.Context, owner VolumeOwner, remaining []BlockVolume) error
}

// ReapOptions bounds the volume step. Zero values take the defaults below.
type ReapOptions struct {
	// DetachTimeout is how long to wait for a volume to detach once its
	// instances are gone. AWS and GCP report a terminated instance's volumes
	// detached within a minute or two; the bound exists so a volume attached to
	// something that is NOT going away does not hold the teardown forever.
	DetachTimeout time.Duration
	// VerifyTimeout is how long a deleted volume may take to disappear from the
	// API before the deletion is reported as failed.
	VerifyTimeout time.Duration
	// PollInterval is the wait between reads.
	PollInterval time.Duration
	// Progress, when set, receives one line per state change.
	Progress func(string)
}

const (
	defaultDetachTimeout = 5 * time.Minute
	defaultVerifyTimeout = 3 * time.Minute
	defaultPollInterval  = 10 * time.Second
	// deleteAttempts covers the short window in which a cloud still reports a
	// just-detached volume as in use (DigitalOcean answers 409 for a few
	// seconds after its droplet is gone).
	deleteAttempts = 3
)

func (o ReapOptions) withDefaults() ReapOptions {
	if o.DetachTimeout <= 0 {
		o.DetachTimeout = defaultDetachTimeout
	}
	if o.VerifyTimeout <= 0 {
		o.VerifyTimeout = defaultVerifyTimeout
	}
	if o.PollInterval <= 0 {
		o.PollInterval = defaultPollInterval
	}
	return o
}

// VolumeFailure is a volume the step could not remove, and why.
type VolumeFailure struct {
	Volume BlockVolume
	Reason string
}

// ReapResult reports exactly what happened to every volume handed to
// ReapVolumes. Each volume is in exactly one list.
type ReapResult struct {
	// Deleted volumes were deleted AND then read back as gone.
	Deleted []BlockVolume
	// StillAttached volumes were never deleted: they stayed attached past
	// DetachTimeout, so something other than the cluster still uses them.
	StillAttached []BlockVolume
	// Failed volumes were asked to be deleted and are still there, or the cloud
	// refused or could not be asked.
	Failed []VolumeFailure
}

// Remaining is every volume that still exists after the step.
func (r ReapResult) Remaining() []BlockVolume {
	out := append([]BlockVolume{}, r.StillAttached...)
	for _, f := range r.Failed {
		out = append(out, f.Volume)
	}
	return out
}

// ReapVolumes deletes vols and proves it: wait (bounded) for each to detach,
// delete the detached ones, then read each back until the cloud says it is
// gone. A volume is reported Deleted only after that read.
//
// "The API accepted the delete" is not deletion. Every teardown failure this
// codebase has recorded began with a step that reported success because nothing
// raised an error (see helpers.NotFoundError); a volume that is still billing
// must be reported as such, with its id.
func ReapVolumes(ctx context.Context, mgr ClusterVolumeManager, vols []BlockVolume, opts ReapOptions) ReapResult {
	opts = opts.withDefaults()
	progress := func(format string, args ...any) {
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf(format, args...))
		}
	}
	var res ReapResult
	if mgr == nil || len(vols) == 0 {
		for _, v := range vols {
			res.Failed = append(res.Failed, VolumeFailure{Volume: v, Reason: "no volume manager for this provider"})
		}
		return res
	}

	// 1. Wait for detach. A volume that is already gone counts as deleted: the
	//    end state is the one asked for, and it is VERIFIED by this very read.
	pending := append([]BlockVolume{}, vols...)
	var ready []BlockVolume
	readErr := map[string]error{}
	detachDeadline := time.Now().Add(opts.DetachTimeout)
	for len(pending) > 0 {
		var still []BlockVolume
		for _, v := range pending {
			cur, exists, err := mgr.GetVolume(ctx, v.ID)
			delete(readErr, v.ID)
			switch {
			case err != nil:
				// Unknown is not detached. Keep waiting; the deadline decides.
				readErr[v.ID] = err
				still = append(still, v)
			case !exists:
				res.Deleted = append(res.Deleted, v)
			case cur.Attached():
				v.AttachedTo = cur.AttachedTo
				still = append(still, v)
			default:
				ready = append(ready, v)
			}
		}
		pending = still
		if len(pending) == 0 || !time.Now().Before(detachDeadline) || ctx.Err() != nil {
			break
		}
		progress("waiting for %d volume(s) to detach", len(pending))
		sleepCtx(ctx, opts.PollInterval)
	}
	for _, v := range pending {
		if err := readErr[v.ID]; err != nil {
			// Never deleted, because its state could not be read: an unknown
			// attachment is not a detached volume.
			res.Failed = append(res.Failed, VolumeFailure{Volume: v,
				Reason: fmt.Sprintf("not deleted: its state could not be read (%v)", err)})
			continue
		}
		res.StillAttached = append(res.StillAttached, v)
	}

	// 2. Delete. A refusal is retried briefly; a refusal for a volume that turns
	//    out to be gone is not a failure.
	var deleting []BlockVolume
	for _, v := range ready {
		var delErr error
		for attempt := 0; attempt < deleteAttempts; attempt++ {
			if delErr = mgr.DeleteVolume(ctx, v); delErr == nil {
				break
			}
			if _, exists, gerr := mgr.GetVolume(ctx, v.ID); gerr == nil && !exists {
				delErr = nil
				break
			}
			if attempt < deleteAttempts-1 {
				sleepCtx(ctx, opts.PollInterval)
			}
		}
		if delErr != nil {
			res.Failed = append(res.Failed, VolumeFailure{Volume: v, Reason: "delete refused: " + delErr.Error()})
			continue
		}
		progress("deleting %s", v.Label())
		deleting = append(deleting, v)
	}

	// 3. Verify: read each back until the cloud no longer has it.
	verifyDeadline := time.Now().Add(opts.VerifyTimeout)
	for len(deleting) > 0 {
		var still []BlockVolume
		lastErr := map[string]error{}
		for _, v := range deleting {
			_, exists, err := mgr.GetVolume(ctx, v.ID)
			switch {
			case err != nil:
				lastErr[v.ID] = err
				still = append(still, v)
			case exists:
				still = append(still, v)
			default:
				res.Deleted = append(res.Deleted, v)
			}
		}
		deleting = still
		if len(deleting) == 0 {
			break
		}
		if !time.Now().Before(verifyDeadline) || ctx.Err() != nil {
			for _, v := range deleting {
				reason := fmt.Sprintf("deletion was accepted but the volume still exists after %s", opts.VerifyTimeout)
				if err := lastErr[v.ID]; err != nil {
					reason = fmt.Sprintf("deletion was accepted but could not be verified: %v", err)
				}
				res.Failed = append(res.Failed, VolumeFailure{Volume: v, Reason: reason})
			}
			break
		}
		progress("verifying %d deletion(s)", len(deleting))
		sleepCtx(ctx, opts.PollInterval)
	}
	return res
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// uuidPattern matches a canonical UUID, which is what Civo and DOKS use as a
// managed cluster's id.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsCloudClusterUUID reports whether id is a cloud-issued cluster UUID rather
// than a name-derived handle such as `adhar-cluster-<name>`. Only the former
// identifies one cluster for its whole life.
func IsCloudClusterUUID(id string) bool { return uuidPattern.MatchString(strings.TrimSpace(id)) }
