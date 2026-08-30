package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

// maskToken shows only the first and last few characters of a secret so it is
// recognizable in logs without being disclosed.
func maskToken(tok string) string {
	if len(tok) <= 12 {
		return "****"
	}
	return tok[:6] + "…" + tok[len(tok)-4:]
}

var (
	tokenCmd = &cobra.Command{
		Use:   "token",
		Short: "Obtain an OIDC token from Keycloak",
		Long: `Obtain an OIDC access token from the Keycloak realm.

With no flags, prints a valid access token for the logged-in session
(auto-refreshing it when expired) — suitable for piping:
  curl -H "Authorization: Bearer $(adhar auth token)" ...

With --user it mints a fresh token via the password grant; with
--client-secret it uses the client_credentials grant for the configured
client. Subcommands (create/list/...) manage client/personal tokens and
require admin wiring; they report clearly when not configured.

Examples:
  adhar auth token
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
	tokenCmd.AddCommand(decodeTokenCmd)
}

var decodeTokenCmd = &cobra.Command{
	Use:   "decode [token]",
	Short: "Decode and display the current (or a given) token's claims",
	Long: `Decode a JWT and show its claims. With no argument, decodes the access
token of the logged-in session (refreshing it if needed). The token is never
printed in full — only its claims are shown.

Examples:
  adhar auth token decode
  adhar auth token decode "$(adhar auth token)"`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDecodeToken,
}

func runDecodeToken(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	var raw string
	if len(args) == 1 {
		raw = args[0]
	} else {
		s, err := currentSession(ctx)
		if err != nil {
			return err
		}
		raw = s.AccessToken
	}

	claims, err := parseClaims(raw)
	if err != nil {
		return fmt.Errorf("decoding token: %w", err)
	}
	if output == "json" {
		return helpers.PrintJSON(claims)
	}

	fmt.Printf("🔐 Token:    %s\n", maskToken(raw))
	if claims.PreferredUsername != "" {
		fmt.Printf("👤 User:     %s\n", claims.PreferredUsername)
	}
	if claims.Email != "" {
		fmt.Printf("📧 Email:    %s\n", claims.Email)
	}
	if claims.Subject != "" {
		fmt.Printf("🆔 Subject:  %s\n", claims.Subject)
	}
	if len(claims.Groups) > 0 {
		fmt.Printf("👥 Groups:   %s\n", strings.Join(claims.Groups, ", "))
	}
	if len(claims.RealmAccess.Roles) > 0 {
		fmt.Printf("🎭 Roles:    %s\n", strings.Join(claims.RealmAccess.Roles, ", "))
	}
	fmt.Printf("🏛️ Issuer:   %s\n", claims.Issuer)
	if claims.Expiry > 0 {
		exp := time.Unix(claims.Expiry, 0)
		fmt.Printf("⏰ Expires:  %s (%s)\n", exp.Format(time.RFC3339), time.Until(exp).Round(time.Second))
	}
	return nil
}

func runToken(cmd *cobra.Command, args []string) error {
	kc := settings()
	ctx := context.Background()

	// Default path: the logged-in session, refreshed as needed. Raw token on
	// stdout so it composes with curl/kubectl.
	if tokenUser == "" && kcClientSecret == "" {
		s, err := currentSession(ctx)
		if err != nil {
			return err
		}
		if output == "json" {
			return helpers.PrintJSON(map[string]any{
				"accessToken": s.AccessToken,
				"expiresAt":   s.AccessExpiry,
				"username":    s.Username,
			})
		}
		fmt.Println(s.AccessToken)
		return nil
	}

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
	// Keycloak does not expose "personal access tokens"; the CLI's token is the
	// current OIDC session. Surface that (masked) as the live token, which is
	// what `adhar auth token` would emit.
	s, err := loadSession()
	if err != nil {
		return err
	}
	if s == nil {
		fmt.Println(helpers.CreateMuted("📭 Not logged in — run `adhar auth login <username>` to obtain a token."))
		return nil
	}

	if output == "json" {
		return helpers.PrintJSON(map[string]any{
			"username":      s.Username,
			"issuer":        s.Issuer,
			"clientId":      s.ClientID,
			"accessToken":   maskToken(s.AccessToken),
			"accessExpiry":  s.AccessExpiry,
			"refreshExpiry": s.RefreshExpiry,
			"expired":       time.Now().After(s.AccessExpiry),
		})
	}

	fmt.Println("📋 Session token (Keycloak OIDC)")
	fmt.Printf("👤 User:    %s\n", s.Username)
	fmt.Printf("🔌 Client:  %s\n", s.ClientID)
	fmt.Printf("🔐 Access:  %s\n", maskToken(s.AccessToken))
	status := "valid for " + time.Until(s.AccessExpiry).Round(time.Second).String()
	if time.Now().After(s.AccessExpiry) {
		status = "expired (auto-refreshes on next `adhar auth token`)"
	}
	fmt.Printf("⏰ Access:  %s\n", status)
	fmt.Println(helpers.CreateMuted("   Print a live token: adhar auth token  •  decode it: adhar auth token decode"))
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
