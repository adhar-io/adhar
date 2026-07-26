package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show the logged-in identity and its groups",
	Long: `Show the current session's identity as Keycloak sees it: username,
email, groups (which drive both service RBAC and Kubernetes RBAC), realm
roles, and token expiry. Refreshes the access token if needed.`,
	RunE: runWhoami,
}

func runWhoami(cmd *cobra.Command, args []string) error {
	s, err := currentSession(context.Background())
	if err != nil {
		return err
	}
	claims, err := parseClaims(s.AccessToken)
	if err != nil {
		return fmt.Errorf("reading token claims: %w", err)
	}

	if output == "json" {
		return helpers.PrintJSON(claims)
	}

	name := claims.PreferredUsername
	if name == "" {
		name = s.Username
	}
	fmt.Printf("👤 User:    %s\n", name)
	if claims.Email != "" {
		fmt.Printf("📧 Email:   %s\n", claims.Email)
	}
	if len(claims.Groups) > 0 {
		fmt.Printf("👥 Groups:  %s\n", strings.Join(claims.Groups, ", "))
	} else {
		fmt.Println(helpers.CreateMuted("👥 Groups:  (none in token — is the client's groups scope configured?)"))
	}
	if len(claims.RealmAccess.Roles) > 0 {
		fmt.Printf("🎭 Roles:   %s\n", strings.Join(claims.RealmAccess.Roles, ", "))
	}
	fmt.Printf("🏛️ Issuer:  %s\n", claims.Issuer)
	fmt.Printf("⏰ Token:   valid for %s\n", time.Until(s.AccessExpiry).Round(time.Second))
	return nil
}
