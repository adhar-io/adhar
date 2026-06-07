package auth

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	// AuthCmd is the main auth command
	AuthCmd = &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication and authorization",
		Long: `Manage platform authentication and authorization including:
- User and group management
- Authentication methods (OAuth, SAML, LDAP, local)
- Role-based access control (RBAC)
- Multi-factor authentication (MFA)
- API keys and tokens
- Session management
- Security policies`,
		RunE: runAuth,
	}

	// Global flags
	authProvider string
	namespace    string
	output       string
	verbose      bool

	// Keycloak connection flags (shared by login/token/user/role/group).
	kcIssuer       string
	kcAdminURL     string
	kcRealm        string
	kcClientID     string
	kcClientSecret string
	kcAdminToken   string
	kcInsecure     bool
)

func init() {
	// Global flags
	// NOTE: these are persistent (inherited by every subcommand). Several
	// subcommands already use -p (permissions) and -n (name) as local shorthands,
	// so the persistent variants intentionally omit shorthands to avoid a pflag
	// "shorthand already used" panic when cobra merges the flag sets.
	AuthCmd.PersistentFlags().StringVar(&authProvider, "provider", "", "Authentication provider (keycloak, ldap, saml, oauth)")
	AuthCmd.PersistentFlags().StringVar(&namespace, "namespace", "", "Target namespace")
	AuthCmd.PersistentFlags().StringVarP(&output, "output", "o", "table", "Output format: table, json, yaml")
	AuthCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")

	// Keycloak connection flags.
	AuthCmd.PersistentFlags().StringVar(&kcIssuer, "issuer", defaultIssuer, "OIDC issuer URL (Keycloak realm)")
	AuthCmd.PersistentFlags().StringVar(&kcAdminURL, "admin-url", defaultAdminAPIURL, "Keycloak base URL for the Admin REST API")
	AuthCmd.PersistentFlags().StringVar(&kcRealm, "realm", defaultRealm, "Keycloak realm")
	AuthCmd.PersistentFlags().StringVar(&kcClientID, "client-id", defaultClientID, "OIDC client ID")
	AuthCmd.PersistentFlags().StringVar(&kcClientSecret, "client-secret", "", "OIDC client secret (if the client is confidential)")
	AuthCmd.PersistentFlags().StringVar(&kcAdminToken, "admin-token", "", "Bearer token for the Keycloak Admin REST API")
	AuthCmd.PersistentFlags().BoolVar(&kcInsecure, "insecure", false, "Skip TLS verification (for the local self-signed cert)")

	// Add subcommands
	AuthCmd.AddCommand(loginCmd)
	AuthCmd.AddCommand(logoutCmd)
	AuthCmd.AddCommand(userCmd)
	AuthCmd.AddCommand(groupCmd)
	AuthCmd.AddCommand(roleCmd)
	AuthCmd.AddCommand(tokenCmd)
	AuthCmd.AddCommand(mfaCmd)
	AuthCmd.AddCommand(providerCmd)
	AuthCmd.AddCommand(sessionCmd)
}

func runAuth(cmd *cobra.Command, args []string) error {
	fmt.Println("🔐 Adhar Platform Authentication & Authorization")
	fmt.Println("")
	fmt.Println("Available commands:")
	fmt.Println("  login     - Authenticate with the platform")
	fmt.Println("  logout    - Logout from the platform")
	fmt.Println("  user      - Manage users and accounts")
	fmt.Println("  group     - Manage user groups")
	fmt.Println("  role      - Manage roles and permissions")
	fmt.Println("  token     - Manage API tokens and keys")
	fmt.Println("  mfa       - Multi-factor authentication")
	fmt.Println("  provider  - Manage authentication providers")
	fmt.Println("  session   - Manage user sessions")
	fmt.Println("")
	fmt.Println("Use 'adhar auth <command> --help' for more information")
	return nil
}
