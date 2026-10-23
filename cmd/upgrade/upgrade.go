/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package upgrade implements `adhar upgrade` (roadmap P1.6): converge the
// foundation components to this binary's embedded manifests, then present the
// diff between the local platform stack and the in-cluster GitOps repositories
// for review before force-pushing and re-syncing.
package upgrade

import (
	"bufio"
	"context"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/cmd/version"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/controllers"
	"adhar-io/adhar/platform/controllers/adharplatform"
	"adhar-io/adhar/platform/k8s"
	"adhar-io/adhar/platform/utils"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	assumeYes      bool
	diffOnly       bool
	skipFoundation bool
	stackDirFlag   string
	platformName   string
)

// UpgradeCmd represents the upgrade command
var UpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "→ Upgrade the platform: converge foundation, review stack diff, sync",
	Long: `→ **Adhar Platform Upgrade**

Upgrades the platform on the current kubeconfig context in two phases:

1. **Converge foundation** — re-applies this binary's embedded manifests
   (Gateway API CRDs, Cilium, Gateway, ArgoCD, Gitea, Crossplane; CNPG in HA
   mode) with server-side apply. Unchanged manifests are no-ops; changed
   components roll to the versions this release ships.
2. **Stack diff & sync** — compares the local platform stack (pre-rendered
   manifests, ADR-0004: the Git diff IS the cluster diff) against the
   in-cluster GitOps repositories and shows what would change. On
   confirmation it force-pushes the stack, re-applies the ApplicationSet and
   requests an ArgoCD refresh.

Run from the adhar repository root (or pass --stack-dir).`,
	Example: `  # Review and apply an upgrade interactively
  adhar upgrade

  # Show the stack diff only; change nothing
  adhar upgrade --diff-only

  # Non-interactive (CI)
  adhar upgrade --yes`,
	RunE:         runUpgrade,
	SilenceUsage: true,
}

func init() {
	UpgradeCmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Apply without interactive confirmation")
	UpgradeCmd.Flags().BoolVar(&diffOnly, "diff-only", false, "Show the stack diff and exit without changing anything")
	UpgradeCmd.Flags().BoolVar(&skipFoundation, "skip-foundation", false, "Skip foundation convergence; only diff/sync the stack")
	UpgradeCmd.Flags().StringVar(&stackDirFlag, "stack-dir", "platform/stack", "Path to the platform stack directory")
	UpgradeCmd.Flags().StringVar(&platformName, "name", globals.DefaultClusterName, "Name of the AdharPlatform resource")
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Wire controller-runtime's logger BEFORE touching any controller code.
	//
	// `adhar upgrade` calls the AdharPlatform reconciler's ApplyPlatformStack,
	// which calls log.FromContext. With no logger set, controller-runtime prints
	// a 30-line "log.SetLogger(...) was never called" goroutine dump into the
	// middle of the command's output — directly after "Pushing stack and
	// re-applying the platform ApplicationSet…", so a successful upgrade looked
	// like a crash. `adhar up` has always done this (cmd/up/bootstrap.go,
	// cmd/up/local.go); this command simply never did.
	//
	// Verbose routes the reconciler's own progress to stderr, which is what you
	// want when a push stalls. Otherwise discard: the CLI prints its own
	// progress, and klog's output here is noise the operator cannot act on.
	verbose, _ := cmd.Flags().GetBool("verbose")
	if verbose {
		stdr.SetVerbosity(1)
		ctrl.SetLogger(stdr.New(stdlog.New(os.Stderr, "", stdlog.LstdFlags)))
	} else {
		ctrl.SetLogger(logr.Discard())
		klog.SetOutput(io.Discard)
	}

	stackDir, err := filepath.Abs(stackDirFlag)
	if err != nil {
		return fmt.Errorf("resolving stack directory: %w", err)
	}
	if _, err := os.Stat(stackDir); err != nil {
		return fmt.Errorf("platform stack directory not found at %s (run from the adhar repository root or pass --stack-dir): %w", stackDir, err)
	}

	// Standard kubeconfig resolution: --kubeconfig, then $KUBECONFIG, then
	// ~/.kube/config — so the context `adhar up` merged (adhar-<cluster>) or
	// an explicit ~/.adhar/clusters/<name>/kubeconfig both work.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kc, _ := cmd.Flags().GetString("kubeconfig"); kc != "" {
		loadingRules.ExplicitPath = kc
	}
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	scheme := k8s.GetScheme()
	kubeClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	// Local platforms are named "adhar"; production platforms are named after
	// their environment. When the default name is absent but exactly one
	// AdharPlatform exists, use it instead of demanding --name.
	var platform v1alpha1.AdharPlatform
	if err := kubeClient.Get(ctx, types.NamespacedName{Name: platformName, Namespace: globals.AdharSystemNamespace}, &platform); err != nil {
		var list v1alpha1.AdharPlatformList
		if listErr := kubeClient.List(ctx, &list, client.InNamespace(globals.AdharSystemNamespace)); listErr != nil || len(list.Items) != 1 {
			return fmt.Errorf("reading AdharPlatform %s/%s (is a platform running on the current context? pass --name for non-default platforms): %w", globals.AdharSystemNamespace, platformName, err)
		}
		platform = list.Items[0]
		helpers.Printf("▸ Using AdharPlatform %q (the only platform on this cluster)\n", platform.Name)
	}

	tmpDir, err := os.MkdirTemp("", "adhar-upgrade-")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	reconciler := &adharplatform.AdharPlatformReconciler{
		Client:   kubeClient,
		Scheme:   scheme,
		Config:   platform.Spec.BuildCustomization,
		TempDir:  tmpDir,
		StackDir: stackDir,
		RepoMap:  utils.NewRepoLock(),
	}

	// ------------------------------------------------------------------
	// Phase 1: converge foundation (embedded manifests, SSA-idempotent).
	// ------------------------------------------------------------------
	if !skipFoundation && !diffOnly {
		if err := controllers.EnsureCRDs(ctx, scheme, kubeClient, platform.Spec.BuildCustomization); err != nil {
			return fmt.Errorf("updating platform CRDs: %w", err)
		}
		type step struct {
			name string
			run  func(context.Context, ctrl.Request, *v1alpha1.AdharPlatform) (ctrl.Result, error)
		}
		steps := []step{
			{"gateway-api-crds", reconciler.ReconcileGatewayAPICRDs},
			{"cilium", reconciler.ReconcileCilium},
			{"gateway", reconciler.ReconcileGateway},
		}
		if platform.Spec.BuildCustomization.EnableHAMode {
			steps = append(steps, step{"cnpg", reconciler.ReconcileCNPG})
		}
		steps = append(steps,
			step{"argocd", reconciler.ReconcileArgo},
			step{"gitea", reconciler.ReconcileGitea},
			step{"crossplane", reconciler.ReconcileCrossplane},
		)
		// The Crossplane reconciler applies the control-plane configuration
		// (XRDs, Compositions, Functions, provider packages, Operations) only
		// while ControlPlaneApplied is false — once per platform lifetime on
		// the steady-state controller. An upgrade exists precisely to roll a
		// newer configuration out, so clear the gate for this pass; every
		// apply is server-side and idempotent.
		platform.Status.Crossplane.ControlPlaneApplied = false

		// The SHARED checklist, not a bullet list.
		//
		// Each component used to be printed BEFORE it ran, so the output could
		// not distinguish "converged" from "started and then failed" — on a
		// failure you saw the bullet and an error and had to guess which of the
		// seven components the error belonged to. The tracker marks each one
		// done or failed with its own duration, which is also what `adhar up`
		// renders, so the two commands no longer look like different products.
		defs := make([]helpers.StageDef, 0, len(steps)+1)
		for _, st := range steps {
			defs = append(defs, helpers.StageDef{Label: st.name, Detail: "embedded manifest"})
		}
		defs = append(defs, helpers.StageDef{Label: "controller-manager", Detail: "in-cluster manager"})

		// os.Stderr and !verbose, matching `adhar up`: the animated frames belong
		// on stderr so piping the command's output stays clean, and a verbose run
		// prints the reconcilers' own logs that the animation would fight with.
		tracker := helpers.NewStageTracker(os.Stderr, "Converging foundation to "+version.Version, defs, !verbose)
		tracker.Start()
		// Stop() is idempotent, and a failure path returns without reaching the
		// explicit Stop below — without this the spinner goroutine outlives the
		// command and the terminal is left mid-frame.
		defer tracker.Stop()

		for i, st := range steps {
			tracker.Activate(i)
			if _, err := st.run(ctx, ctrl.Request{}, &platform); err != nil {
				tracker.Fail(i)
				tracker.Stop()
				return fmt.Errorf("converging %s: %w", st.name, err)
			}
			tracker.Done(i)
		}

		// The controller manager is part of the foundation too. It used to be
		// applied only by `adhar up`, so a release that changed the manager
		// Deployment (a new port, a volume, a flag) never reached an existing
		// cluster: the CLI upgraded every component except the one running the
		// controllers. Converge it here with the same embedded manifest.
		//
		// The IMAGE is preserved from the live Deployment rather than reset to
		// this release's default: an operator who pinned a tag did so on
		// purpose, and an upgrade should roll the manifest shape, not silently
		// change which image runs. A missing Deployment gets the release default.
		cmIdx := len(defs) - 1
		tracker.Activate(cmIdx)
		if err := controllers.EnsureControllerManager(ctx, kubeClient, controllers.ManagerConfig{
			Image:        liveControllerImage(ctx, kubeClient, platform.Namespace),
			Namespace:    platform.Namespace,
			PlatformName: platform.Name,
		}); err != nil {
			tracker.Fail(cmIdx)
			tracker.Stop()
			return fmt.Errorf("converging controller-manager: %w", err)
		}
		tracker.Done(cmIdx)
		tracker.Stop()
	}

	// ------------------------------------------------------------------
	// Phase 2: stack diff.
	// ------------------------------------------------------------------
	//
	// The same checklist as the foundation block: each repository resolves to a
	// ✓ with what it found, instead of a "Comparing…" line that never resolves
	// followed by unaligned counts.
	fmt.Fprintln(os.Stderr)
	diffTracker := helpers.NewStageTracker(os.Stderr, "Comparing with the in-cluster GitOps repos", []helpers.StageDef{
		{Label: "packages", Detail: "cloning from Gitea"},
		{Label: "environments", Detail: "cloning from Gitea"},
	}, !verbose)
	diffTracker.Start()
	changes, err := diffStack(ctx, kubeClient, platform.Spec.BuildCustomization, platform.Spec.Provider, stackDir, tmpDir, diffTracker)
	diffTracker.Stop()
	if err != nil {
		return fmt.Errorf("computing stack diff: %w", err)
	}
	diff := changes.counts()
	fmt.Print(renderStackChanges(changes))

	if diffOnly {
		return nil
	}
	if diff.any() && !assumeYes {
		fmt.Print(helpers.Indent(confirmPrompt(changes)))
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			helpers.Println(helpers.MutedStyle.Render("Aborted — nothing was pushed."))
			return nil
		}
	}

	// ------------------------------------------------------------------
	// Phase 3: push stack + re-apply ApplicationSet.
	// ------------------------------------------------------------------
	//
	// Say which of the two actually happens. The line was unconditionally
	// "Pushing stack and re-applying the platform ApplicationSet…", so a run
	// that had just printed "packages: no changes" still announced a push — and
	// then the closing report warned about ten minutes of fleet-wide churn that
	// could not occur, because a force push of identical content creates no
	// commit and ArgoCD sees no new revision. Verified live on 2026-10-11: a
	// no-op upgrade left the packages repo HEAD untouched.
	applyDefs := []helpers.StageDef{{Label: "applicationset", Detail: "re-applying"}}
	if diff.any() {
		applyDefs = []helpers.StageDef{{Label: "push + applicationset", Detail: pluralFiles(changes.total()) + " → Gitea"}}
	}
	fmt.Fprintln(os.Stderr)
	applyTracker := helpers.NewStageTracker(os.Stderr, "Applying the stack", applyDefs, !verbose)
	applyTracker.Start()
	applyTracker.Activate(0)
	if err := reconciler.ApplyPlatformStack(ctx, &platform); err != nil {
		applyTracker.Fail(0)
		applyTracker.Stop()
		return fmt.Errorf("applying platform stack: %w", err)
	}
	if diff.any() {
		applyTracker.DoneWith(0, helpers.MutedStyle.Render(pluralFiles(changes.total())+" pushed"))
	} else {
		applyTracker.Done(0)
	}
	applyTracker.Stop()
	fmt.Println()

	// Say what is about to happen, with a number.
	//
	// "ArgoCD is syncing the stack" understated it badly: ANY push to the
	// packages repo flips every Application OutOfSync, because they all track
	// one monorepo — roughly ten minutes of churn on a full production profile,
	// most of it apps that did not change. An operator who does not know that
	// reads the next `adhar get status` as the upgrade having broken the
	// platform, and the honest fix is to name the scale up front rather than to
	// hide it. Counted live rather than guessed, and silent if the count cannot
	// be read: a failed count must not look like a failed upgrade.
	//
	// But ONLY when something was pushed. This warning is load-bearing — it is
	// what stops an operator reading post-upgrade churn as breakage — and
	// printing it after "packages: no changes" spends that credibility on an
	// event that will not happen. A warning that cries wolf on every no-op run
	// is one an operator learns to skip past on the run where it is true.
	fmt.Print(upgradeOutcomeReport(diff, countApplications(ctx, kubeClient, platform.Namespace), skipFoundation))
	return nil
}

// upgradeOutcomeReport is the command's closing paragraph, returned as a string
// so both of its branches can be asserted without a cluster.
//
// Two things were wrong with it when it printed inline. The ten-minute churn
// warning fired for ANY push, but every Application tracks the PACKAGES
// monorepo and nothing tracks the environments repo except the Kargo projects
// and AppProjects built from it — so an environments-only push promised a
// fleet-wide re-comparison that never arrived (live 2026-10-12, pushing only
// `test/config.yaml`). And the wrapped sentences were hand-indented three
// spaces through bare `fmt.Println` while the bullet text sits at column four,
// so each continuation stepped one column left of the line it continued.
func upgradeOutcomeReport(diff stackDiff, apps int, skipFoundation bool) string {
	var b strings.Builder
	// bullet starts a sentence; cont continues one. Both go through the gutter
	// helper, so the single place that knows the gutter still owns it, and a
	// continuation is exactly two columns past it — under the bullet's text.
	bullet := func(format string, args ...any) {
		b.WriteString(helpers.Indent("● "+fmt.Sprintf(format, args...)) + "\n")
	}
	cont := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(helpers.Indent("  "+l) + "\n")
		}
	}

	if !diff.any() {
		bullet("Nothing to push — the stack is already in sync, so no application is")
		cont("re-comparing (identical content makes no commit, and ArgoCD only reacts",
			"to a new revision).")
		// Only claim the foundation ran when it did: --skip-foundation makes
		// this path the whole command, and saying otherwise would describe work
		// the operator explicitly asked to skip.
		if !skipFoundation {
			cont("The foundation was converged above.")
		}
		return b.String()
	}

	if diff.packagesChanged() {
		if apps > 0 {
			bullet("ArgoCD is re-comparing all %d applications — allow about 10 minutes.", apps)
		} else {
			bullet("ArgoCD is syncing the stack — allow about 10 minutes.")
		}
		cont("Every app tracks the packages repo, so even a one-file push",
			"flips every app OutOfSync for a while. That is expected, not a fault.")
		// Only the churn case gets the recovery advice. Telling an operator that
		// "a wave-stuck app needs its operation terminated" right after telling
		// them nothing is re-comparing is two sentences that contradict each
		// other, and it is advice for a situation they are not in.
		cont("Watch it with `adhar get status`; a wave-stuck app needs its operation",
			"terminated before it picks up the new revision.")
		return b.String()
	}

	bullet("Pushed — only the environments repo changed, so the application")
	cont("fleet is not re-comparing: nothing tracks that repo except the Kargo",
		"projects and AppProjects built from it.")
	return b.String()
}

// countApplications reports how many ArgoCD Applications live in the platform
// namespace, or 0 when that cannot be determined.
//
// Deliberately error-free: this exists only to put a number in a closing
// message, so an unreadable count degrades the sentence rather than the command.
func countApplications(ctx context.Context, kubeClient client.Client, namespace string) int {
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList",
	})
	if err := kubeClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return 0
	}
	return len(list.Items)
}

// diffStack clones the packages and environments repos from the in-cluster
// Gitea and diffs them against the local stack directory. It returns a
// human-readable summary and whether any differences exist.
// stackDiff records how many files differ per GitOps repository.
//
// Per-repo, not one boolean, because the two repos have very different
// consequences. Every Application tracks the PACKAGES monorepo, so a push there
// flips all of them OutOfSync; the ENVIRONMENTS repo is read by a handful of
// Kargo/AppProject objects and a one-file change in it churns nothing. Reporting
// a single "something differed" meant an environments-only push still warned
// about ten minutes of fleet-wide re-comparison (seen live 2026-10-12, pushing
// only `test/config.yaml`).
type stackDiff map[string]int

func (d stackDiff) any() bool {
	for _, n := range d {
		if n > 0 {
			return true
		}
	}
	return false
}

// packagesChanged reports whether the monorepo every Application tracks moved.
func (d stackDiff) packagesChanged() bool { return d["packages"] > 0 }

// fileChange is one path that differs between Gitea and the staged stack.
// Status is git's name-status letter, read as "what the push does to Gitea":
// A adds, M modifies, D deletes, R renames. Owner is set when the file's last
// commit in Gitea was made in-cluster (Kargo, the Marketplace) — the push would
// revert it.
type fileChange struct {
	Status string
	Path   string
	Owner  string
}

// repoChanges is one repository's differences, in the order git reported them.
type repoChanges struct {
	Repo  string
	Files []fileChange
}

type stackChanges []repoChanges

func (c stackChanges) counts() stackDiff {
	d := stackDiff{}
	for _, r := range c {
		if len(r.Files) > 0 {
			d[r.Repo] = len(r.Files)
		}
	}
	return d
}

func (c stackChanges) total() int {
	n := 0
	for _, r := range c {
		n += len(r.Files)
	}
	return n
}

// promotions lists the changed files whose last commit was made in-cluster.
func (c stackChanges) promotions() []string {
	var out []string
	for _, r := range c {
		for _, f := range r.Files {
			if f.Owner != "" {
				out = append(out, r.Repo+"/"+f.Path)
			}
		}
	}
	return out
}

// historyDepth is how much history each clone fetches. The packages repo is
// large and only its tree matters; the environments repo is tiny, and its
// history is how a Kargo-written file is recognised (a depth-1 clone only knows
// the tip commit).
func historyDepth(repo string) int {
	if repo == "environments" {
		return 50
	}
	// Enough to see who last wrote applicationset.yaml (a Marketplace toggle)
	// without paying for the packages repo's whole history.
	return 10
}

func diffStack(ctx context.Context, kubeClient client.Client, cfg v1alpha1.BuildCustomizationSpec, provider v1alpha1.EnvironmentProvider, stackDir, tmpDir string, tracker *helpers.StageTracker) (stackChanges, error) {
	creds, err := utils.GetSecretByName(ctx, kubeClient, utils.GiteaNamespace, utils.GiteaAdminSecret)
	if err != nil {
		return nil, fmt.Errorf("reading gitea credentials: %w", err)
	}
	username := string(creds.Data["username"])
	password := string(creds.Data["password"])

	// Where to actually send the connection. Empty means "trust public DNS",
	// which is only safe when public DNS is correct — and this command exists
	// partly to repair platforms where it is not.
	dialAddr := gatewayDialAddress(ctx, kubeClient)

	var out stackChanges
	for i, repo := range []string{"packages", "environments"} {
		tracker.Activate(i)
		fail := func(err error) (stackChanges, error) {
			tracker.Fail(i)
			return nil, err
		}
		cloneDir := filepath.Join(tmpDir, "repo-"+repo)
		if err := cloneGiteaRepo(ctx, cfg, username, password, repo, cloneDir, dialAddr, historyDepth(repo)); err != nil {
			return fail(err)
		}
		// Move clone metadata aside so the directory diff sees content only,
		// while history stays readable for the ownership lookup below.
		gitDir := cloneDir + ".git"
		if err := os.Rename(filepath.Join(cloneDir, ".git"), gitDir); err != nil {
			return fail(err)
		}

		// Compare against the stack as it would be seeded (templates rendered,
		// host convention rewritten), not the raw checkout.
		tracker.SetDetail(i, "comparing")
		staged, cleanup, err := adharplatform.StageStack(filepath.Join(stackDir, repo), cfg)
		if err != nil {
			return fail(fmt.Errorf("staging %s: %w", repo, err))
		}
		// Seeding adds the profile's ApplicationSet to the packages repo; the
		// diff must expect it too, or it would report the file as deleted.
		if repo == globals.GitOpsRepoPackages {
			if err := adharplatform.InjectPlatformAppSet(staged, stackDir, provider); err != nil {
				cleanup()
				return fail(err)
			}
		}
		names, err := gitDiffNames(ctx, cloneDir, staged)
		cleanup()
		if err != nil {
			return fail(err)
		}
		rc := repoChanges{Repo: repo, Files: classifyChanges(ctx, gitDir, names)}
		out = append(out, rc)
		result := helpers.MutedStyle.Render("no changes")
		if len(rc.Files) > 0 {
			result = helpers.WarningStyle.Render(pluralFiles(len(rc.Files)) + " differ")
			if len(rc.Files) == 1 {
				result = helpers.WarningStyle.Render("1 file differs")
			}
		}
		tracker.DoneWith(i, result)
	}
	return out, nil
}

// classifyChanges turns gitDiffNames lines into changes, labelling each file
// whose last commit in Gitea (history at gitDir) was made in-cluster.
func classifyChanges(ctx context.Context, gitDir string, names []string) []fileChange {
	var out []fileChange
	for _, n := range names {
		fc := parseNameStatus(n)
		// Only a file Gitea already has can carry someone else's commit.
		if !strings.HasPrefix(fc.Status, "A") {
			fc.Owner = inClusterChange(ctx, gitDir, fc.Path)
		}
		out = append(out, fc)
	}
	return out
}

// parseNameStatus reads one gitDiffNames line ("M  path", "R100  old  new").
func parseNameStatus(line string) fileChange {
	fields := strings.Split(line, "  ")
	fc := fileChange{Status: fields[0]}
	switch len(fields) {
	case 1:
	case 2:
		fc.Path = fields[1]
	default:
		fc.Path = fields[1] + " → " + fields[len(fields)-1]
	}
	return fc
}

// inClusterChange describes the commit that last wrote path when that commit
// was made INSIDE the cluster — a Kargo promotion or a Marketplace toggle — or
// returns "".
//
// Two things edit the GitOps repos in-cluster by design: Kargo commits
// `promote: dev -> test` into the environments repo, and the console's
// Marketplace commits `chore(marketplace): enable …` to applicationset.yaml in
// the packages repo. The upgrade compares the whole tree, lists those files as
// differing, and pushing reverts them — which happened on 2026-10-15 and
// 2026-10-23 with nothing on screen to say the "change" was not drift.
// Best effort: unreadable history labels nothing.
func inClusterChange(ctx context.Context, gitDir, path string) string {
	out, err := exec.CommandContext(ctx, "git", "--git-dir", gitDir, "log", "-1",
		"--format=%an%x1f%s", "--", path).Output()
	if err != nil {
		return ""
	}
	author, subject, ok := strings.Cut(strings.TrimSpace(string(out)), "\x1f")
	if !ok {
		return ""
	}
	switch {
	case strings.Contains(strings.ToLower(author), "kargo"):
		return "Kargo (" + strconv.Quote(subject) + ")"
	case strings.HasPrefix(subject, "chore(marketplace):"):
		return "the Marketplace (" + strconv.Quote(subject) + ")"
	}
	return ""
}

func pluralFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// changeGlyph renders git's status letter as what the push does to Gitea.
func changeGlyph(status string) string {
	switch {
	case strings.HasPrefix(status, "A"):
		return helpers.SuccessStyle.Render("+")
	case strings.HasPrefix(status, "D"):
		return helpers.ErrorStyle.Render("−")
	case strings.HasPrefix(status, "R"), strings.HasPrefix(status, "C"):
		return helpers.InfoStyle.Render("→")
	default:
		return helpers.WarningStyle.Render("~")
	}
}

// renderStackChanges is the file list printed under the comparison checklist,
// returned as a string so its layout can be asserted without a cluster.
// Repositories without changes are already reported by the checklist itself.
func renderStackChanges(c stackChanges) string {
	if c.total() == 0 {
		return ""
	}
	const maxShown = 40
	var b strings.Builder
	b.WriteString("\n")
	for _, r := range c {
		if len(r.Files) == 0 {
			continue
		}
		b.WriteString(helpers.Indent(helpers.BoldStyle.Render(r.Repo)+helpers.MutedStyle.Render("  "+pluralFiles(len(r.Files)))) + "\n")
		for i, f := range r.Files {
			if i == maxShown {
				b.WriteString(helpers.Indent(helpers.MutedStyle.Render(fmt.Sprintf("  … and %d more", len(r.Files)-maxShown))) + "\n")
				break
			}
			b.WriteString(helpers.Indent("  "+changeGlyph(f.Status)+" "+f.Path) + "\n")
			if f.Owner != "" {
				b.WriteString(helpers.Indent("    "+helpers.WarningStyle.Render(
					fmt.Sprintf("%s changed in-cluster by %s — pushing reverts it", helpers.IconDegraded, f.Owner))) + "\n")
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}

// confirmPrompt names what the push does, and says so louder when it would
// undo a Kargo promotion.
func confirmPrompt(c stackChanges) string {
	if p := c.promotions(); len(p) > 0 {
		what := "1 file was changed in-cluster (Kargo or the Marketplace) — pushing rolls it back."
		if len(p) > 1 {
			what = fmt.Sprintf("%d files were changed in-cluster (Kargo or the Marketplace) — pushing rolls them back.", len(p))
		}
		return helpers.WarningStyle.Render(helpers.IconDegraded+" "+what) +
			"\n" + helpers.Gutter + fmt.Sprintf("Push %s to Gitea anyway? [y/N]: ", pluralFiles(c.total()))
	}
	return fmt.Sprintf("Push %s to Gitea and sync? [y/N]: ", pluralFiles(c.total()))
}

// cloneGiteaRepo clones a repo from the in-cluster Gitea over its external URL
// using the admin credentials. TLS verification is disabled because the
// platform certificate is self-signed by default.
// gatewayDialAddress returns the gateway's load-balancer IP, read from the
// cluster through the kubeconfig — the one path that cannot be broken by a DNS
// fault. Empty when there is nothing usable, in which case the clone falls back
// to resolving the hostname normally.
//
// Only an IP is useful here: curl's resolve override maps a host:port to an
// ADDRESS, so a provider that hands out a load-balancer hostname (AWS) is left
// to DNS, which for that provider is the thing that works.
func gatewayDialAddress(ctx context.Context, kubeClient client.Client) string {
	var svcs corev1.ServiceList
	if err := kubeClient.List(ctx, &svcs, client.InNamespace(globals.AdharSystemNamespace)); err != nil {
		return ""
	}
	for _, svc := range svcs.Items {
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer || !strings.HasPrefix(svc.Name, "cilium-gateway-") {
			continue
		}
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if ip := net.ParseIP(ing.IP); ip != nil {
				return ing.IP
			}
		}
	}
	return ""
}

func cloneGiteaRepo(ctx context.Context, cfg v1alpha1.BuildCustomizationSpec, username, password, repo, dest string, dialAddr string, depth int) error {
	host := cfg.IngressHost
	if host == "" {
		host = cfg.Host
	}
	// Always the subdomain form: every HTTPRoute serves per-app subdomains
	// regardless of the CR's legacy usePathRouting flag (observed live: the
	// path-routed /gitea/... URL does not exist).
	base := url.URL{
		Scheme: cfg.Protocol,
		Host:   fmt.Sprintf("gitea.%s:%s", host, cfg.Port),
		Path:   fmt.Sprintf("/%s/%s.git", globals.GiteaPlatformOrg, repo),
		User:   url.UserPassword(username, password),
	}

	// credential.helper= (empty) disables the DEVELOPER's configured helpers for this
	// one clone. The URL already carries the platform's credential, and a helper
	// such as osxkeychain or git-credential-manager can return a STALE entry for the
	// same host — after the platform rotates the Gitea admin password
	// (security/adhar-credential-rotation) that cached value is wrong, and the clone
	// fails with an opaque
	//     remote: Access denied … 403
	// that looks like a platform fault and reproduces on one machine but not another.
	// GIT_TERMINAL_PROMPT=0 keeps a failure a failure instead of a hung prompt.
	args := []string{"-c", "credential.helper=", "-c", "http.sslVerify=false"}
	// Send the connection straight at the gateway instead of trusting public
	// DNS for the platform's own hostname.
	//
	// `adhar upgrade` is how a broken platform gets repaired, so it must not
	// depend on the platform being healthy. A stale external-dns record made
	// this command fail before it could do anything:
	//
	//	Error: computing stack diff: cloning gitea repo "packages": fatal: unable
	//	to access 'https://gitea.cloud.adhar.io:443/adhar/packages.git/': Failed
	//	to connect to gitea.cloud.adhar.io port 443 after 75088 ms
	//
	// The records pointed at a previous cluster's dead load-balancer IP, and the
	// fix for that lives in the stack this command pushes — so the only way to
	// apply it was to repair DNS by hand first. TLS verification is already off
	// above, so the certificate's name is not an obstacle either.
	if dialAddr != "" && cfg.Port != "" {
		args = append(args, "-c", fmt.Sprintf("http.curloptResolve=gitea.%s:%s:%s", host, cfg.Port, dialAddr))
	}
	args = append(args, "clone", "--quiet", "--depth", strconv.Itoa(depth), base.String(), dest)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		// Never echo the URL (it embeds credentials).
		return fmt.Errorf("cloning gitea repo %q: %w: %s", repo, err, sanitize(string(out), password))
	}
	return nil
}

// gitDiffNames returns the paths that differ between two directory trees using
// `git diff --no-index`. Exit code 1 means "differences found".
func gitDiffNames(ctx context.Context, a, b string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-index", "--name-status", a, b)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("git diff failed: %w", err)
		}
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Lines look like "M\t/tmp/.../path"; strip the temp prefixes for
		// readability.
		fields := strings.SplitN(line, "\t", 3)
		for i := 1; i < len(fields); i++ {
			fields[i] = strings.TrimPrefix(strings.TrimPrefix(fields[i], a+"/"), b+"/")
		}
		names = append(names, strings.Join(fields, "  "))
	}
	return names, nil
}

// sanitize removes a credential from command output before it reaches logs.
func sanitize(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}

// liveControllerImage returns the image the in-cluster controller manager runs
// today, or this release's default when none is deployed. See the comment at
// the call site for why the live value wins.
func liveControllerImage(ctx context.Context, c client.Client, namespace string) string {
	deployed := ""
	var dep appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "adhar-controller-manager"}, &dep); err == nil {
		for _, ct := range dep.Spec.Template.Spec.Containers {
			if ct.Name == "manager" && ct.Image != "" {
				deployed = ct.Image
			}
		}
	}
	// Preferring the deployed image is right while it WORKS. An image that
	// cannot be pulled is not a choice worth preserving: an upgrade run to fix a
	// manager stuck in ImagePullBackOff would otherwise reinstall the same
	// unpullable tag (see helpers.ResolveControllerImage for the live incident).
	if deployed != "" {
		return helpers.ResolveControllerImage(ctx, deployed, helpers.DefaultControllerImage(version.Version),
			helpers.ControllerImageExists, func(msg string) { fmt.Println(helpers.CreateWarning(msg)) }, nil)
	}
	// Same rule as `adhar up`, from the same place.
	return helpers.DefaultControllerImage(version.Version)
}
