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
// confirmation at an interactive terminal — see legacyNeedsATerminal. The same
// terminal rule protects volumes an earlier teardown kept ON PURPOSE
// (--keep-volumes, or a "no"): a later blanket --yes does not undo that choice.
//
// A provided cluster (clusterMode: provided) never reaches this step: the
// teardown loop skips it before locating anything, and runJob refuses one again.
// While a provided environment in the same file may run in the region, the
// legacy offer is not made at all (providedNeighbours).

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
	"adhar-io/adhar/globals"
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
	// ProviderOpts are the options the environment's provider is built with
	// (environmentProviderOptions) — its region and clusterConfig over the
	// provider block, as `adhar up` built it. nil means the teardown's own
	// options alone.
	ProviderOpts map[string]interface{}
	// ProviderClusterID is the cloud's id for the cluster when it was found.
	// Only a cloud-issued UUID (managed Civo, DOKS) attributes anything.
	ProviderClusterID string
	// ClusterDeleted is true when this run deleted the cluster, false when it
	// was already gone.
	ClusterDeleted bool
	// DeletedName is the name of the cluster this run deleted (when
	// ClusterDeleted): the one cluster whose records may still read "running"
	// and be acted on without a terminal — this run SAW it deleted.
	DeletedName string
	// ProvidedNeighbours are the environments in the same file whose cluster is
	// PROVIDED and may run where this one did (providedNeighbours). Their
	// volumes carry no install id, so while one exists an untagged volume here
	// may be theirs: the legacy offer is not provable and is never made.
	ProvidedNeighbours []string
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
				opts := j.ProviderOpts
				if opts == nil {
					opts = providerOpts
				}
				built, err := buildTeardownProvider(cfg, j.ProviderName, opts)
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
			m.ClusterDeleted, m.Provider, m.ProviderClusterID, m.DeletedName = true, j.Provider, j.ProviderClusterID, j.DeletedName
		}
		if !pfactory.ClusterLifecycleIsOurs(j.ClusterMode) {
			m.ClusterMode = j.ClusterMode
		}
		for _, n := range j.ProvidedNeighbours {
			if !containsString(m.ProvidedNeighbours, n) {
				m.ProvidedNeighbours = append(m.ProvidedNeighbours, n)
			}
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
	// unconfirmed is a record this machine still marks as RUNNING, for a
	// cluster this run did not delete: the only evidence that it is gone is
	// one listing call that did not name it. See unconfirmedGone.
	unconfirmed bool
	// confirmedGone is set when a person, at a terminal, confirmed deleting an
	// unconfirmed record's volumes — and with that, that its cluster is gone.
	confirmedGone bool
}

// unconfirmedGone reports whether a record describes a cluster the CLI still
// records as running while the job's cluster was not deleted by this run.
//
// "Not found" is only as good as the provider's listing, and several listings
// answer without an error when part of them failed — DigitalOcean and Civo
// log a failed droplet/instance listing and carry on with the managed half;
// AWS does the same for a failed EKS listing; Azure keeps a partial discovery
// when local state answered; GCP lists from local state. Read as "already
// gone", that sent a RUNNING cluster's volumes, by its install id, into a
// step that deletes the detached ones under --yes. So the install id still
// proves whose they are, but not that their cluster is gone: they are listed,
// never offered while any of them is attached, and deleted only after a
// confirmation typed at a terminal — and the record is left exactly as it was
// unless a person confirms.
func unconfirmedGone(j volumeJob, r pfactory.ClusterInstall) bool {
	if !r.Active() {
		return false // the CLI itself recorded the cluster as gone
	}
	return !(j.ClusterDeleted && r.ClusterName == j.DeletedName)
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
			records = append(records, tracked{rec: r, persisted: true, unconfirmed: unconfirmedGone(j, r)})
		}
	}
	if pfactory.IsCloudClusterUUID(j.ProviderClusterID) && !hasCloudRecord(records, j.ProviderClusterID) {
		// Only a job whose cluster this run deleted carries the cluster's cloud
		// id, so this record is the deleted cluster's: never unconfirmed.
		records = append(records, tracked{rec: pfactory.ClusterInstall{
			ProviderClusterID: j.ProviderClusterID, Provider: j.ProviderName, Region: j.Region,
			ClusterName: name, CreatedAt: s.now().UTC(),
		}, unconfirmed: !j.ClusterDeleted})
	}

	// 1. What carries this cluster's attribution. Volumes a previous teardown
	// kept ON PURPOSE (--keep-volumes, or a "no") are set apart: a blanket --yes
	// must not undo a choice somebody made about their data. So are the volumes
	// of a cluster this machine still records as running that this run did not
	// delete (unconfirmedGone): an install id proves whose they are, not that
	// their cluster is gone.
	var attributed, keptEarlier, unconfirmed []pfactory.BlockVolume
	seen := map[string]bool{}
	installOf := map[string]string{}
	recordOf := map[string]int{}
	for i, t := range records {
		vols, err := mgr.ClusterVolumes(s.ctx, t.rec.Owner())
		if err != nil {
			s.printf("\n%s Could not list the volumes of %s (%s): %v\n", helpers.IconFailed, name, ownerLabel(t.rec), err)
			s.printf("  Nothing was deleted and the record is kept; re-run: %s\n", s.finish)
			return false
		}
		for _, v := range vols {
			if seen[v.ID] {
				continue
			}
			seen[v.ID] = true
			installOf[v.ID] = recordLabel(t.rec)
			recordOf[v.ID] = i
			switch {
			case t.rec.KeptByChoice:
				keptEarlier = append(keptEarlier, v)
			case t.unconfirmed:
				unconfirmed = append(unconfirmed, v)
			default:
				attributed = append(attributed, v)
			}
		}
	}
	pfactory.SortVolumes(attributed)
	pfactory.SortVolumes(keptEarlier)
	pfactory.SortVolumes(unconfirmed)

	// 2. What is provably this cluster's from before attribution existed.
	ok := true
	var legacy pfactory.LegacyVolumes
	if len(j.ProvidedNeighbours) > 0 {
		// A provided cluster's volumes carry no install id and look exactly like
		// a leftover, and the provider's evidence ("no Kubernetes node left in
		// the region") cannot see a cluster whose nodes carry no tag of ours. The
		// platform does not own that cluster, so it does not even offer.
		legacy = pfactory.LegacyVolumes{Reason: fmt.Sprintf(
			"not provable: %s %s a provided cluster (clusterMode: provided) that may run in %s, and its volumes carry no install id",
			strings.Join(j.ProvidedNeighbours, ", "), pluralVerb(len(j.ProvidedNeighbours), "uses", "use"), where)}
	} else {
		var err error
		legacy, err = mgr.LegacyVolumes(s.ctx, pfactory.VolumeOwner{ClusterName: name})
		if err != nil {
			s.printf("\n%s Could not check %s for volumes from before clusters were tagged: %v\n", helpers.IconFailed, where, err)
			ok = false
		}
	}
	var legacyVols []pfactory.BlockVolume
	for _, v := range legacy.Volumes {
		if !seen[v.ID] {
			legacyVols = append(legacyVols, v)
		}
	}

	if len(attributed) == 0 && len(keptEarlier) == 0 && len(unconfirmed) == 0 && len(legacyVols) == 0 {
		s.printf("\n%s No block volumes left by %s in %s.\n", helpers.IconReady, name, where)
		s.noteUnprovable(legacy)
		return s.settle(mgr, j, records, nil, nil) && ok
	}
	s.noteUnprovable(legacy)

	// 3. Decide.
	//
	// unanswered are volumes kept because the prompt got NO answer at all
	// (stdin closed: a script that passed neither --yes nor --keep-volumes).
	// Nobody consented, so they are kept — but nobody chose to keep them either,
	// so they are not marked kept-on-purpose and a later --yes finishes the job.
	//
	// heldBack are volumes of a cluster not seen deleted (unconfirmedGone) that
	// were not deleted: their record is left exactly as it was, so they are
	// neither a choice to keep nor an unfinished deletion.
	var approved, keptByChoice, unanswered, heldBack []pfactory.BlockVolume
	keepReason := map[string]string{}
	if len(attributed) > 0 {
		s.printf("\n%s left %d block volume(s), %d GiB, tagged with its install id:\n\n", name, len(attributed), pfactory.TotalGiB(attributed))
		fmt.Fprintln(s.out, volumeTable(attributed, installOf))
		reply := answerYes
		if !s.keep && !s.assumeYes {
			reply = s.ask(fmt.Sprintf("Delete these %d volume(s)? Their data cannot be recovered.", len(attributed)))
		}
		switch {
		case s.keep:
			keptByChoice = append(keptByChoice, attributed...)
			for _, v := range attributed {
				keepReason[v.ID] = "kept (--keep-volumes)"
			}
		case reply == answerYes:
			approved = append(approved, attributed...)
		case reply == answerNone:
			unanswered = append(unanswered, attributed...)
			for _, v := range attributed {
				keepReason[v.ID] = "kept (no answer: stdin was closed; --yes deletes them)"
			}
		default:
			keptByChoice = append(keptByChoice, attributed...)
			for _, v := range attributed {
				keepReason[v.ID] = "kept (not confirmed)"
			}
		}
	}
	if len(keptEarlier) > 0 {
		s.printf("\n%d volume(s), %d GiB, that an earlier `adhar down` of %s KEPT on purpose (--keep-volumes, or a \"no\"):\n\n",
			len(keptEarlier), pfactory.TotalGiB(keptEarlier), name)
		fmt.Fprintln(s.out, volumeTable(keptEarlier, installOf))
		switch {
		case s.keep:
			for _, v := range keptEarlier {
				keepReason[v.ID] = "kept (--keep-volumes)"
			}
		case legacyNeedsATerminal(s.assumeYes, s.interactive):
			// The same rule as the legacy volumes, for a different reason: a
			// person chose to keep this data, and --yes is what a script passes.
			s.printf("Somebody chose to keep these, so --yes / --force does not delete them; only a confirmation typed at a terminal does.\n")
			for _, v := range keptEarlier {
				keepReason[v.ID] = "kept (kept on purpose earlier; confirm at a terminal to delete)"
			}
		case s.ask(fmt.Sprintf("Delete these %d volume(s) that were kept on purpose? Their data cannot be recovered.", len(keptEarlier))) == answerYes:
			approved = append(approved, keptEarlier...)
		default:
			for _, v := range keptEarlier {
				keepReason[v.ID] = "kept (not confirmed)"
			}
		}
		for _, v := range keptEarlier {
			if _, kept := keepReason[v.ID]; kept {
				keptByChoice = append(keptByChoice, v)
			}
		}
	}
	if len(unconfirmed) > 0 {
		s.printf("\n%d volume(s), %d GiB, carry the install id of a cluster %s that this machine still records as RUNNING,\n"+
			"and %s did not list it — it was deleted outside adhar, or the lookup missed it:\n\n",
			len(unconfirmed), pfactory.TotalGiB(unconfirmed), name, where)
		fmt.Fprintln(s.out, volumeTable(unconfirmed, installOf))
		var attached []string
		for _, v := range unconfirmed {
			if v.Attached() {
				attached = append(attached, v.Label())
			}
		}
		switch {
		case s.keep:
			for _, v := range unconfirmed {
				keepReason[v.ID] = "kept (--keep-volumes)"
			}
		case len(attached) > 0:
			// Something still uses them: the strongest sign there is that the
			// cluster is running and the lookup missed it.
			s.printf("%s %s still attached, so that cluster may well be running: none of these is offered.\n",
				strings.Join(attached, ", "), pluralVerb(len(attached), "is", "are"))
			for _, v := range unconfirmed {
				keepReason[v.ID] = "kept (a volume of the same install is attached: its cluster may be running)"
			}
		case legacyNeedsATerminal(s.assumeYes, s.interactive):
			s.printf("Nothing but one listing says that cluster is gone, so --yes / --force does not delete these;\n" +
				"only a confirmation typed at a terminal does.\n")
			for _, v := range unconfirmed {
				keepReason[v.ID] = "kept (its cluster was not seen deleted; confirm at a terminal to delete)"
			}
		case s.ask(fmt.Sprintf("Is %s really gone? Delete these %d volume(s)? Their data cannot be recovered.", name, len(unconfirmed))) == answerYes:
			approved = append(approved, unconfirmed...)
			for _, v := range unconfirmed {
				records[recordOf[v.ID]].confirmedGone = true
			}
		default:
			for _, v := range unconfirmed {
				keepReason[v.ID] = "kept (not confirmed)"
			}
		}
		for _, v := range unconfirmed {
			if _, kept := keepReason[v.ID]; kept {
				heldBack = append(heldBack, v)
			}
		}
	}
	if len(legacyVols) > 0 {
		s.printf("\n%d volume(s), %d GiB, from before clusters were tagged with an install id — %s:\n\n",
			len(legacyVols), pfactory.TotalGiB(legacyVols), legacy.Reason)
		fmt.Fprintln(s.out, volumeTable(legacyVols, nil))
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
		case s.ask(fmt.Sprintf("Delete these %d volume(s) too? Nothing names their cluster; they are offered because nothing that could own them is left.", len(legacyVols))) == answerYes:
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
	kept := append(append(append([]pfactory.BlockVolume{}, keptByChoice...), unanswered...), heldBack...)
	s.report(res, kept, keepReason)
	if len(res.Failed) > 0 || len(res.StillAttached) > 0 {
		ok = false
	}

	remaining := append(append([]pfactory.BlockVolume{}, kept...), res.Remaining()...)
	chosen := map[string]bool{}
	for _, v := range keptByChoice {
		chosen[v.ID] = true
	}
	if !s.settle(mgr, j, records, remaining, chosen) {
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

// answer is what came back from a prompt.
type answer int

const (
	// answerNone: nothing was read — stdin is closed or empty. Nobody decided
	// anything, so the volumes are kept but NOT recorded as kept on purpose.
	answerNone answer = iota
	// answerNo: an answer other than "yes" was given — a choice to keep them.
	answerNo
	answerYes
)

func (s *volumeStep) ask(question string) answer {
	s.printf("\n%s\n", question)
	fmt.Fprint(s.out, helpers.Indent("Type yes to delete, anything else keeps them: "))
	if s.in == nil {
		fmt.Fprintln(s.out)
		return answerNone
	}
	line, err := s.in.ReadString('\n')
	switch {
	case strings.TrimSpace(line) == "yes":
		return answerYes
	case err != nil && strings.TrimSpace(line) == "":
		// End of input with nothing typed: the prompt was never answered.
		fmt.Fprintln(s.out)
		return answerNone
	default:
		return answerNo
	}
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
//
// keptByChoice are the volumes this run kept because somebody said so
// (--keep-volumes, a "no", or an earlier such choice not overturned at a
// terminal): a record left pointing at any of them is marked KeptByChoice, so
// a later blanket --yes does not delete them.
func (s *volumeStep) settle(mgr pfactory.ClusterVolumeManager, j volumeJob, records []tracked, remaining []pfactory.BlockVolume, keptByChoice map[string]bool) bool {
	ok := true
	fin, hasFinalizer := mgr.(pfactory.ClusterVolumeFinalizer)
	for _, t := range records {
		if t.unconfirmed && !t.confirmedGone {
			// A cluster this machine still records as running, and nobody has
			// said otherwise: not marked deleted, not forgotten, no finalizer.
			// Marking it deleted would let the next `--yes` delete its volumes
			// on the strength of the same single listing.
			s.printf("  %s is still recorded as running on this machine (%s); %s did not list it, so its record is left as it is.\n",
				t.rec.ClusterName, ownerLabel(t.rec), strings.TrimSpace(j.ProviderName+" "+j.Region))
			continue
		}
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
			rec.KeptByChoice = false
			for _, v := range left {
				rec.RetainedVolumes = append(rec.RetainedVolumes, v.ID)
				rec.KeptByChoice = rec.KeptByChoice || keptByChoice[v.ID]
			}
			sort.Strings(rec.RetainedVolumes)
			rec.Note = fmt.Sprintf("the cluster is gone; %d volume(s) carrying its id remain", len(left))
			if rec.KeptByChoice {
				rec.Note += ", kept on purpose: a later --yes does not delete them"
			}
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

// recordLabel says, in the confirmation table, which cluster a volume came
// from: its install id (or cloud id), and when that cluster is not the one
// being torn down, that it is an earlier one.
func recordLabel(r pfactory.ClusterInstall) string {
	label := r.ID
	if label == "" {
		label = r.ProviderClusterID
	}
	if r.ClusterDeletedAt != nil {
		label += " (gone since " + r.ClusterDeletedAt.UTC().Format("2006-01-02") + ")"
	}
	return label
}

// volumeTable is the list a person confirms: exactly the volumes, by id —
// and, when installOf is given, the install each one carries.
//
// Unbounded: the table never shortens a cell. A confirmation over an id cut to
// "vol-0aaa…" — or an Azure disk id, ~150 characters of ARM path, cut anywhere
// — is not a confirmation of exactly these volumes; a wrapped line still is.
func volumeTable(vols []pfactory.BlockVolume, installOf map[string]string) string {
	headers := []string{"VOLUME", "SIZE", "PVC", "CREATED", "STATE"}
	if installOf != nil {
		headers = append(headers, "INSTALL")
	}
	t := helpers.NewTable(headers...).Unbounded()
	for _, v := range vols {
		created := "-"
		if !v.Created.IsZero() {
			created = v.Created.UTC().Format("2006-01-02 15:04")
		}
		state := "detached"
		if v.Attached() {
			state = "attached to " + strings.Join(v.AttachedTo, ",")
		}
		cells := []string{v.Label(), fmt.Sprintf("%d GiB", v.SizeGiB), v.PVC(), created, state}
		if installOf != nil {
			cells = append(cells, installOf[v.ID])
		}
		t.Row(cells...)
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

// environmentProviderOptions are the options an ENVIRONMENT's provider is built
// with during the teardown: the layer `adhar up` put over the provider block for
// that environment (its region and clusterConfig —
// pfactory.EnvironmentProviderOverrides), then teardownProviderOptions, which
// win. (Providers honour only a boolean for the teardown's own keys, and a
// clusterConfig value is a string, so no environment setting can switch the
// purge on or the volume step off.)
//
// The provider block alone is not the environment's provider. `adhar up`
// creates an environment's cluster in the environment's own `region:` and
// clusterConfig `clusterMode`; a teardown built from the block searched the
// block's region, found nothing, reported the cluster absent, and its volume
// step then listed volumes in the wrong place — dropping the install record of
// the still-running cluster (nothing there carried its id) and, on DigitalOcean,
// deleting its account-wide id tag. --purge-orphaned-volumes swept the block's
// region instead of the environment's.
func environmentProviderOptions(env *config.ResolvedEnvironmentConfig) map[string]interface{} {
	opts := pfactory.EnvironmentProviderOverrides(env)
	for k, v := range teardownProviderOptions() {
		opts[k] = v
	}
	return opts
}

// buildTeardownProvider builds a provider from the config file and options; a
// variable only so tests can see what the teardown builds, and with which
// options, without a cloud.
var buildTeardownProvider = helpers.BuildProvider

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

func pluralVerb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// providedNeighbours lists the environments in cfg whose cluster is PROVIDED
// (clusterMode: provided) and may run where an environment on providerName in
// region does: the same cloud — or `custom`, which says nothing about where its
// hosts are — in the same region, or in one the file does not name (a provided
// environment needs no region at all).
//
// The platform owns nothing in a provided cluster, its volumes included, and
// those volumes carry no install id: to a region-wide search they look exactly
// like a deleted cluster's leftovers. Every evidence-based deletion — the legacy
// offer, --purge-orphaned-volumes — is therefore off wherever one may run.
// Volumes found by an install id are unaffected: the id proves whose they are.
func providedNeighbours(cfg *config.Config, providerName, region string) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for name, env := range cfg.ResolvedEnvironments {
		if env == nil {
			continue
		}
		mode, err := environmentClusterMode(env)
		if err == nil && pfactory.ClusterLifecycleIsOurs(mode) {
			continue // ours: its volumes carry its own id
		}
		// An unparseable mode is refused by the teardown loop; here it counts as
		// provided, the reading under which nothing is deleted.
		if !sameCloud(env, providerName) {
			continue
		}
		if r := strings.TrimSpace(env.ResolvedRegion); r != "" && region != "" && !strings.EqualFold(r, region) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// sameCloud reports whether env's cluster may live on providerName: the same
// provider key or type, or the `custom` type, whose hosts can be anywhere.
func sameCloud(env *config.ResolvedEnvironmentConfig, providerName string) bool {
	if strings.EqualFold(env.ResolvedProvider, providerName) {
		return true
	}
	if env.ProviderConfig == nil {
		return false
	}
	t := strings.ToLower(strings.TrimSpace(env.ProviderConfig.Type))
	return t == "custom" || (t != "" && strings.EqualFold(t, providerName))
}

// volumesGoWithTheCluster reports whether deleting env's cluster deletes its
// block volumes too, before the volume step could list or keep them.
//
// Today that is AKS: the Azure disk CSI driver creates a managed cluster's
// disks in its NODE resource group (MC_<group>_<cluster>_<location>), and
// Azure deletes that group, every disk in it, together with the cluster. There
// is nothing left to list afterwards, and --keep-volumes cannot be honoured —
// so the teardown refuses that combination before deleting anything, and the
// confirmation says so instead of promising a list. Every other managed service
// (EKS, GKE, DOKS, Civo) leaves its volumes behind for the step.
func volumesGoWithTheCluster(env *config.ResolvedEnvironmentConfig, mode string) bool {
	if env == nil || !pfactory.ClusterModeIsManaged(mode) {
		return false
	}
	if strings.EqualFold(env.ResolvedProvider, globals.CloudProviderAzure) {
		return true
	}
	return env.ProviderConfig != nil && strings.EqualFold(strings.TrimSpace(env.ProviderConfig.Type), globals.CloudProviderAzure)
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
