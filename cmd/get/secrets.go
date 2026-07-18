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

package get

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// secretsCmd represents the secrets command
var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Get platform secrets and credentials",
	Long: `Get platform secrets and credentials for various services.

This command retrieves and displays:
• ArgoCD admin credentials
• Gitea admin credentials
• Keycloak admin credentials
• Database credentials
• API tokens and keys

Examples:
  adhar get secrets                    # Get all platform secrets
  adhar get secrets -p argocd         # Get ArgoCD specific secrets
  adhar get secrets -p gitea          # Get Gitea specific secrets
  adhar get secrets -p keycloak       # Get Keycloak specific secrets`,
	RunE: runGetSecrets,
}

var (
	provider string
	showAll  bool
	debug    bool
)

func init() {
	secretsCmd.Flags().StringVarP(&provider, "provider", "p", "", "Filter secrets by provider (argocd, gitea, keycloak, vault, postgres, redis)")
	secretsCmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show all secrets including system ones")
	secretsCmd.Flags().BoolVarP(&debug, "debug", "d", false, "Show debug information about secret keys")
}

// providerConfig defines where to look for each provider's secrets
type providerConfig struct {
	namespaces []string // namespaces to search
	patterns   []string // secret name patterns to match
}

// essentialProviders are shown by default with `adhar get secrets`
var essentialProviders = []string{"argocd", "gitea", "keycloak-admin", "keycloak-user"}

// knownProviders maps provider names to their search configuration.
// "keycloak-admin" and "keycloak-user" both read keycloak-config but extract different fields.
// "keycloak" as a -p filter returns both.
// All platform packages deploy into adhar-system.
//
//nolint:goconst // provider names and secret patterns repeat by design in this lookup table
var knownProviders = map[string]providerConfig{
	"argocd":         {namespaces: []string{"adhar-system"}, patterns: []string{"argocd-initial-admin-secret"}},
	"gitea":          {namespaces: []string{"adhar-system"}, patterns: []string{"gitea-admin-credentials"}},
	"keycloak-admin": {namespaces: []string{"adhar-system"}, patterns: []string{"keycloak-config"}},
	"keycloak-user":  {namespaces: []string{"adhar-system"}, patterns: []string{"keycloak-config"}},
	"keycloak":       {namespaces: []string{"adhar-system"}, patterns: []string{"keycloak-config", "keycloak-clients"}},
	"vault":          {namespaces: []string{"adhar-system"}, patterns: []string{"vault-keys", "vault-unseal-keys", "vault-root-token"}},
	"postgres":       {namespaces: []string{"adhar-system"}, patterns: []string{"postgres", "postgresql"}},
	"redis":          {namespaces: []string{"adhar-system"}, patterns: []string{"redis"}},
	"harbor":         {namespaces: []string{"adhar-system"}, patterns: []string{"harbor-admin", "harbor-core"}},
	"rustfs":         {namespaces: []string{"adhar-system"}, patterns: []string{"rustfs-credentials"}},
}

func runGetSecrets(cmd *cobra.Command, args []string) error {
	logger.Info("Retrieving platform secrets...")

	clientset, err := getKubernetesClient()
	if err != nil {
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		return helpers.FriendlyError(fmt.Errorf("could not connect to the cluster: %w", err),
			"Is the cluster running? Try: adhar up")
	}

	if provider != "" {
		return getProviderSecrets(clientset, strings.ToLower(provider))
	}
	return getAllPlatformSecrets(clientset)
}

func getKubernetesClient() (*kubernetes.Clientset, error) {
	kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	return kubernetes.NewForConfig(config)
}

// getProviderSecrets retrieves secrets for a specific provider
func getProviderSecrets(clientset *kubernetes.Clientset, providerName string) error {
	if _, exists := knownProviders[providerName]; !exists {
		available := []string{"argocd", "gitea", "keycloak", "vault", "postgres", "redis", "harbor", "rustfs"}
		return fmt.Errorf("unknown provider %q (available: %s)", providerName, strings.Join(available, ", "))
	}

	// "keycloak" expands to both admin and user
	names := []string{providerName}
	if providerName == "keycloak" {
		names = []string{"keycloak-admin", "keycloak-user", "keycloak"}
	}
	entries := resolveSecrets(clientset, names)

	if len(entries) == 0 {
		logger.Info(fmt.Sprintf("No secrets found for provider: %s", providerName))
		return nil
	}

	return displaySecretEntries(entries, providerName)
}

// getAllPlatformSecrets retrieves admin credentials for core services.
// By default only shows ArgoCD, Gitea, Keycloak admin + user. Use --all for everything.
func getAllPlatformSecrets(clientset *kubernetes.Clientset) error {
	providers := essentialProviders
	if showAll {
		providers = make([]string, 0, len(knownProviders))
		for name := range knownProviders {
			providers = append(providers, name)
		}
	}

	entries := resolveSecrets(clientset, providers)

	if len(entries) == 0 {
		logger.Info("No platform secrets found")
		return nil
	}

	label := "platform"
	if showAll {
		label = "all"
	}
	return displaySecretEntries(entries, label)
}

// resolveSecrets collects secrets for given provider names and extracts credential entries
func resolveSecrets(clientset *kubernetes.Clientset, providerNames []string) []SecretEntry {
	var entries []SecretEntry
	seen := make(map[string]bool)

	for _, name := range providerNames {
		cfg, ok := knownProviders[name]
		if !ok {
			continue
		}

		secrets := collectSecrets(clientset, cfg.namespaces, cfg.patterns)

		// Gitea: add virtual secret from deployment env vars
		if name == "gitea" {
			if s := buildGiteaAdminSecret(clientset); s != nil {
				secrets = append(secrets, *s)
			}
		}

		for _, secret := range secrets {
			for _, entry := range extractEntries(name, secret) {
				key := entry.Service + "/" + entry.Username
				if !seen[key] {
					seen[key] = true
					entries = append(entries, entry)
				}
			}
		}
	}

	return entries
}

// collectSecrets searches namespaces for secrets matching the given name patterns
func collectSecrets(clientset *kubernetes.Clientset, namespaces, patterns []string) []corev1.Secret {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var result []corev1.Secret
	for _, ns := range namespaces {
		secrets, err := clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			logger.Debugf("Failed to list secrets in namespace %s: %v", ns, err)
			continue
		}
		for _, secret := range secrets.Items {
			if matchesAny(secret.Name, patterns) {
				result = append(result, secret)
			}
		}
	}
	return result
}

// matchesAny returns true if name contains any of the patterns
func matchesAny(name string, patterns []string) bool {
	lower := strings.ToLower(name)
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// buildGiteaAdminSecret creates a virtual secret from Gitea deployment env vars
func buildGiteaAdminSecret(clientset *kubernetes.Clientset) *corev1.Secret {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deployments, err := clientset.AppsV1().Deployments("adhar-system").List(ctx, metav1.ListOptions{
		LabelSelector: "app=gitea",
	})
	if err != nil || len(deployments.Items) == 0 {
		return nil
	}

	var username, password string
	// Search all containers (init + main) for credentials
	allContainers := append(deployments.Items[0].Spec.Template.Spec.InitContainers,
		deployments.Items[0].Spec.Template.Spec.Containers...)
	for _, c := range allContainers {
		for _, env := range c.Env {
			switch env.Name {
			case "GITEA_ADMIN_USERNAME":
				username = env.Value
			case "GITEA_ADMIN_PASSWORD":
				password = env.Value
			}
		}
	}

	if username == "" || password == "" {
		return nil
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitea-admin-credentials",
			Namespace: "adhar-system",
		},
		Data: map[string][]byte{
			"username": []byte(username),
			"password": []byte(password),
		},
	}
}

// SecretEntry is a single row in the output table
type SecretEntry struct {
	Icon     string
	Service  string
	Username string
	Password string
}

// extractEntries converts a K8s secret into one or more display entries based on provider context
func extractEntries(providerName string, secret corev1.Secret) []SecretEntry {
	switch providerName {
	case "argocd":
		if strings.Contains(secret.Name, "argocd-initial-admin-secret") {
			return []SecretEntry{{
				Icon: "🚀", Service: "ArgoCD",
				Username: "admin", Password: string(secret.Data["password"]),
			}}
		}
	case "gitea":
		if strings.Contains(secret.Name, "gitea-admin") {
			return []SecretEntry{{
				Icon: "🦊", Service: "Gitea",
				Username: string(secret.Data["username"]), Password: string(secret.Data["password"]),
			}}
		}
	case "keycloak-admin":
		if strings.Contains(secret.Name, "keycloak-config") {
			return []SecretEntry{{
				Icon: "🔑", Service: "Keycloak (admin)",
				Username: "adhar-admin", Password: string(secret.Data["KEYCLOAK_ADMIN_PASSWORD"]),
			}}
		}
	case "keycloak-user":
		if strings.Contains(secret.Name, "keycloak-config") {
			pw := string(secret.Data["USER_PASSWORD"])
			return []SecretEntry{
				{Icon: "👤", Service: "Keycloak user1 (admin)", Username: "user1", Password: pw},
				{Icon: "👤", Service: "Keycloak user2 (developer)", Username: "user2", Password: pw},
			}
		}
	case "keycloak":
		// When using -p keycloak, return both admin + user + clients
		var entries []SecretEntry
		if strings.Contains(secret.Name, "keycloak-config") {
			pw := string(secret.Data["USER_PASSWORD"])
			entries = append(entries, SecretEntry{
				Icon: "🔑", Service: "Keycloak (admin)",
				Username: "adhar-admin", Password: string(secret.Data["KEYCLOAK_ADMIN_PASSWORD"]),
			}, SecretEntry{
				Icon: "👤", Service: "Keycloak user1 (admin)",
				Username: "user1", Password: pw,
			}, SecretEntry{
				Icon: "👤", Service: "Keycloak user2 (developer)",
				Username: "user2", Password: pw,
			})
		}
		if strings.Contains(secret.Name, "keycloak-clients") {
			entries = append(entries, SecretEntry{
				Icon: "⚙️", Service: "Keycloak (backstage)",
				Username: string(secret.Data["BACKSTAGE_CLIENT_ID"]),
				Password: string(secret.Data["BACKSTAGE_CLIENT_SECRET"]),
			})
		}
		return entries
	case "harbor":
		if strings.Contains(secret.Name, "harbor-admin") {
			return []SecretEntry{{Icon: "⚓", Service: "Harbor", Username: "admin", Password: string(secret.Data["HARBOR_ADMIN_PASSWORD"])}}
		}
		if strings.Contains(secret.Name, "harbor-core") {
			return []SecretEntry{{Icon: "⚓", Service: "Harbor (core)", Username: "harbor", Password: string(secret.Data["secret"])}}
		}
	case "rustfs":
		if strings.Contains(secret.Name, "rustfs-credentials") {
			return []SecretEntry{{
				Icon: "📦", Service: "RustFS (S3/console)",
				Username: string(secret.Data["RUSTFS_ACCESS_KEY"]),
				Password: string(secret.Data["RUSTFS_SECRET_KEY"]),
			}}
		}
	case "postgres":
		entry := SecretEntry{Icon: "🐘", Service: "PostgreSQL (" + secret.Namespace + ")"}
		if p, ok := secret.Data["postgres-password"]; ok {
			entry.Username = "postgres"
			entry.Password = string(p)
		} else if p, ok := secret.Data["password"]; ok {
			entry.Username = string(secret.Data["username"])
			entry.Password = string(p)
		}
		if entry.Password != "" {
			return []SecretEntry{entry}
		}
	case "redis":
		if p, ok := secret.Data["auth"]; ok {
			return []SecretEntry{{Icon: "🔴", Service: "Redis", Username: "default", Password: string(p)}}
		}
		if p, ok := secret.Data["password"]; ok {
			return []SecretEntry{{Icon: "🔴", Service: "Redis", Username: "default", Password: string(p)}}
		}
	case "vault":
		entry := SecretEntry{Icon: "🔒", Service: "Vault"}
		if p, ok := secret.Data["root-token"]; ok {
			entry.Username = "root"
			entry.Password = string(p)
		}
		if entry.Password != "" {
			return []SecretEntry{entry}
		}
	}

	// Generic fallback
	entry := SecretEntry{Icon: "🔐", Service: secret.Name}
	for _, k := range []string{"username", "user", "admin-user", "login"} {
		if v, ok := secret.Data[k]; ok && len(v) > 0 {
			entry.Username = string(v)
			break
		}
	}
	for _, k := range []string{"password", "pass", "secret", "token", "key", "auth"} {
		if v, ok := secret.Data[k]; ok && len(v) > 0 {
			entry.Password = string(v)
			break
		}
	}
	if entry.Username != "" || entry.Password != "" {
		return []SecretEntry{entry}
	}
	return nil
}

// displaySecretEntries renders the entries as a clean table
func displaySecretEntries(entries []SecretEntry, label string) error {
	fmt.Println()
	logger.Info(fmt.Sprintf("Found %d credential(s) for %s\n", len(entries), label))

	// Calculate column widths from data
	svcW, userW, passW := 22, 20, 20
	for _, e := range entries {
		if l := len(e.Service) + 4; l > svcW { // +4 for icon + spaces
			svcW = l
		}
		if l := len(e.Username); l > userW {
			userW = l
		}
		if l := len(e.Password); l > passW {
			passW = l
		}
	}
	// Cap max widths
	if svcW > 30 {
		svcW = 30
	}
	if userW > 25 {
		userW = 25
	}
	if passW > 45 {
		passW = 45
	}

	totalW := svcW + userW + passW + 10 // padding between columns
	var tb strings.Builder

	headerFmt := fmt.Sprintf("  %%-%ds  %%-%ds  %%-%ds", svcW, userW, passW)
	tb.WriteString(helpers.CreateHighlight(fmt.Sprintf(headerFmt, "SERVICE", "USERNAME", "PASSWORD")))
	tb.WriteString("\n")
	tb.WriteString(strings.Repeat("─", totalW))
	tb.WriteString("\n")

	rowFmt := fmt.Sprintf("  %%-%ds  %%-%ds  %%s\n", svcW, userW)
	for _, e := range entries {
		svc := truncate(e.Icon+" "+e.Service, svcW)
		user := truncate(e.Username, userW)
		pass := truncate(e.Password, passW)
		if user == "" {
			user = "-"
		}
		if pass == "" {
			pass = "-"
		}
		tb.WriteString(fmt.Sprintf(rowFmt, svc, user, pass))
	}

	fmt.Println(helpers.BorderStyle.Width(totalW + 6).Render(tb.String()))
	fmt.Println()
	return nil
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}
