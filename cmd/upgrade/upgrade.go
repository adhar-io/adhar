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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/controllers"
	"adhar-io/adhar/platform/controllers/adharplatform"
	"adhar-io/adhar/platform/k8s"
	"adhar-io/adhar/platform/utils"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
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
	Short: "⬆️ Upgrade the platform: converge foundation, review stack diff, sync",
	Long: `⬆️ **Adhar Platform Upgrade**

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

	stackDir, err := filepath.Abs(stackDirFlag)
	if err != nil {
		return fmt.Errorf("resolving stack directory: %w", err)
	}
	if _, err := os.Stat(stackDir); err != nil {
		return fmt.Errorf("platform stack directory not found at %s (run from the adhar repository root or pass --stack-dir): %w", stackDir, err)
	}

	kubeConfigPath := filepath.Join(homedir.HomeDir(), ".kube", "config")
	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
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
		fmt.Printf("ℹ️  Using AdharPlatform %q (the only platform on this cluster)\n", platform.Name)
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
		fmt.Println("⬆️  Converging foundation to this release's embedded manifests…")
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
		for _, s := range steps {
			fmt.Printf("   • %s\n", s.name)
			if _, err := s.run(ctx, ctrl.Request{}, &platform); err != nil {
				return fmt.Errorf("converging %s: %w", s.name, err)
			}
		}
		fmt.Println("✅ Foundation converged")
	}

	// ------------------------------------------------------------------
	// Phase 2: stack diff.
	// ------------------------------------------------------------------
	fmt.Println("🔍 Comparing local stack against in-cluster GitOps repositories…")
	summary, hasDiff, err := diffStack(ctx, kubeClient, platform.Spec.BuildCustomization, stackDir, tmpDir)
	if err != nil {
		return fmt.Errorf("computing stack diff: %w", err)
	}
	fmt.Println(summary)

	if diffOnly {
		return nil
	}
	if !hasDiff {
		fmt.Println("✅ Stack already in sync; re-applying ApplicationSet for completeness")
	} else if !assumeYes {
		fmt.Print("Push these changes and sync? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Println("Aborted; nothing pushed.")
			return nil
		}
	}

	// ------------------------------------------------------------------
	// Phase 3: push stack + re-apply ApplicationSet + refresh.
	// ------------------------------------------------------------------
	fmt.Println("🔄 Pushing stack and re-applying the platform ApplicationSet…")
	if err := reconciler.ApplyPlatformStack(ctx, &platform); err != nil {
		return fmt.Errorf("applying platform stack: %w", err)
	}
	fmt.Println("✅ Upgrade applied — ArgoCD is syncing the stack. Track progress with `adhar get status`.")
	return nil
}

// diffStack clones the packages and environments repos from the in-cluster
// Gitea and diffs them against the local stack directory. It returns a
// human-readable summary and whether any differences exist.
func diffStack(ctx context.Context, kubeClient client.Client, cfg v1alpha1.BuildCustomizationSpec, stackDir, tmpDir string) (string, bool, error) {
	creds, err := utils.GetSecretByName(ctx, kubeClient, utils.GiteaNamespace, utils.GiteaAdminSecret)
	if err != nil {
		return "", false, fmt.Errorf("reading gitea credentials: %w", err)
	}
	username := string(creds.Data["username"])
	password := string(creds.Data["password"])

	var b strings.Builder
	hasDiff := false
	for _, repo := range []string{"packages", "environments"} {
		cloneDir := filepath.Join(tmpDir, "repo-"+repo)
		if err := cloneGiteaRepo(ctx, cfg, username, password, repo, cloneDir); err != nil {
			return "", false, err
		}
		// Remove clone metadata so the directory diff sees content only.
		if err := os.RemoveAll(filepath.Join(cloneDir, ".git")); err != nil {
			return "", false, err
		}

		names, err := gitDiffNames(ctx, cloneDir, filepath.Join(stackDir, repo))
		if err != nil {
			return "", false, err
		}
		if len(names) == 0 {
			fmt.Fprintf(&b, "   %s: no changes\n", repo)
			continue
		}
		hasDiff = true
		fmt.Fprintf(&b, "   %s: %d file(s) differ\n", repo, len(names))
		const maxShown = 40
		for i, n := range names {
			if i == maxShown {
				fmt.Fprintf(&b, "      … and %d more\n", len(names)-maxShown)
				break
			}
			fmt.Fprintf(&b, "      %s\n", n)
		}
	}
	return b.String(), hasDiff, nil
}

// cloneGiteaRepo clones a repo from the in-cluster Gitea over its external URL
// using the admin credentials. TLS verification is disabled because the
// platform certificate is self-signed by default.
func cloneGiteaRepo(ctx context.Context, cfg v1alpha1.BuildCustomizationSpec, username, password, repo, dest string) error {
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
		Path:   fmt.Sprintf("/%s/%s.git", username, repo),
		User:   url.UserPassword(username, password),
	}

	cmd := exec.CommandContext(ctx, "git", "-c", "http.sslVerify=false", "clone", "--quiet", "--depth", "1", base.String(), dest)
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
