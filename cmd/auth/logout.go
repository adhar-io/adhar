package auth

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Logout from the platform",
	Long: `End the current session: the refresh token is invalidated at Keycloak's
logout endpoint (best effort) and the local session file is removed.`,
	RunE: runLogout,
}

func runLogout(cmd *cobra.Command, args []string) error {
	s, err := loadSession()
	if err != nil {
		return err
	}
	if s == nil {
		fmt.Println("Not logged in; nothing to do.")
		return nil
	}

	// Best effort: the local session is removed even if Keycloak is
	// unreachable, so logout always leaves the machine clean.
	if s.RefreshToken != "" {
		kc := settings()
		kc.Issuer = s.Issuer
		kc.ClientID = s.ClientID
		kc.Insecure = kc.Insecure || s.Insecure
		if err := kc.endSession(context.Background(), s.RefreshToken); err != nil {
			fmt.Println(helpers.CreateMuted("   (server-side session invalidation failed: " + err.Error() + ")"))
		} else {
			fmt.Println("🔓 Server-side session invalidated")
		}
	}

	if err := deleteSession(); err != nil {
		return fmt.Errorf("removing local session: %w", err)
	}
	fmt.Println(helpers.CreateSuccess("✅ Logged out"))
	fmt.Printf("👤 %s — session removed from %s\n", s.Username, credentialsPath())
	return nil
}
