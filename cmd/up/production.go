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
	"os"
	"sort"

	"adhar-io/adhar/cmd/helpers"
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
	var conv *convergenceSnapshot
	if result != nil {
		var err error
		conv, err = bootstrapPlatformOnCluster(ctx, result, envConfig, cfg, tracker)
		if err != nil {
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
	printProductionSuccessMsg(environment, cfg.GlobalSettings.DefaultHost, clusterName, conv)
	return nil
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
func printProductionSuccessMsg(envName, host, clusterName string, conv *convergenceSnapshot) {
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
	fmt.Println(renderCloudReadyPanel(host, clusterName, conv))
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

	// Provision each environment
	successCount := 0
	// Collected so the summary can say WHICH environment failed and why. The
	// per-environment lines scroll away above a long run.
	type envFailure struct {
		env    string
		reason error
	}
	var envFailures []envFailure
	// Convergence of the LAST environment the panel will speak for — see the
	// comment at the assignment below for why it is not simply the last one.
	var lastConv *convergenceSnapshot
	for _, envName := range environmentsToProvision {
		// The environment is named on the checklist's Cloud cluster stage and in the
		// per-environment result line below, so it is not announced up front too.

		envConfig, err := resolveEnvironmentConfig(cfg, envName)
		if err != nil {
			fmt.Printf("  %s %s: %v\n", helpers.ErrorStyle.Render(helpers.IconFailed), envName, err)
			envFailures = append(envFailures, envFailure{envName, err})
			continue
		}
		applyKubeVersionOverride(envConfig)

		provisionOpts := pfactory.ProvisionOptions{
			DryRun:      dryRun,
			Force:       force,
			Recreate:    recreateCluster,
			ClusterName: clusterName,
		}

		// A checklist per environment: this loop provisions each in turn, so one
		// block covering all of them would show several clusters' progress on the
		// same lines.
		resolvedName := pfactory.ResolveClusterName(clusterName)
		tracker, restoreProgress := startCloudProgress(envConfig.Name, resolvedName, envConfig.ResolvedProvider, envConfig.ResolvedRegion, verbose)
		tracker.Activate(0)
		provisionOpts.OnPhase = trackPhases(tracker,
			clusterTargetLabel(resolvedName, envConfig.ResolvedProvider, envConfig.ResolvedRegion))
		result, err := providerManager.ProvisionEnvironment(ctx, envConfig, provisionOpts)
		if err != nil {
			tracker.Fail(1)
			restoreProgress(true)
			fmt.Printf("  %s %s: %v\n", helpers.ErrorStyle.Render(helpers.IconFailed), envName, err)
			envFailures = append(envFailures, envFailure{envName, err})
			continue
		}
		if result != nil {
			snap, err := bootstrapPlatformOnCluster(ctx, result, envConfig, cfg, tracker)
			if err != nil {
				restoreProgress(true)
				fmt.Printf("  %s %s: %v\n", helpers.ErrorStyle.Render(helpers.IconFailed), envName, err)
				envFailures = append(envFailures, envFailure{envName, err})
				continue
			}
			// Across several environments the panel can only make one claim, so it
			// makes the WEAKEST one: a single environment whose console is not yet
			// serving means the shared URL list is not a promise. Keeping the first
			// non-serving snapshot rather than the last avoids a late healthy
			// environment masking an earlier stalled one.
			if lastConv == nil || (lastConv.serving() && !snap.serving()) {
				lastConv = snap
			}
		}
		// Finalise this environment's block before the next one starts its own.
		restoreProgress(false)
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
		fmt.Println(renderCloudReadyPanel(cfg.GlobalSettings.DefaultHost, pfactory.ResolveClusterName(clusterName), lastConv))
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
