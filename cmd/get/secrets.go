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
• Gitea admin credentials
• ArgoCD admin credentials
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
	// Secrets-specific flags
	provider string
	showAll  bool
	debug    bool
)

func init() {
	secretsCmd.Flags().StringVarP(&provider, "provider", "p", "", "Filter secrets by provider (argocd, gitea, keycloak, etc.)")
	secretsCmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show all secrets including system ones")
	secretsCmd.Flags().BoolVarP(&debug, "debug", "d", false, "Show debug information about secret keys")
}

func runGetSecrets(cmd *cobra.Command, args []string) error {
	logger.Info("🔐 Retrieving platform secrets...")

	// Get Kubernetes client
	clientset, err := getKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	// Get secrets based on provider filter
	if provider != "" {
		return getSpecificProviderSecrets(clientset, provider)
	}

	// Get all platform secrets
	return getAllPlatformSecrets(clientset)
}

// getKubernetesClient creates a Kubernetes client
func getKubernetesClient() (*kubernetes.Clientset, error) {
	// Load kubeconfig
	kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return clientset, nil
}

// getSpecificProviderSecrets gets secrets for a specific provider
func getSpecificProviderSecrets(clientset *kubernetes.Clientset, provider string) error {
	logger.Info(fmt.Sprintf("🔍 Retrieving secrets for provider: %s", provider))

	// Define provider-specific secret patterns
	providerPatterns := map[string][]string{
		"argocd":   {"argocd-initial-admin-secret", "repo-"},
		"gitea":    {"gitea-admin-credentials"}, // Only essential Gitea admin credentials
		"keycloak": {"keycloak-", "keycloak-config"},
		"vault":    {"vault-", "vault-config"},
		"postgres": {"postgres-", "postgresql-"},
		"redis":    {"redis-", "redis-config"},
	}

	patterns, exists := providerPatterns[strings.ToLower(provider)]
	if !exists {
		return fmt.Errorf("unknown provider: %s", provider)
	}

	// Get secrets from adhar-system namespace
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	secrets, err := clientset.CoreV1().Secrets("adhar-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list secrets: %w", err)
	}

	var matchingSecrets []corev1.Secret
	for _, secret := range secrets.Items {
		// Skip certificate secrets entirely
		if shouldSkipSecret(secret) {
			continue
		}
		for _, pattern := range patterns {
			if strings.Contains(secret.Name, pattern) {
				matchingSecrets = append(matchingSecrets, secret)
				break
			}
		}
	}

	// Add virtual Gitea admin secret if provider is gitea
	if strings.ToLower(provider) == "gitea" {
		giteaAdminSecret, err := createGiteaAdminSecret(clientset)
		if err != nil {
			logger.Debugf("Failed to create Gitea admin secret: %v", err)
		} else if giteaAdminSecret != nil {
			logger.Debugf("Successfully created virtual Gitea admin secret")
			matchingSecrets = append(matchingSecrets, *giteaAdminSecret)
		}
	}

	if len(matchingSecrets) == 0 {
		logger.Info(fmt.Sprintf("No secrets found for provider: %s", provider))
		return nil
	}

	// Display secrets
	displaySecrets(matchingSecrets, provider)
	return nil
}

// getAllPlatformSecrets gets all platform secrets
func getAllPlatformSecrets(clientset *kubernetes.Clientset) error {

	// Get secrets from adhar-system namespace
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	secrets, err := clientset.CoreV1().Secrets("adhar-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list secrets: %w", err)
	}

	// Filter platform secrets (exclude system secrets unless --all is specified)
	// Always skip certificate secrets from display
	var platformSecrets []corev1.Secret
	for _, secret := range secrets.Items {
		// Skip certificate secrets entirely
		if shouldSkipSecret(secret) {
			continue
		}
		if isPlatformSecret(secret.Name) || showAll {
			platformSecrets = append(platformSecrets, secret)
		}
	}

	// Add virtual Gitea admin secret from deployment
	giteaAdminSecret, err := createGiteaAdminSecret(clientset)
	if err != nil {
		logger.Debugf("Failed to create Gitea admin secret: %v", err)
	} else if giteaAdminSecret != nil {
		logger.Debugf("Successfully created virtual Gitea admin secret")
		platformSecrets = append(platformSecrets, *giteaAdminSecret)
	}

	if len(platformSecrets) == 0 {
		logger.Info("No platform secrets found")
		return nil
	}

	// Display secrets
	displaySecrets(platformSecrets, "all")
	return nil
}

// isPlatformSecret checks if a secret is a platform secret
func isPlatformSecret(secretName string) bool {
	// Define essential platform secret patterns - only the most important ones
	essentialPatterns := []string{
		"argocd-initial-admin-secret", // ArgoCD admin credentials
		"gitea-admin-credentials",     // Gitea admin credentials (virtual secret)
		"keycloak-",                   // Keycloak related secrets
	}

	for _, pattern := range essentialPatterns {
		if strings.Contains(secretName, pattern) {
			return true
		}
	}

	return false
}

// shouldSkipSecret checks if a secret should be completely hidden from output
func shouldSkipSecret(secret corev1.Secret) bool {
	secretName := strings.ToLower(secret.Name)

	// Skip certificate/CA secrets entirely
	skipPatterns := []string{
		"-ca",
		"-server",
		"-replication",
		"-client",
		"tls-secret",
		"cert-secret",
	}

	for _, pattern := range skipPatterns {
		if strings.HasSuffix(secretName, pattern) {
			return true
		}
	}

	// Also skip if it's a TLS type secret or contains certificate data
	return isCertificateLike(secret)
}

// createGiteaAdminSecret creates a virtual secret for Gitea admin credentials from deployment
func createGiteaAdminSecret(clientset *kubernetes.Clientset) (*corev1.Secret, error) {
	// Get Gitea deployment to extract admin credentials
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deployments, err := clientset.AppsV1().Deployments("adhar-system").List(ctx, metav1.ListOptions{
		LabelSelector: "app=gitea",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get Gitea deployment: %w", err)
	}

	if len(deployments.Items) == 0 {
		return nil, fmt.Errorf("Gitea deployment not found")
	}

	deployment := deployments.Items[0]
	var giteaAdminUsername, giteaAdminPassword string

	// Extract admin credentials from environment variables
	// Check main containers first
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			switch env.Name {
			case "GITEA_ADMIN_USERNAME":
				giteaAdminUsername = env.Value
			case "GITEA_ADMIN_PASSWORD":
				giteaAdminPassword = env.Value
			}
		}
	}

	// Check init containers if not found in main containers
	if giteaAdminUsername == "" || giteaAdminPassword == "" {
		for _, container := range deployment.Spec.Template.Spec.InitContainers {
			for _, env := range container.Env {
				switch env.Name {
				case "GITEA_ADMIN_USERNAME":
					giteaAdminUsername = env.Value
				case "GITEA_ADMIN_PASSWORD":
					giteaAdminPassword = env.Value
				}
			}
		}
	}

	// If we found admin credentials, create a virtual secret
	if giteaAdminUsername != "" && giteaAdminPassword != "" {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitea-admin-credentials",
				Namespace: "adhar-system",
			},
			Data: map[string][]byte{
				"username": []byte(giteaAdminUsername),
				"password": []byte(giteaAdminPassword),
			},
		}
		return secret, nil
	}

	return nil, fmt.Errorf("Gitea admin credentials not found in deployment")
}

// displaySecrets displays secrets in a formatted table
func displaySecrets(secrets []corev1.Secret, provider string) {
	logger.Info(fmt.Sprintf("📋 Found %d secrets for %s", len(secrets), provider))

	// Create bordered table content
	var tableContent strings.Builder

	// Create simple, clean header without complex styling
	headerContent := fmt.Sprintf("%-40s %-30s %-40s",
		"🔐 SECRET",
		"👤 USERNAME",
		"🔑 PASSWORD")
	tableContent.WriteString(helpers.CreateHighlight(headerContent))
	tableContent.WriteString("\n")

	// Add separator line with proper width
	tableContent.WriteString(strings.Repeat("─", 110))
	tableContent.WriteString("\n")

	// Add secret rows with proper formatting
	for _, secret := range secrets {
		secretInfo := extractSecretInfo(secret)

		// Get secret icon and truncate if needed
		secretName := getSecretIcon(secretInfo.Name) + " " + secretInfo.Name
		if len(secretName) > 38 {
			secretName = secretName[:35] + "..."
		}

		// Handle username
		username := secretInfo.Username
		if len(username) > 28 {
			username = username[:25] + "..."
		}

		// Handle password
		password := secretInfo.Password
		if len(password) > 38 {
			password = password[:35] + "..."
		}

		// Format the row with proper spacing (matching header widths)
		secretRow := fmt.Sprintf("%-40s %-30s %-40s",
			secretName,
			username,
			password)
		tableContent.WriteString(secretRow)
		tableContent.WriteString("\n")

		// Add debug information if requested
		if debug {
			fmt.Printf("  🔍 Debug: %s\n", secret.Name)
			for key, value := range secret.Data {
				valueStr := string(value)
				if len(valueStr) > 50 {
					valueStr = valueStr[:47] + "..."
				}
				fmt.Printf("    Key: %s, Value: %s\n", key, valueStr)
			}
			fmt.Println()
		}
	}

	// Create bordered box around the table
	borderStyle := helpers.BorderStyle.Width(115)
	borderedTable := borderStyle.Render(tableContent.String())
	fmt.Println(borderedTable)
}

// SecretInfo contains extracted secret information
type SecretInfo struct {
	Name     string
	Username string
	Password string
	URL      string
}

// extractSecretInfo extracts useful information from a secret
func extractSecretInfo(secret corev1.Secret) SecretInfo {
	info := SecretInfo{
		Name: secret.Name,
	}

	// Do not print certificate/private key contents
	if isCertificateLike(secret) {
		info.Username = "(certificate)"
		info.Password = "[redacted]"
		return info
	}

	// Extract username from various possible keys
	usernameKeys := []string{"username", "user", "admin-user", "admin-username", "login", "email"}
	for _, key := range usernameKeys {
		if username, exists := secret.Data[key]; exists && len(username) > 0 {
			info.Username = string(username)
			break
		}
	}

	// Extract password from various possible keys
	passwordKeys := []string{"password", "pass", "admin-password", "admin-pass", "secret", "token", "key"}
	for _, key := range passwordKeys {
		if password, exists := secret.Data[key]; exists && len(password) > 0 {
			info.Password = string(password)
			break
		}
	}

	// Special handling for specific secret types
	switch {
	case strings.Contains(strings.ToLower(secret.Name), "argocd-initial-admin-secret"):
		// ArgoCD admin secret has password key
		if adminPass, exists := secret.Data["password"]; exists {
			info.Username = "admin"
			info.Password = string(adminPass)
		}
	case strings.Contains(strings.ToLower(secret.Name), "gitea-admin-credentials"):
		// Virtual Gitea admin secret from deployment
		if username, exists := secret.Data["username"]; exists {
			info.Username = string(username)
		}
		if password, exists := secret.Data["password"]; exists {
			info.Password = string(password)
		}
	case strings.Contains(strings.ToLower(secret.Name), "gitea-postgresql"):
		// Gitea PostgreSQL has postgres-password key
		if postgresPass, exists := secret.Data["postgres-password"]; exists {
			info.Username = "gitea"
			info.Password = string(postgresPass)
		}
	case strings.Contains(strings.ToLower(secret.Name), "gitea-init"):
		// Gitea init secret - contains scripts, not credentials
		info.Username = "script"
		info.Password = "configuration"
	case strings.Contains(strings.ToLower(secret.Name), "gitea-inline-config"):
		// Gitea inline config - contains configuration, not credentials
		info.Username = "config"
		info.Password = "settings"
	case strings.Contains(strings.ToLower(secret.Name), "argocd-redis"):
		// ArgoCD Redis secret has auth key
		if redisPass, exists := secret.Data["auth"]; exists {
			info.Username = "redis"
			info.Password = string(redisPass)
		}
	case strings.Contains(strings.ToLower(secret.Name), "argocd-secret"):
		// ArgoCD secret has admin.password key
		if adminPass, exists := secret.Data["admin.password"]; exists {
			info.Username = "argocd"
			info.Password = string(adminPass)
		}
	case strings.Contains(strings.ToLower(secret.Name), "argocd-notifications-secret"):
		// ArgoCD notifications secret - empty
		info.Username = "none"
		info.Password = "empty"
	}

	// If we still don't have username/password, try to infer from available keys
	if info.Username == "" {
		// Look for any key that might contain username-like data
		for key, value := range secret.Data {
			keyLower := strings.ToLower(key)
			if strings.Contains(keyLower, "user") || strings.Contains(keyLower, "login") || strings.Contains(keyLower, "admin") {
				if len(value) > 0 && len(value) < 50 { // Reasonable length for username
					// Skip timestamp-like values
					valueStr := string(value)
					if !isTimestamp(valueStr) {
						info.Username = valueStr
						break
					}
				}
			}
		}
	}

	if info.Password == "" {
		// Look for any key that might contain password-like data
		for key, value := range secret.Data {
			keyLower := strings.ToLower(key)
			if strings.Contains(keyLower, "pass") || strings.Contains(keyLower, "secret") || strings.Contains(keyLower, "token") || strings.Contains(keyLower, "key") {
				if len(value) > 0 {
					info.Password = string(value)
					break
				}
			}
		}
	}

	// Generate URL based on secret name
	info.URL = generateSecretURL(secret.Name)

	return info
}

// isCertificateLike returns true if the secret appears to hold TLS material or CAs.
func isCertificateLike(secret corev1.Secret) bool {
	if secret.Type == corev1.SecretTypeTLS {
		return true
	}

	pemMarkers := []string{
		"BEGIN CERTIFICATE",
		"BEGIN PRIVATE KEY",
		"BEGIN RSA PRIVATE KEY",
		"BEGIN EC PRIVATE KEY",
	}

	for key, value := range secret.Data {
		// Common key names for TLS/CAs
		if strings.Contains(key, "tls.") || strings.Contains(key, "ca") || strings.Contains(key, "crt") || strings.Contains(key, "cert") {
			return true
		}
		strVal := string(value)
		for _, marker := range pemMarkers {
			if strings.Contains(strVal, marker) {
				return true
			}
		}
	}

	return false
}

// generateSecretURL generates a URL for the secret
func generateSecretURL(secretName string) string {
	// TODO: Implement URL generation based on secret type
	// This should generate appropriate URLs for different services
	return "https://adhar.localtest.me/" + strings.ToLower(secretName)
}

// isTimestamp checks if a string looks like a timestamp
func isTimestamp(value string) bool {
	// Check for common timestamp patterns
	timestampPatterns := []string{
		"T", // ISO 8601 format
		"-", // Date separators
		":", // Time separators
		"Z", // UTC timezone
	}

	// If it contains multiple timestamp indicators, it's likely a timestamp
	count := 0
	for _, pattern := range timestampPatterns {
		if strings.Contains(value, pattern) {
			count++
		}
	}

	// If it has 3 or more timestamp indicators, consider it a timestamp
	return count >= 3
}

// getSecretIcon returns an appropriate icon for the secret type
func getSecretIcon(secretName string) string {
	secretName = strings.ToLower(secretName)

	switch {
	case strings.Contains(secretName, "argocd"):
		return "🚀"
	case strings.Contains(secretName, "gitea"):
		return "🦊"
	case strings.Contains(secretName, "keycloak"):
		return "🔐"
	case strings.Contains(secretName, "vault"):
		return "🔒"
	case strings.Contains(secretName, "postgres"):
		return "🐘"
	case strings.Contains(secretName, "redis"):
		return "🔴"
	case strings.Contains(secretName, "admin"):
		return "👑"
	case strings.Contains(secretName, "config"):
		return "⚙️"
	case strings.Contains(secretName, "cert"):
		return "📜"
	case strings.Contains(secretName, "token"):
		return "🔑"
	default:
		return "🔐"
	}
}
