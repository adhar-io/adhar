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

package up

import (
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	applyconfigcorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"

	// Import providers so their init() functions register with DefaultFactory
	_ "adhar-io/adhar/platform/providers/aws"
	_ "adhar-io/adhar/platform/providers/azure"
	_ "adhar-io/adhar/platform/providers/civo"
	_ "adhar-io/adhar/platform/providers/custom"
	_ "adhar-io/adhar/platform/providers/digitalocean"
	_ "adhar-io/adhar/platform/providers/gcp"
	_ "adhar-io/adhar/platform/providers/kind"

	"github.com/spf13/cobra"
)

// createProductionCluster handles production cluster provisioning using the new ProviderManager
func createProductionCluster(ctx context.Context, cmd *cobra.Command, args []string, ctxCancel context.CancelFunc) error {
	// Validate config file exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		return fmt.Errorf("configuration file not found: %s", configFile)
	}

	// Load configuration from file
	cfg, err := loadConfigFromFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Initialize enhanced logger
	log := logger.GetLogger()
	if verbose {
		log.SetLevel(logger.DEBUG)
	}

	// Create provider manager for production operations
	providerManager := pfactory.NewProviderManager(pfactory.DefaultFactory)

	// No banner here. The `adhar up` header (mode / config / env) has already
	// printed, and the checklist carries its own title — a third and fourth
	// announcement of "Adhar Platform / Provisioning …" before any work started
	// just pushed the useful output down the screen.

	// If no environment specified, provision the complete platform
	if environment == "" {
		return provisionCompletePlatformNew(ctx, providerManager, cfg, dryRun, force)
	}

	// Get environment configuration
	envConfig, err := resolveEnvironmentConfig(cfg, environment)
	if err != nil {
		return fmt.Errorf("failed to resolve environment configuration: %w", err)
	}
	applyKubeVersionOverride(envConfig)

	// If dry run, show what would be provisioned
	if dryRun {
		return showDryRunInfo(envConfig)
	}

	// One checklist for the whole run, same as the local path. Every provider's own
	// log.Printf output is routed into it, so it scrolls above the block.
	//
	// The name is resolved the same way provisioning resolves it, so the checklist
	// names the cluster that will actually be built.
	resolvedName := pfactory.ResolveClusterName(clusterName)
	clusterTarget := clusterTargetLabel(resolvedName, envConfig.ResolvedProvider, envConfig.ResolvedRegion)
	tracker, restoreProgress := startCloudProgress(envConfig.Name, resolvedName,
		envConfig.ResolvedProvider, envConfig.ResolvedRegion, verbose)
	// The buffered provider detail is only worth printing when something went
	// wrong, so the deferred restore needs to know the outcome.
	provisionFailed := false
	defer func() { restoreProgress(provisionFailed) }()

	tracker.Activate(0) // Preflight — the provider validates credentials and quota
	// Set provision options
	provisionOpts := pfactory.ProvisionOptions{
		DryRun:      dryRun,
		Force:       force,
		Recreate:    recreateCluster,
		ClusterName: clusterName,
		OnPhase:     trackPhases(tracker, clusterTarget),
	}

	// Stages 0-2 are advanced by OnPhase as provisioning reports each milestone.
	result, err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts)
	if err != nil {
		tracker.Fail(1)
		provisionFailed = true
		// Deliberately NOT logged as well as returned. The returned error already
		// names the environment and the provider, and — for an access failure — it
		// carries the remedy ExplainAccessError attached. Logging it here too
		// printed the whole thing a second time, pushing the "→ what to do next"
		// line out of sight.
		return fmt.Errorf("failed to provision environment %s (provider %s): %w",
			environment, envConfig.ResolvedProvider, err)
	}

	// Bootstrap the platform (foundation + GitOps stack + in-cluster controller
	// manager) on the freshly provisioned cluster — same flow as local, sized
	// by enableHAMode.
	if result != nil {
		if err := bootstrapPlatformOnCluster(ctx, result, envConfig, cfg, tracker); err != nil {
			provisionFailed = true
			return fmt.Errorf("failed to bootstrap platform on environment %s: %w", environment, err)
		}
	}

	log.FinishOperation("Environment Provisioning", fmt.Sprintf("%s environment ready", environment))

	// Print success message
	clusterName := result.Cluster.Name
	if clusterName == "" {
		clusterName = result.Cluster.ID
	}
	printProductionSuccessMsg(environment, cfg.GlobalSettings.DefaultHost, clusterName)
	return nil
}

// createEnvironmentNamespaces materialises the namespace-isolated environments
// on the shared cluster.
//
// This is what `isolation: namespace` actually produces: one namespace per
// environment, labelled so the Console, the namespace views and any policy can
// tell an environment apart from a platform namespace. Idempotent, because
// re-running `adhar up` must not fail on its own previous work.
func createEnvironmentNamespaces(ctx context.Context, result *pfactory.ProvisionResult, envs []string) error {
	kubeconfigStr, err := result.Provider.GetKubeconfig(ctx, result.Cluster.ID)
	if err != nil {
		return fmt.Errorf("retrieving kubeconfig: %w", err)
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigStr))
	if err != nil {
		return fmt.Errorf("building client config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building clientset: %w", err)
	}

	labels := func(env string) map[string]string {
		return map[string]string{
			"adhar.io/environment": env,
			// `workload`, not `control`: these hold the team's software. The
			// platform itself lives in adhar-system.
			"adhar.io/plane":               "workload",
			"app.kubernetes.io/managed-by": "adhar",
		}
	}

	for _, env := range envs {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: env, Labels: labels(env)},
		}
		_, cerr := cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		switch {
		case cerr == nil:
			// Silent on success. A line per namespace restated what the config
			// already says, three times, directly under a checklist that had just
			// reported the environment provisioned.
		case apierrors.IsAlreadyExists(cerr):
			if _, perr := cs.CoreV1().Namespaces().Apply(ctx,
				applyconfigcorev1.Namespace(env).WithLabels(labels(env)),
				metav1.ApplyOptions{FieldManager: "adhar-cli", Force: true}); perr != nil {
				return fmt.Errorf("relabelling existing namespace %s: %w", env, perr)
			}
		default:
			return fmt.Errorf("creating namespace %s: %w", env, cerr)
		}
	}
	return nil
}

// printSharedClusterPlan explains, before any work starts, that the environments
// are namespaces on one cluster.
//
// Silent on the LOCAL provider. A Kind cluster is one cluster by definition —
// the whole local flow is a single `adhar` cluster on the operator's laptop — so
// announcing that environments share it states the obvious, and the advice to
// set `isolation: cluster` is worse than obvious: it offers a second Kind
// cluster as if that were a sensible local setup. Nothing about the local run
// is affected by the setting, so nothing about it is worth saying.
//
// The earlier wording was
//
//	▸ 3 environments share one cluster (dev, prod, test); sizing it from "prod"
//	     Give an environment `isolation: cluster` in the config to split it out.
//
// which read as a warning about a constraint rather than a statement of the
// plan: "share one cluster" sounds like a compromise, "sizing it from" is jargon
// for a decision the reader has not been told exists, and "split it out" does
// not say what is split or where it goes. It says what will be BUILT now.
func printSharedClusterPlan(cfg *config.Config, nsEnvs []string, sharedEnv string) {
	if isLocalProviderEnv(cfg, sharedEnv) {
		return
	}
	fmt.Printf("  %s Building ONE cluster; %s become namespaces on it.\n",
		helpers.InfoStyle.Render("▸"), strings.Join(nsEnvs, ", "))
	fmt.Printf("     To give an environment its own cluster instead, set `isolation: cluster`\n")
	fmt.Printf("     on it in the config.\n\n")
}

// isLocalProviderEnv reports whether the environment runs on the local (Kind)
// provider.
func isLocalProviderEnv(cfg *config.Config, envName string) bool {
	ec, err := resolveEnvironmentConfig(cfg, envName)
	if err != nil {
		// Unknown provider: say the thing rather than suppress it. A missing
		// message is harder to notice than a redundant one.
		return false
	}
	return ec.ResolvedProvider == globals.CloudProviderKind
}

// partitionEnvironments splits the selected environments by isolation mode.
func partitionEnvironments(cfg *config.Config, names []string) (clusterEnvs, nsEnvs []string, err error) {
	for _, n := range names {
		ec, rerr := resolveEnvironmentConfig(cfg, n)
		if rerr != nil {
			return nil, nil, fmt.Errorf("resolving environment %s: %w", n, rerr)
		}
		if ec.ResolvedIsolation == config.EnvironmentIsolationCluster {
			clusterEnvs = append(clusterEnvs, n)
		} else {
			nsEnvs = append(nsEnvs, n)
		}
	}
	return clusterEnvs, nsEnvs, nil
}

// sharedClusterEnvironment picks which namespace environment's shape sizes the
// one shared cluster.
//
// The production-type environment when there is one, because the shared cluster
// has to carry production's load and sizing it from `dev` would under-provision
// everything. Otherwise the first in sorted order, which is at least stable
// between runs — a different choice per run would make two runs of the same file
// produce differently sized clusters.
func sharedClusterEnvironment(cfg *config.Config, nsEnvs []string) string {
	for _, n := range nsEnvs {
		if ec, err := resolveEnvironmentConfig(cfg, n); err == nil &&
			ec.ResolvedType == config.EnvironmentTypeProduction {
			return n
		}
	}
	return nsEnvs[0]
}

// envClusterName gives each environment of a MULTI-environment run its own
// cluster, while leaving a single-environment run exactly as it was.
//
// `ResolveClusterName` only ever returns --name or the default "adhar", so every
// environment in one `adhar up` targeted the SAME cluster. Provisioning
// dev/test/prod from one config therefore built `adhar` for dev and then
// reported, twice:
//
//	INFO: Cluster 'adhar' already exists (gcp/adhar-cloud/adhar, status running)
//	      — reusing it; pass --recreate to build a fresh one
//
// Three environments silently sharing one cluster is worse than a failure: each
// bootstrap would overwrite the previous one's platform while the summary
// claimed success. The DigitalOcean example's own comment already stated the
// intent — "the CLUSTER is named after the environment" — but nothing
// implemented it.
//
// Single environment keeps the plain default, because that is the common case
// and "default name keep adhar" is the stated preference. Only a multi-
// environment run suffixes, where a shared name cannot work at all. An explicit
// --name becomes the PREFIX so the operator's choice is still honoured.
func envClusterName(override, envName string, total int) string {
	if total <= 1 {
		return override
	}
	base := override
	if base == "" {
		base = globals.DefaultClusterName
	}
	return base + "-" + envName
}

// loadConfigFromFile loads configuration from a specific file path using Viper
// (which understands mapstructure tags used by the Config struct)
func loadConfigFromFile(configPath string) (*config.Config, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	// Resolve environment configurations (merge templates, assign providers)
	if err := cfg.ResolveEnvironments(); err != nil {
		return nil, fmt.Errorf("failed to resolve environments: %w", err)
	}

	return cfg, nil
}

// applyKubeVersionOverride lets `adhar up --kube-version` decide the Kubernetes
// version for every provider, not just Kind. Precedence, highest first:
//
//  1. an explicitly passed --kube-version,
//  2. the environment's own `kubeVersion` / `version` clusterConfig entry,
//  3. globals.DefaultKubernetesVersion (applied in buildClusterSpec).
//
// The flag carries the platform default as its *default value*, so only an
// explicit `Changed` may override what the environment configured — otherwise
// every cluster would silently be pinned to the CLI's compiled-in version.
func applyKubeVersionOverride(envConfig *config.ResolvedEnvironmentConfig) {
	if envConfig == nil || !kubeVersionExplicit {
		return
	}
	for i, kv := range envConfig.ResolvedClusterConfig {
		if kv.Key == "kubeVersion" || kv.Key == "version" {
			envConfig.ResolvedClusterConfig[i].Value = kubeVersion
			return
		}
	}
	envConfig.ResolvedClusterConfig = append(envConfig.ResolvedClusterConfig,
		config.KeyValueConfig{Key: "kubeVersion", Value: kubeVersion})
}

// resolveEnvironmentConfig resolves a specific environment configuration
func resolveEnvironmentConfig(cfg *config.Config, envName string) (*config.ResolvedEnvironmentConfig, error) {
	if cfg.ResolvedEnvironments == nil {
		return nil, fmt.Errorf("environments not resolved")
	}

	envConfig, exists := cfg.ResolvedEnvironments[envName]
	if !exists {
		return nil, fmt.Errorf("environment '%s' not found in configuration", envName)
	}

	return envConfig, nil
}

// printProductionSuccessMsg prints the success message for a provisioned
// production cluster with real, copy-pasteable next steps: the kubeconfig was
// persisted and merged during bootstrap (context adhar-<cluster>, now current),
// and every platform URL derives from the configured base domain (no
// hardcoded host).
func printProductionSuccessMsg(envName, host, clusterName string) {
	// The SAME panel the local path prints, via the same renderer.
	//
	// This was a row of 75 '#' characters, a bulleted list of things the platform
	// installs (which the checklist above has just shown, one line each, with
	// timings), and four numbered "next steps" with two of the URLs run together on
	// one line. Local had a bordered, aligned, brand-coloured table. Nothing
	// justified the difference except that the two were written at different times.
	//
	// The bullet list is gone rather than restyled: "Cilium CNI with
	// production-ready configuration" and friends restate the stage list directly
	// above them, and one of them ("Auto-scaling and high availability") is not even
	// true unless the config asked for it.
	fmt.Println()
	fmt.Println(renderCloudReadyPanel(host, clusterName))
	fmt.Println()

	// The one thing that will otherwise surprise them. Printed last, and as its own
	// block, because a self-signed certificate does not look like a DNS problem from
	// a browser — it looks like the platform is broken.
	printEdgeDNSBlocker()
}

// printEdgeDNSBlocker reports why the platform's URLs will not work, if anything
// was found to be wrong with the zone.
//
// Shared by BOTH provisioning paths, which is the whole point. It used to be
// inlined in printProductionSuccessMsg, and that is only called for a single
// named environment — so `adhar up -f config.yaml` with no --env (the documented
// way to bring up a whole file) finished with a bare "provisioning complete" box
// and dropped this warning entirely. The bootstrap had already detected the
// missing delegation and written the exact registrar fix; nobody ever saw it, and
// the platform looked broken for a reason the tool knew and did not say.
func printEdgeDNSBlocker() {
	if tlsBlocker == nil {
		return
	}
	fmt.Printf("%s\n", helpers.WarningStyle.Render(
		"▲ Platform URLs will not resolve, and TLS is SELF-SIGNED"))
	fmt.Printf("   why: %s\n", tlsBlocker.Reason)
	fmt.Printf("   fix: %s\n", tlsBlocker.Fix)
	fmt.Printf("   Until then the gateway is reachable only by IP, and every platform\n")
	fmt.Printf("   hostname fails to resolve. Let's Encrypt is already configured;\n")
	fmt.Printf("   external-dns publishes the records and cert-manager issues a trusted\n")
	fmt.Printf("   certificate on its own within minutes of the delegation being live.\n\n")
}

// tlsBlocker records why a publicly trusted certificate cannot be issued, so the
// closing summary can repeat what the bootstrap found. nil when nothing is wrong.
var tlsBlocker *acmeDNS01Blocker

// provisionCompletePlatformNew provisions the complete Adhar platform using the new provider system
func provisionCompletePlatformNew(ctx context.Context, providerManager *pfactory.ProviderManager, cfg *config.Config, dryRun bool, force bool) error {
	// Each environment prints its own checklist below; no separate header.
	fmt.Println()

	// Determine environments to provision
	var environmentsToProvision []string
	if len(cfg.Environments) == 0 {
		return fmt.Errorf("no environments defined in configuration file")
	}

	// Use environments from config, in a stable order — ranging over the map
	// provisioned them in a different sequence every run, which makes two runs of
	// the same file impossible to compare.
	for envName := range cfg.Environments {
		environmentsToProvision = append(environmentsToProvision, envName)
	}
	sort.Strings(environmentsToProvision)

	// Split the environments by what they physically ARE.
	//
	// `isolation: namespace` (the default) means the environment is a namespace
	// on ONE shared platform cluster; `isolation: cluster` means it gets a
	// cluster and a platform of its own. Declaring dev/test/prod therefore costs
	// one cluster by default instead of three — three was both unaffordable and
	//, on a default GCP quota (CPUS_ALL_REGIONS 64 against ~40 for one
	// production cluster), impossible.
	clusterEnvs, nsEnvs, partErr := partitionEnvironments(cfg, environmentsToProvision)
	if partErr != nil {
		return partErr
	}

	// The namespace environments need a cluster to live in. If no environment
	// asked for one of its own, provision exactly ONE — shaped by whichever
	// namespace environment is the most demanding, because that cluster has to
	// host all of them.
	sharedEnv := ""
	if len(nsEnvs) > 0 && len(clusterEnvs) == 0 {
		sharedEnv = sharedClusterEnvironment(cfg, nsEnvs)
		printSharedClusterPlan(cfg, nsEnvs, sharedEnv)
		clusterEnvs = []string{sharedEnv}
	}

	// Only the cluster-backed environments are provisioned; the namespace ones
	// are created on the shared cluster once its platform is up.
	environmentsToProvision = clusterEnvs

	// Provision each environment
	successCount := 0
	// Collected so the summary can say WHICH environment failed and why. The
	// per-environment lines scroll away above a long run.
	type envFailure struct {
		env    string
		reason error
	}
	var envFailures []envFailure
	for _, envName := range environmentsToProvision {
		// The environment is named on the checklist's Cloud cluster stage and in the
		// per-environment result line below, so it is not announced up front too.

		envConfig, err := resolveEnvironmentConfig(cfg, envName)
		if err != nil {
			fmt.Printf("  %s %s: %s\n", helpers.ErrorStyle.Render(helpers.IconFailed), envName, liveFailureLine(err))
			envFailures = append(envFailures, envFailure{envName, err})
			continue
		}
		applyKubeVersionOverride(envConfig)

		provisionOpts := pfactory.ProvisionOptions{
			DryRun:      dryRun,
			Force:       force,
			Recreate:    recreateCluster,
			ClusterName: envClusterName(clusterName, envName, len(environmentsToProvision)),
		}

		// A checklist per environment: this loop provisions each in turn, so one
		// block covering all of them would show several clusters' progress on the
		// same lines.
		resolvedName := pfactory.ResolveClusterName(envClusterName(clusterName, envName, len(environmentsToProvision)))
		tracker, restoreProgress := startCloudProgress(envConfig.Name, resolvedName, envConfig.ResolvedProvider, envConfig.ResolvedRegion, verbose)
		tracker.Activate(0)
		provisionOpts.OnPhase = trackPhases(tracker,
			clusterTargetLabel(resolvedName, envConfig.ResolvedProvider, envConfig.ResolvedRegion))
		result, err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts)
		if err != nil {
			tracker.Fail(1)
			restoreProgress(true)
			fmt.Printf("  %s %s: %s\n", helpers.ErrorStyle.Render(helpers.IconFailed), envName, liveFailureLine(err))
			envFailures = append(envFailures, envFailure{envName, err})
			continue
		}
		if result != nil {
			if err := bootstrapPlatformOnCluster(ctx, result, envConfig, cfg, tracker); err != nil {
				restoreProgress(true)
				fmt.Printf("  %s %s: %s\n", helpers.ErrorStyle.Render(helpers.IconFailed), envName, liveFailureLine(err))
				envFailures = append(envFailures, envFailure{envName, err})
				continue
			}
		}
		// Finalise this environment's block before ANY plain printing below.
		//
		// While the tracker runs it owns the cursor: it repositions by moving up
		// `lastLines` and clearing to the end of the screen. A bare fmt.Printf
		// underneath it scrolls the block without the tracker knowing, so the next
		// move-up lands in the wrong place and leaves an orphaned copy of the
		// checklist on screen — which is exactly what creating the environment
		// namespaces used to do from inside this block:
		//
		//	Provisioning prod  22m53s     <- orphan, truncated where it scrolled
		//	✓  Preflight  0s
		//	✓  Cloud cluster  3m56s
		//	Provisioning prod  22m53s     <- the live block, same elapsed time
		//	✓  Preflight  0s
		//	...
		//
		// Anything that must print WHILE the tracker is live goes through
		// tracker.Log, which clears first and redraws after.
		restoreProgress(false)

		// The namespace-isolated environments live on THIS cluster, so make them
		// now its platform is up. Once, on the shared cluster only.
		if result != nil && envName == sharedEnv && len(nsEnvs) > 0 {
			if nsErr := createEnvironmentNamespaces(ctx, result, nsEnvs); nsErr != nil {
				// Not fatal. The platform is up and usable, and the namespaces are
				// a thin, re-creatable layer on top of it — failing the environment
				// here would discard a working cluster over a label.
				fmt.Printf("  %s could not create environment namespaces: %v\n",
					helpers.WarningStyle.Render("▲"), nsErr)
			}
		}
		fmt.Printf("  %s %s provisioned\n", helpers.SuccessStyle.Render(helpers.IconReady), envName)
		successCount++
	}

	// Summary that matches the result.
	//
	// This said "● Platform Provisioning Complete!" unconditionally — printed after
	// provisioning 0 of 1 environments, directly above "Error: failed to provision 1
	// out of 1". A summary that contradicts its own outcome teaches people not to
	// read it. The box was hand-drawn too, with the count followed by a fixed run of
	// spaces, so the closing │ moved as soon as the numbers were not one digit.
	total := len(environmentsToProvision)
	// The summary box is skipped for the ordinary case — ONE environment, fully
	// provisioned — because the ready panel below already says so, and the local
	// path prints only that panel. Stacking "1 of 1 provisioned" above
	// "Platform ready" says the same thing twice in two different frames.
	//
	// It is still printed whenever it carries something the panel cannot: a
	// failure, a partial result, or several environments to account for.
	if total > 1 || successCount < total {
		fmt.Println(renderProvisionSummary(successCount, total, func() []string {
			lines := make([]string, 0, len(envFailures))
			for _, f := range envFailures {
				lines = append(lines, fmt.Sprintf("%s — %v", f.env, f.reason))
			}
			return lines
		}()))
	}

	// Everything the single-environment path tells the operator, on this path too.
	// Without it `adhar up -f config.yaml` (no --env) ended at the box above: no
	// URLs, no kubectl context, and no word about a broken DNS delegation.
	//
	// The BLOCKER GOES FIRST when there is one. This block used to list
	// "Console: https://console.<host>" and the paragraph immediately below it then
	// explained that those hostnames do not resolve — an output that hands over
	// three URLs and then says they do not work reads as a broken tool, and the
	// reader has already copied the first one.
	printEdgeDNSBlocker()
	if successCount > 0 {
		fmt.Println(renderCloudReadyPanel(cfg.GlobalSettings.DefaultHost, pfactory.ResolveClusterName(clusterName)))
		fmt.Println()
	}

	if successCount < total {
		// Carry the first reason into the returned error. "failed to provision 1 out
		// of 1 environments" told the operator only what they could already count.
		if len(envFailures) > 0 {
			return fmt.Errorf("provisioning failed for %d of %d environments: %s: %w",
				total-successCount, total, envFailures[0].env, envFailures[0].reason)
		}
		return fmt.Errorf("provisioning failed for %d of %d environments", total-successCount, total)
	}

	return nil
}

// showDryRunInfo displays what would be provisioned in dry-run mode
func showDryRunInfo(envConfig *config.ResolvedEnvironmentConfig) error {
	fmt.Printf("\n%s\n", helpers.BoldStyle.Render("▸ Dry Run - Configuration Preview"))
	fmt.Printf("┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│ Environment: %-30s │\n", envConfig.Name)
	fmt.Printf("│ Provider:    %-30s │\n", envConfig.ResolvedProvider)
	fmt.Printf("│ Region:      %-30s │\n", envConfig.ResolvedRegion)
	fmt.Printf("│ Type:        %-30s │\n", envConfig.ResolvedType)
	fmt.Printf("└─────────────────────────────────────────────┘\n")

	if len(envConfig.ResolvedClusterConfig) > 0 {
		fmt.Printf("\nCluster Configuration:\n")
		for _, cfg := range envConfig.ResolvedClusterConfig {
			fmt.Printf("  %s: %s\n", cfg.Key, cfg.Value)
		}
	}

	if envConfig.ResolvedCoreServices != nil {
		fmt.Printf("\nCore Services:\n")
		fmt.Printf("  ArgoCD:    %v\n", envConfig.ResolvedCoreServices.ArgoCD != nil)
		fmt.Printf("  Gitea:     %v\n", envConfig.ResolvedCoreServices.Gitea != nil)
		fmt.Printf("  Gateway:   %v\n", envConfig.ResolvedCoreServices.Gateway != nil)
		fmt.Printf("  Cilium:    %v\n", envConfig.ResolvedCoreServices.Cilium != nil)
	}

	if len(envConfig.ResolvedAddons) > 0 {
		fmt.Printf("\nAddons:\n")
		for _, addon := range envConfig.ResolvedAddons {
			fmt.Printf("  %s\n", addon.Name)
		}
	}

	fmt.Printf("\n%s\n", helpers.CodeStyle.Render("No changes will be made in dry-run mode"))
	return nil
}

// validateEnvironmentExists checks if the specified environment exists in the config file
func validateEnvironmentExists(configPath, envName string) error {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config file: %w", err)
	}

	if len(cfg.Environments) == 0 {
		return fmt.Errorf("no environments defined in configuration file")
	}

	if _, exists := cfg.Environments[envName]; !exists {
		var availableEnvs []string
		for env := range cfg.Environments {
			availableEnvs = append(availableEnvs, env)
		}
		return fmt.Errorf("environment '%s' not found. Available environments: %v", envName, availableEnvs)
	}

	return nil
}

// liveFailureLine is what the checklist prints the moment an environment fails.
//
// It is deliberately the FIRST LINE only. The closing panel renders the same
// failure a few lines below with the cause condensed and the remedy attached,
// and the returned error repeats it in full for logs — so printing everything
// here showed an operator the same cloud SDK dump, and the same multi-line
// remedy, three times in one screen. On the Azure auth failure of 2026-10-04
// that was three copies of a 40-line azidentity body.
//
// The live line's job is only to say which environment stopped and why, next to
// its checklist; the panel is where the detail and the fix belong.
func liveFailureLine(err error) string {
	if err == nil {
		return ""
	}
	line := err.Error()
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimRight(line[:i], " .") + "…"
	}
	return line
}
