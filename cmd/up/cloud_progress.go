package up

import (
	"fmt"
	stdlog "log"
	"os"
	"strings"
	"sync"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"
)

// cloud_progress.go gives a cloud or on-prem `adhar up` the same checklist the
// local Kind path has.
//
// Before this, the cloud path printed whatever each provider happened to log —
// raw `log.Printf` lines with their own `2026/10/01 00:41:35` stamps, interleaved
// with the structured logger's, for twenty minutes with no sense of where it was:
//
//	2026/10/01 00:41:35 DigitalOcean VPC config: CIDR=10.3.0.0/16, existingVPC=
//	2026/10/01 00:41:35 DigitalOcean tags: [adhar adhar-mgmt]
//	[...] INFO: Creating cluster 'dev' using digitalocean provider in region blr1
//
// The stages after the third are the ones the controller drives, and they are
// identical on every provider — same components, same order — so the same poller
// advances them here as locally (pollPlatformStages, base
// cloudControllerStageBase). Only the first three differ, because a cloud has a
// preflight and a real cluster to build where Kind has neither.

// cloudControllerStageBase is where the controller-driven stages start on the
// cloud checklist: after preflight (0), the cluster (1) and kubeconfig/TLS (2).
// It matches localControllerStageBase, which is a coincidence worth keeping —
// both paths do exactly three things before handing over to the controller.
const cloudControllerStageBase = 3

// cloudStages is the checklist for a cloud/on-prem bring-up. The tail must stay
// in the order pollPlatformStages advances it.
//
// The Cloud cluster detail names the cluster, the provider and the region because
// the log line that used to say all three —
//
//	INFO: Creating cluster 'adhar' for environment 'dev' using digitalocean
//	      provider in region blr1
//
// was printed directly above this checklist and is gone (demoted to debug). If the
// stage does not carry that, removing the line loses it: the cluster NAME in
// particular is no longer implied by the environment now that `--name` exists.
func cloudStages(clusterName, providerName, region string) []helpers.StageDef {
	return []helpers.StageDef{
		{Label: "Preflight", Detail: "credentials, quota and API access"},
		{Label: "Cloud cluster", Detail: clusterTargetLabel(clusterName, providerName, region)},
		{Label: "Kubeconfig & TLS", Detail: "context, certificates, edge DNS"},
		// ── everything below is controller-driven; see cloudControllerStageBase ──
		{Label: "Cilium & Gateway", Detail: "eBPF data path"},
		{Label: "ArgoCD", Detail: "GitOps engine"},
		{Label: "Gitea", Detail: "in-cluster Git server"},
		{Label: "GitOps repos", Detail: "packages, environments"},
		{Label: "Crossplane", Detail: "control plane + providers"},
		{Label: "GitOps sync - platform stack", Detail: "platform apps via ArgoCD"},
	}
}

// startCloudProgress builds the checklist and decides where each stream of output
// goes, so what is left on screen is steps and progress.
//
// The two streams are not equivalent and are treated differently:
//
//   - The STRUCTURED logger carries things the platform chose to say — the cluster
//     it is creating, the kubeconfig it wrote, warnings, failures. Those scroll
//     above the checklist through a TrackerWriter, which keeps the animated block
//     from being torn apart.
//   - The standard `log` package carries each provider's internal chatter, printed
//     with its own `2026/10/01 01:58:12` stamps: "DigitalOcean VPC config:
//     CIDR=10.3.0.0/16, existingVPC=", "Found 0 DigitalOcean clusters", and a dozen
//     more before anything happens. That is debugging detail, not progress, and it
//     buried the checklist under fifteen lines of it. It is BUFFERED and printed
//     only if provisioning fails, where it is exactly what you need.
//
// `--verbose` opts out of all of it: the tracker degrades to plain lines and both
// streams go straight to the terminal in file order.
//
// The returned func restores both streams and finalises the block. Call it on every
// return path (defer it), passing whether the run failed — a failure both dumps the
// buffered detail and stops the block, and leaving the block live means the next
// write orphans a copy of it (see the note on tracker.Stop in local.go).
func startCloudProgress(envName, clusterName, providerName, region string, verbose bool) (*helpers.StageTracker, func(failed bool)) {
	stages := cloudStages(clusterName, providerName, region)
	if got := stages[cloudControllerStageBase].Label; got != "Cilium & Gateway" {
		panic(fmt.Sprintf("cloudControllerStageBase points at %q, not the first controller-driven stage", got))
	}

	// The environment goes in the TITLE: a run with no --env provisions each one in
	// turn and prints a block per environment, so without it two blocks are
	// indistinguishable. It also replaces the removed "for environment 'dev'".
	title := "Provisioning Adhar platform"
	if envName != "" {
		title = fmt.Sprintf("Provisioning %s", envName)
	}
	tracker := helpers.NewStageTracker(os.Stderr, title, stages, !verbose)
	tracker.Start()

	if verbose {
		return tracker, func(bool) { tracker.Stop() }
	}

	tw := helpers.NewTrackerWriter(tracker)
	prevLogOut := logger.Output()
	logger.SetOutput(tw)

	detail := &strings.Builder{}
	prevStd := stdlog.Writer()
	stdlog.SetOutput(detail)

	return tracker, func(failed bool) {
		tw.Flush()
		logger.SetOutput(prevLogOut)
		stdlog.SetOutput(prevStd)
		tracker.Stop()
		if failed && detail.Len() > 0 {
			// Indented and dimmed: this is evidence attached to the failure above,
			// not a second stream of output competing with it.
			dim := helpers.SubtitleStyle.UnsetMarginLeft()
			var out strings.Builder
			out.WriteString("\n" + dim.Render("  provider detail") + "\n")
			for _, line := range strings.Split(strings.TrimRight(detail.String(), "\n"), "\n") {
				out.WriteString(dim.Render("    "+line) + "\n")
			}
			fmt.Fprint(os.Stderr, out.String())
		}
	}
}

// syncProgress carries the last convergence counts the poller saw to whoever
// finalises the checklist.
//
// It exists because `adhar up` no longer waits for every application — it hands
// over once the console is serving (see ConvergenceReport.Usable) — so the GitOps
// stage is routinely ticked with a tail still outstanding. A green tick beside
// "26/75 apps Synced + Healthy" reads as a contradiction, so the finaliser needs
// the numbers to say what the tick actually means.
type syncProgress struct {
	mu             sync.Mutex
	healthy, total int
}

func (p *syncProgress) set(healthy, total int) {
	p.mu.Lock()
	p.healthy, p.total = healthy, total
	p.mu.Unlock()
}

func (p *syncProgress) get() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.healthy, p.total
}

// finalGitOpsDetail is the text left on the GitOps stage when the run finishes.
// A complete sync says so; anything else names what is still in flight rather
// than letting a tick imply the whole catalogue is up.
func finalGitOpsDetail(healthy, total int) string {
	switch {
	case total == 0:
		return "no platform apps reported"
	case healthy >= total:
		return fmt.Sprintf("%d/%d apps Synced + Healthy", healthy, total)
	default:
		return fmt.Sprintf("%d/%d ready — ArgoCD continues in the background", healthy, total)
	}
}

// trackPhases maps provisioning milestones onto the checklist. One stage active at
// a time: the CLI used to mark Preflight and Cloud cluster active together, because
// from outside ProvisionEnvironment is one opaque call, and both spinners ran at
// once.
func trackPhases(tracker *helpers.StageTracker, target string) func(pfactory.Phase) {
	return func(phase pfactory.Phase) {
		switch phase {
		case pfactory.PhasePreflightDone:
			tracker.Done(0)
			tracker.Activate(1)
		case pfactory.PhaseClusterCreating:
			// Already active. The detail keeps naming the target (it is the only place
			// the cluster name now appears) and adds what it is waiting for, since
			// this is the longest single step of a cloud bring-up.
			tracker.SetDetail(1, target+" — creating machines, this takes a few minutes")
		case pfactory.PhaseClusterReady:
			tracker.Done(1)
			tracker.Activate(2)
		}
	}
}

// clusterTargetLabel is the "name · provider · region" the Cloud cluster stage
// shows. One builder, because the stage list and the phase callback both render it
// and a second copy is how they drift.
func clusterTargetLabel(clusterName, providerName, region string) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{clusterName, providerName, region} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}
