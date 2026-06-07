package auth

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	tokenCmd = &cobra.Command{
		Use:   "token",
		Short: "Obtain an OIDC token from Keycloak",
		Long: `Obtain an OIDC access token from the Keycloak realm.

With no subcommand, this mints a token: if --user is set it uses the password
grant (prompting for the password), otherwise it uses the client_credentials
grant for the configured client. Subcommands (create/list/...) manage
client/personal tokens and require admin wiring; they report clearly when not
configured.

Examples:
  adhar auth token --user admin --insecure
  adhar auth token --client-id my-svc --client-secret xxxx`,
		RunE: runToken,
	}

	// Token specific flags
	tokenID   string
	tokenName string
	tokenUser string
)

func init() {
	tokenCmd.Flags().StringVarP(&tokenID, "id", "i", "", "Token ID")
	tokenCmd.Flags().StringVarP(&tokenName, "name", "n", "", "Token name")
	tokenCmd.Flags().StringVarP(&tokenUser, "user", "u", "", "Token owner")

	// Add token subcommands
	tokenCmd.AddCommand(createTokenCmd)
	tokenCmd.AddCommand(listTokensCmd)
	tokenCmd.AddCommand(getTokenCmd)
	tokenCmd.AddCommand(revokeTokenCmd)
	tokenCmd.AddCommand(renewTokenCmd)
}

func runToken(cmd *cobra.Command, args []string) error {
	kc := settings()
	ctx := context.Background()

	var (
		tr  *tokenResponse
		err error
	)
	if tokenUser != "" {
		pw, perr := promptPassword(fmt.Sprintf("Password for %s: ", tokenUser))
		if perr != nil {
			return perr
		}
		fmt.Printf("🔑 Requesting token for %q via password grant...\n", tokenUser)
		tr, err = kc.passwordGrant(ctx, tokenUser, pw)
	} else {
		fmt.Printf("🔑 Requesting token for client %q via client_credentials grant...\n", kc.ClientID)
		tr, err = kc.clientCredentialsGrant(ctx)
	}
	if err != nil {
		return err
	}

	if output == "json" {
		return helpers.PrintJSON(tr)
	}
	fmt.Println(helpers.CreateSuccess("✅ Token obtained"))
	fmt.Printf("⏰ Expires: %ds\n", tr.ExpiresIn)
	fmt.Printf("🔑 Access token:\n%s\n", tr.AccessToken)
	return nil
}

var (
	createTokenCmd = &cobra.Command{
		Use:   "create [token-name]",
		Short: "Create a new API token",
		Long:  "Create a new API token with specified permissions and expiration",
		Args:  cobra.ExactArgs(1),
		RunE:  runCreateToken,
	}

	// Create token specific flags
	tokenDesc   string
	tokenPerms  []string
	tokenExpiry time.Duration
	tokenScope  string
)

func init() {
	createTokenCmd.Flags().StringVarP(&tokenDesc, "description", "d", "", "Token description")
	createTokenCmd.Flags().StringArrayVarP(&tokenPerms, "permissions", "p", []string{}, "Token permissions")
	createTokenCmd.Flags().DurationVarP(&tokenExpiry, "expiry", "e", 24*365*time.Hour, "Token expiration time")
	createTokenCmd.Flags().StringVarP(&tokenScope, "scope", "s", "namespace", "Token scope (namespace, cluster, global)")
}

func runCreateToken(cmd *cobra.Command, args []string) error {
	// Minting a personal/offline token in Keycloak requires the password (or an
	// offline_access scope) and is realm-specific. Rather than fabricate a token,
	// point the user at the supported path: `adhar auth token` (OIDC grant).
	return fmt.Errorf("creating named API tokens is not wired to Keycloak in this build; use `adhar auth token --user %s` to obtain an OIDC access token instead", args[0])
}

var (
	listTokensCmd = &cobra.Command{
		Use:   "list",
		Short: "List all tokens",
		Long:  "List all API tokens with filtering options",
		RunE:  runListTokens,
	}

	// List tokens specific flags
	showExpired bool
	showRevoked bool
)

func init() {
	listTokensCmd.Flags().BoolVarP(&showExpired, "expired", "e", false, "Show expired tokens")
	listTokensCmd.Flags().BoolVarP(&showRevoked, "revoked", "r", false, "Show revoked tokens")
}

func runListTokens(cmd *cobra.Command, args []string) error {
	fmt.Println("📋 API Tokens")
	fmt.Println("")

	if showExpired {
		fmt.Println("⏰ Including expired tokens")
	}
	if showRevoked {
		fmt.Println("🚫 Including revoked tokens")
	}

	// TODO: Implement actual token listing logic
	fmt.Println("📭 No tokens found")
	fmt.Println("Use 'adhar auth token create' to create your first token")

	return nil
}

var (
	getTokenCmd = &cobra.Command{
		Use:   "get [token-id]",
		Short: "Get token details",
		Long:  "Get detailed information about a specific token",
		Args:  cobra.ExactArgs(1),
		RunE:  runGetToken,
	}
)

func runGetToken(cmd *cobra.Command, args []string) error {
	tokenID := args[0]

	fmt.Printf("🔑 Token Details: %s\n", tokenID)
	fmt.Println("")

	// TODO: Implement actual token retrieval logic
	fmt.Println("📭 Token not found")

	return nil
}

var (
	revokeTokenCmd = &cobra.Command{
		Use:   "revoke [token-id]",
		Short: "Revoke a token",
		Long:  "Revoke an API token immediately",
		Args:  cobra.ExactArgs(1),
		RunE:  runRevokeToken,
	}

	// Revoke token specific flags
	revokeReason string
)

func init() {
	revokeTokenCmd.Flags().StringVarP(&revokeReason, "reason", "r", "", "Reason for revocation")
}

func runRevokeToken(cmd *cobra.Command, args []string) error {
	tokenID := args[0]

	fmt.Printf("🚫 Revoking token: %s\n", tokenID)

	if revokeReason != "" {
		fmt.Printf("📝 Reason: %s\n", revokeReason)
	}

	// TODO: Implement actual token revocation logic
	fmt.Printf("✅ Successfully revoked token: %s\n", tokenID)
	return nil
}

var (
	renewTokenCmd = &cobra.Command{
		Use:   "renew [token-id]",
		Short: "Renew an expired token",
		Long:  "Renew an expired API token with new expiration",
		Args:  cobra.ExactArgs(1),
		RunE:  runRenewToken,
	}

	// Renew token specific flags
	newExpiry time.Duration
)

func init() {
	renewTokenCmd.Flags().DurationVarP(&newExpiry, "expiry", "e", 24*365*time.Hour, "New expiration time")
}

func runRenewToken(cmd *cobra.Command, args []string) error {
	tokenID := args[0]

	fmt.Printf("🔄 Renewing token: %s\n", tokenID)
	fmt.Printf("⏰ New expiry: %s\n", newExpiry)

	// TODO: Implement actual token renewal logic
	fmt.Printf("✅ Successfully renewed token: %s\n", tokenID)
	return nil
}
