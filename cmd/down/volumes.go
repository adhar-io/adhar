package down

// The volume step: what `adhar down` does with a cluster's block volumes once
// the cluster itself is gone.
//
// A CSI volume outlives the instance it was attached to, and for a long time
// nothing said which cluster had made it. The AWS teardown of 2026-10-31 left six
// EBS volumes in ap-southeast-1 carrying only the tags every EBS CSI driver
// writes, a week after 22 more had piled up the same way, in an account that had
// run many clusters called "adhar" there. Clusters now carry an install id onto
// every volume (platform/providers/volume_attribution.go), and this step uses it:
//
//  1. After the cluster's instances (or managed cluster) are deleted — so its
//     volumes detach — list every volume carrying an install id the CLI recorded
//     for this cluster name, plus a managed cluster's cloud UUID.
//  2. Show exactly that list (id, size, PVC, created) and, unless --yes/--force,
//     ask. --keep-volumes keeps them and says how to delete them later.
//  3. Wait (bounded) for each to detach, delete it, and READ IT BACK until the
//     cloud says it is gone. Anything left is reported by id, with the command
//     that finishes the job.
//  4. Drop the CLI's record of an install id only once a fresh lookup finds no
//     volume carrying it. A record that still points at a volume is the only
//     way back to it.
//
// Volumes from before attribution existed are offered separately, only when the
// provider can PROVE no running cluster could own them, and only after a typed
// confirmation at an interactive terminal — see legacyNeedsATerminal.
//
// A provided cluster (clusterMode: provided) never reaches this step: the
// teardown loop skips it before locating anything, and runJob refuses one again.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
)

// volumeJob is one cluster whose volumes the step must deal with.
type volumeJob struct {
	Env          string
	ProviderName string
	Region       string
	// ClusterNames are the names the cluster may be recorded under, primary
	// first (the same candidates the teardown searched).
	ClusterNames []string
	// ClusterMode is the environment's cluster mode; a provided cluster is
	// refused here as well as before the teardown.
	ClusterMode string
	// Provider is the provider that deleted the cluster; nil when the cluster
	// was already gone and one must be built from the config.
	Provider pfactory.Provider
	// ProviderClusterID is the cloud's id for the cluster when it was found.
	// Only a cloud-issued UUID (managed Civo, DOKS) attributes anything.
	ProviderClusterID string
	// ClusterDeleted is true when this run deleted the cluster, false when it
	// was already gone.
	ClusterDeleted bool
}

func (j volumeJob) primaryName() string {
	if len(j.ClusterNames) == 0 {
		return ""
	}
	return j.ClusterNames[0]
}

// volumeStep runs the step. Everything it needs from the outside world is a
// field, so the decisions can be tested without a cloud or a terminal.
type volumeStep struct {
	ctx context.Context
	out io.Writer
	in  *bufio.Reader
	// interactive is true when stdin is a terminal a person is typing into.
	interactive bool
	// assumeYes is --yes / --force.
	assumeYes bool
	// keep is --keep-volumes.
	keep bool
	// finish is the command that re-runs this teardown, printed with anything
	// left behind.
	finish string
	// managerFor returns the job's volume manager, or nil when its provider
	// creates no block volumes.
	managerFor func(volumeJob) (pfactory.ClusterVolumeManager, error)
	reap       pfactory.ReapOptions
	now        func() time.Time
}

// newVolumeStep wires the step to the real terminal, flags and config.
func newVolumeStep(cfg *config.Config, providerOpts map[string]interface{}) *volumeStep {
	return &volumeStep{
		ctx:         context.Background(),
		out:         os.Stdout,
		in:          bufio.NewReader(os.Stdin),
		interactive: term.IsTerminal(int(os.Stdin.Fd())),
		assumeYes:   forceDelete,
		keep:        keepVolumes,
		finish:      finishCommand(),
		managerFor: func(j volumeJob) (pfactory.ClusterVolumeManager, error) {
			p := j.Provider
			if p == nil {
				built, err := helpers.BuildProvider(cfg, j.ProviderName, providerOpts)
				if err != nil {
					return nil, err
				}
				p = built
			}
			mgr, ok := p.(pfactory.ClusterVolumeManager)
			if !ok {
				return nil, nil
			}
			return mgr, nil
		},
		now: time.Now,
	}
}

// finishCommand is how to re-run this teardown: with the cluster gone, only the
// volume step has anything left to do.
func finishCommand() string {
	cmd := "adhar down -f " + downConfigFile
	if downEnv != "" {
		cmd += " --env " + downEnv
	}
	if downClusterName != "" {
		cmd += " --name " + downClusterName
	}
	return cmd
}

func (s *volumeStep) printf(format string, args ...any) {
	fmt.Fprint(s.out, helpers.Indent(fmt.Sprintf(format, args...)))
}

// run handles every job and reports whether all of them finished. Volumes kept
// on purpose (--keep-volumes, a "no", a legacy volume without a terminal) are
// not a failure; a volume that could not be listed, deleted or verified is.
func (s *volumeStep) run(jobs []volumeJob) bool {
	ok := true
	for _, j := range mergeVolumeJobs(jobs) {
		if !s.runJob(j) {
			ok = false
		}
	}
	return ok
}

// mergeVolumeJobs collapses jobs that describe the same cluster. Environments
// default to `isolation: namespace` — dev, test and prod as namespaces of ONE
// cluster — so tearing down a whole file queues that cluster once per
// environment, and without this its volumes would be listed and asked about
// three times. The strictest mode wins: if any of them says provided, nothing
// is deleted.
func mergeVolumeJobs(jobs []volumeJob) []volumeJob {
	index := map[string]int{}
	var out []volumeJob
	for _, j := range jobs {
		key := strings.ToLower(j.ProviderName) + "|" + j.Region + "|" + j.primaryName()
		i, seen := index[key]
		if !seen {
			index[key] = len(out)
			j.ClusterNames = append([]string{}, j.ClusterNames...)
			out = append(out, j)
			continue
		}
		m := &out[i]
		for _, n := range j.ClusterNames {
			if !containsString(m.ClusterNames, n) {
				m.ClusterNames = append(m.ClusterNames, n)
			}
		}
		if j.ClusterDeleted && !m.ClusterDeleted {
			m.ClusterDeleted, m.Provider, m.ProviderClusterID = true, j.Provider, j.ProviderClusterID
		}
		if !pfactory.ClusterLifecycleIsOurs(j.ClusterMode) {
			m.ClusterMode = j.ClusterMode
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// tracked is one install record and whether it exists on disk yet.
type tracked struct {
	rec       pfactory.ClusterInstall
	persisted bool
}

func (s *volumeStep) runJob(j volumeJob) bool {
	name := j.primaryName()
	where := strings.TrimSpace(j.ProviderName + " " + j.Region)
	if !pfactory.ClusterLifecycleIsOurs(j.ClusterMode) {
		// Unreachable from the teardown loop; refused here too, because this is
		// the code that deletes.
		s.printf("\n%s is a provided cluster (clusterMode: %s): the platform deletes none of its volumes.\n", name, j.ClusterMode)
		return true
	}
	mgr, err := s.managerFor(j)
	if err != nil {
		s.printf("\n%s Could not check %s for volumes of %s: %v\n", helpers.IconFailed, where, name, err)
		return false
	}
	if mgr == nil {
		// Kind, custom hosts, Civo compute mode: no cloud block volumes.
		return true
	}

	var records []tracked
	for _, n := range j.ClusterNames {
		recs, err := pfactory.LoadClusterInstalls(j.ProviderName, j.Region, n)
		if err != nil {
			s.printf("\n%s %v\n", helpers.IconFailed, err)
			return false
		}
		for _, r := range recs {
			records = append(records, tracked{rec: r, persisted: true})
		}
	}
	if pfactory.IsCloudClusterUUID(j.ProviderClusterID) && !hasCloudRecord(records, j.ProviderClusterID) {
		records = append(records, tracked{rec: pfactory.ClusterInstall{
			ProviderClusterID: j.ProviderClusterID, Provider: j.ProviderName, Region: j.Region,
			ClusterName: name, CreatedAt: s.now().UTC(),
		}})
	}

	// 1. What carries this cluster's attribution.
	var attributed []pfactory.BlockVolume
	seen := map[string]bool{}
	for _, t := range records {
		vols, err := mgr.ClusterVolumes(s.ctx, t.rec.Owner())
		if err != nil {
			s.printf("\n%s Could not list the volumes of %s (%s): %v\n", helpers.IconFailed, name, ownerLabel(t.rec), err)
			s.printf("  Nothing was deleted and the record is kept; re-run: %s\n", s.finish)
			return false
		}
		for _, v := range vols {
			if !seen[v.ID] {
				seen[v.ID] = true
				attributed = append(attributed, v)
			}
		}
	}
	pfactory.SortVolumes(attributed)

	// 2. What is provably this cluster's from before attribution existed.
	ok := true
	legacy, err := mgr.LegacyVolumes(s.ctx, pfactory.VolumeOwner{ClusterName: name})
	if err != nil {
		s.printf("\n%s Could not check %s for volumes from before clusters were tagged: %v\n", helpers.IconFailed, where, err)
		ok = false
	}
	var legacyVols []pfactory.BlockVolume
	for _, v := range legacy.Volumes {
		if !seen[v.ID] {
			legacyVols = append(legacyVols, v)
		}
	}

	if len(attributed) == 0 && len(legacyVols) == 0 {
		s.printf("\n%s No block volumes left by %s in %s.\n", helpers.IconReady, name, where)
		s.noteUnprovable(legacy)
		return s.settle(mgr, j, records, nil) && ok
	}
	s.noteUnprovable(legacy)

	// 3. Decide.
	var approved, keptByChoice []pfactory.BlockVolume
	keepReason := map[string]string{}
	if len(attributed) > 0 {
		s.printf("\n%s left %d block volume(s), %d GiB, tagged with its install id:\n\n", name, len(attributed), pfactory.TotalGiB(attributed))
		fmt.Fprintln(s.out, volumeTable(attributed))
		switch {
		case s.keep:
			keptByChoice = append(keptByChoice, attributed...)
			for _, v := range attributed {
				keepReason[v.ID] = "kept (--keep-volumes)"
			}
		case s.assumeYes || s.confirm(fmt.Sprintf("Delete these %d volume(s)? Their data cannot be recovered.", len(attributed))):
			approved = append(approved, attributed...)
		default:
			keptByChoice = append(keptByChoice, attributed...)
			for _, v := range attributed {
				keepReason[v.ID] = "kept (not confirmed)"
			}
		}
	}
	if len(legacyVols) > 0 {
		s.printf("\n%d volume(s), %d GiB, from before clusters were tagged with an install id — %s:\n\n",
			len(legacyVols), pfactory.TotalGiB(legacyVols), legacy.Reason)
		fmt.Fprintln(s.out, volumeTable(legacyVols))
		switch {
		case s.keep:
			for _, v := range legacyVols {
				keepReason[v.ID] = "kept (--keep-volumes)"
			}
		case legacyNeedsATerminal(s.assumeYes, s.interactive):
			s.printf("These were found by evidence, not by a tag, so they are only deleted after a confirmation typed at a terminal;\n" +
				"--yes / --force does not cover them.\n")
			for _, v := range legacyVols {
				keepReason[v.ID] = "kept (needs an interactive confirmation)"
			}
		case s.confirm(fmt.Sprintf("Delete these %d volume(s) too? Nothing names their cluster; they are offered because nothing that could own them is left.", len(legacyVols))):
			approved = append(approved, legacyVols...)
		default:
			for _, v := range legacyVols {
				keepReason[v.ID] = "kept (not confirmed)"
			}
		}
		for _, v := range legacyVols {
			if _, kept := keepReason[v.ID]; kept {
				keptByChoice = append(keptByChoice, v)
			}
		}
	}

	// 4. Delete, and prove it.
	var res pfactory.ReapResult
	if len(approved) > 0 {
		opts := s.reap
		opts.Progress = func(line string) { s.printf("  … %s\n", line) }
		s.printf("\nDeleting %d volume(s) — waiting for each to detach, then reading it back until it is gone…\n", len(approved))
		res = pfactory.ReapVolumes(s.ctx, mgr, approved, opts)
	}
	s.report(res, keptByChoice, keepReason)
	if len(res.Failed) > 0 || len(res.StillAttached) > 0 {
		ok = false
	}

	remaining := append(append([]pfactory.BlockVolume{}, keptByChoice...), res.Remaining()...)
	if !s.settle(mgr, j, records, remaining) {
		ok = false
	}
	if len(remaining) > 0 {
		s.printf("\nTo delete what is left later, re-run (with the cluster gone, only the volumes are left to do):\n  %s\n", s.finish)
		s.printf("or delete them one by one:\n")
		for _, v := range remaining {
			if v.DeleteCommand != "" {
				s.printf("  %s\n", v.DeleteCommand)
			}
		}
	}
	return ok
}

// noteUnprovable says why volumes from before install ids were not offered,
// when the provider could not prove whose they were — "none offered" must not
// read as "none exist".
func (s *volumeStep) noteUnprovable(legacy pfactory.LegacyVolumes) {
	if !legacy.Provable && legacy.Reason != "" {
		s.printf("  Untagged volumes from before clusters carried an install id were not offered — %s\n", legacy.Reason)
	}
}

// legacyNeedsATerminal is the rule for volumes found by EVIDENCE rather than by
// an id: they are deleted only after a confirmation typed at a terminal.
//
// --yes/--force deliberately does NOT cover them. An install id proves a volume
// is this cluster's; legacy evidence only proves nothing ELSE could own it at the
// moment of looking ("no instance of any cluster is left in the region"), and the
// names that used to identify clusters are reused. --yes is what a script passes,
// and a script cannot look at a list. Retaining a volume costs cents a month and
// the exact command to remove it is printed; deleting the wrong one is permanent.
// A piped "yes" is not a person either, so stdin must be a terminal.
func legacyNeedsATerminal(assumeYes, interactive bool) bool {
	return assumeYes || !interactive
}

func (s *volumeStep) confirm(question string) bool {
	s.printf("\n%s\n", question)
	fmt.Fprint(s.out, helpers.Indent("Type yes to delete, anything else keeps them: "))
	if s.in == nil {
		fmt.Fprintln(s.out)
		return false
	}
	line, _ := s.in.ReadString('\n')
	return strings.TrimSpace(line) == "yes"
}

func (s *volumeStep) report(res pfactory.ReapResult, kept []pfactory.BlockVolume, keepReason map[string]string) {
	if len(res.Deleted) > 0 {
		s.printf("\n%s Deleted %d volume(s), %d GiB — each read back as gone:\n", helpers.IconReady, len(res.Deleted), pfactory.TotalGiB(res.Deleted))
		for _, v := range res.Deleted {
			s.printf("  %s  %d GiB  %s\n", v.Label(), v.SizeGiB, v.PVC())
		}
	}
	if len(res.StillAttached) > 0 {
		s.printf("\n%s Not deleted — still attached after the wait, so something else uses them:\n", helpers.IconFailed)
		for _, v := range res.StillAttached {
			s.printf("  %s  attached to %s\n", v.Label(), strings.Join(v.AttachedTo, ", "))
		}
	}
	if len(res.Failed) > 0 {
		s.printf("\n%s Failed:\n", helpers.IconFailed)
		for _, f := range res.Failed {
			s.printf("  %s  %s\n", f.Volume.Label(), f.Reason)
		}
	}
	if len(kept) > 0 {
		s.printf("\n%s Kept %d volume(s), %d GiB — they keep billing:\n", helpers.IconDegraded, len(kept), pfactory.TotalGiB(kept))
		for _, v := range kept {
			s.printf("  %s  %d GiB  %s  %s\n", v.Label(), v.SizeGiB, v.PVC(), keepReason[v.ID])
		}
	}
}

// settle brings the CLI's records up to date and runs the provider's
// finalizer. A record is dropped only when a FRESH lookup by its id finds no
// volume; one that still points at volumes is kept, with them listed.
func (s *volumeStep) settle(mgr pfactory.ClusterVolumeManager, j volumeJob, records []tracked, remaining []pfactory.BlockVolume) bool {
	ok := true
	fin, hasFinalizer := mgr.(pfactory.ClusterVolumeFinalizer)
	for _, t := range records {
		left, err := mgr.ClusterVolumes(s.ctx, t.rec.Owner())
		if err != nil {
			s.printf("%s Could not confirm what is left of %s (%v); its record is kept.\n", helpers.IconFailed, ownerLabel(t.rec), err)
			ok = false
			continue
		}
		if len(left) == 0 {
			if t.persisted {
				if err := pfactory.ForgetClusterInstall(t.rec); err != nil {
					s.printf("%s %v\n", helpers.IconFailed, err)
					ok = false
				}
			}
		} else {
			rec := t.rec
			if rec.ClusterDeletedAt == nil {
				at := s.now().UTC()
				rec.ClusterDeletedAt = &at
			}
			rec.RetainedVolumes = rec.RetainedVolumes[:0]
			for _, v := range left {
				rec.RetainedVolumes = append(rec.RetainedVolumes, v.ID)
			}
			sort.Strings(rec.RetainedVolumes)
			rec.Note = fmt.Sprintf("the cluster is gone; %d volume(s) carrying its id remain", len(left))
			if err := pfactory.SaveClusterInstall(rec); err != nil {
				s.printf("%s Could not record the %d volume(s) left by %s: %v\n", helpers.IconFailed, len(left), ownerLabel(rec), err)
				ok = false
			}
		}
		if hasFinalizer {
			if err := fin.AfterClusterVolumes(s.ctx, t.rec.Owner(), left); err != nil {
				s.printf("%s %v\n", helpers.IconFailed, err)
				ok = false
			}
		}
	}
	if hasFinalizer && len(records) == 0 {
		// A cluster with no record (one from before attribution): the provider
		// may still have cleanup that waited on its legacy volumes.
		if err := fin.AfterClusterVolumes(s.ctx, pfactory.VolumeOwner{ClusterName: j.primaryName()}, remaining); err != nil {
			s.printf("%s %v\n", helpers.IconFailed, err)
			ok = false
		}
	}
	return ok
}

func hasCloudRecord(records []tracked, cloudID string) bool {
	for _, t := range records {
		if t.rec.ProviderClusterID == cloudID {
			return true
		}
	}
	return false
}

func ownerLabel(r pfactory.ClusterInstall) string {
	if r.ID != "" {
		return "install " + r.ID
	}
	return "cluster " + r.ProviderClusterID
}

// volumeTable is the list a person confirms: exactly the volumes, by id.
func volumeTable(vols []pfactory.BlockVolume) string {
	t := helpers.NewTable("VOLUME", "SIZE", "PVC", "CREATED", "STATE")
	for _, v := range vols {
		created := "-"
		if !v.Created.IsZero() {
			created = v.Created.UTC().Format("2006-01-02 15:04")
		}
		state := "detached"
		if v.Attached() {
			state = "attached to " + strings.Join(v.AttachedTo, ",")
		}
		t.Row(v.Label(), fmt.Sprintf("%d GiB", v.SizeGiB), v.PVC(), created, state)
	}
	return t.Render()
}

// teardownProviderOptions are the per-run options every provider this teardown
// builds receives. KeepClusterVolumesOption is always set: the cluster's own
// volumes are this step's to list, confirm and verify, so DeleteCluster must not
// delete them unasked — which is also the only way --keep-volumes can be
// honoured at all.
func teardownProviderOptions() map[string]interface{} {
	opts := map[string]interface{}{pfactory.KeepClusterVolumesOption: true}
	if purgeVolumes {
		opts["purgeOrphanedVolumes"] = true
	}
	return opts
}

// clusterModeOf is the environment's cluster mode for a volume job. The
// teardown loop has already refused an unparseable mode before any job exists,
// so an error here cannot happen; it is mapped to "provided" anyway, the one
// value under which the step deletes nothing.
func clusterModeOf(env *config.ResolvedEnvironmentConfig) string {
	mode, err := environmentClusterMode(env)
	if err != nil {
		return pfactory.ClusterModeProvided
	}
	return mode
}

// runVolumeStep runs the volume step for a finished teardown, on the real
// terminal. It reports whether everything it found was dealt with.
func runVolumeStep(jobs []volumeJob) bool {
	if len(jobs) == 0 {
		return true
	}
	cfg, err := config.LoadConfig(downConfigFile)
	if err == nil {
		err = cfg.ResolveEnvironments()
	}
	if err != nil {
		// Only clusters that were already gone need the config (to build their
		// provider); the deleted ones carry the provider that deleted them.
		cfg = nil
	}
	return newVolumeStep(cfg, teardownProviderOptions()).run(jobs)
}
