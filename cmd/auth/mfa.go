package auth

import (
	"context"
	"fmt"
	"net/http"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

// kcCredential is a subset of the Keycloak credential representation.
type kcCredential struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

var (
	mfaCmd = &cobra.Command{
		Use:   "mfa",
		Short: "Multi-factor authentication",
		Long: `Manage multi-factor authentication including:
- TOTP setup and management
- SMS/Email verification
- Hardware security keys
- Backup codes`,
		RunE: runMFA,
	}

	// MFA specific flags
	mfaUser string
	mfaType string
)

func init() {
	mfaCmd.Flags().StringVarP(&mfaUser, "user", "u", "", "Target user")
	mfaCmd.Flags().StringVarP(&mfaType, "type", "t", "", "MFA type (totp, sms, email, hardware)")

	// Add MFA subcommands
	mfaCmd.AddCommand(setupMFACmd)
	mfaCmd.AddCommand(verifyMFACmd)
	mfaCmd.AddCommand(disableMFACmd)
	mfaCmd.AddCommand(generateBackupCodesCmd)
}

func runMFA(cmd *cobra.Command, args []string) error {
	fmt.Println("🔐 Adhar Platform Multi-Factor Authentication")
	fmt.Println("")
	fmt.Println("Available commands:")
	fmt.Println("  setup           - Setup MFA for a user")
	fmt.Println("  verify          - Verify MFA code")
	fmt.Println("  disable         - Disable MFA for a user")
	fmt.Println("  generate-codes  - Generate backup codes")
	fmt.Println("")
	fmt.Println("Use 'adhar auth mfa <command> --help' for more information")
	return nil
}

var (
	setupMFACmd = &cobra.Command{
		Use:   "setup [username]",
		Short: "Setup MFA for a user",
		Long:  "Setup multi-factor authentication for a specific user",
		Args:  cobra.ExactArgs(1),
		RunE:  runSetupMFA,
	}

	// Setup MFA specific flags
	mfaMethod string
	mfaPhone  string
	mfaEmail  string
)

func init() {
	setupMFACmd.Flags().StringVarP(&mfaMethod, "method", "m", "totp", "MFA method (totp, sms, email, hardware)")
	setupMFACmd.Flags().StringVarP(&mfaPhone, "phone", "p", "", "Phone number for SMS verification")
	setupMFACmd.Flags().StringVarP(&mfaEmail, "email", "e", "", "Email for email verification")
}

func runSetupMFA(cmd *cobra.Command, args []string) error {
	username := args[0]
	kc := settings()
	ctx := context.Background()

	// TOTP is what Keycloak lets an admin provision headlessly: we flag the user
	// with the CONFIGURE_TOTP required action, so at next login Keycloak walks
	// them through enrolling their authenticator (the QR/secret are generated
	// per-user by Keycloak, not by the CLI). Other methods are realm-policy
	// driven and not settable per-user via the admin API.
	if mfaMethod != "totp" {
		return fmt.Errorf("only --method totp can be provisioned via the admin API; %q is configured by realm authentication policy in the Keycloak console", mfaMethod)
	}

	id, err := kc.userIDByUsername(ctx, username)
	if err != nil {
		return err
	}
	var current map[string]interface{}
	if err := kc.adminGetOne(ctx, "/users/"+id, &current); err != nil {
		return err
	}
	if !hasRequiredAction(current, "CONFIGURE_TOTP") {
		current["requiredActions"] = append(requiredActions(current), "CONFIGURE_TOTP")
	}
	if _, err := kc.adminWrite(ctx, http.MethodPut, "/users/"+id, current); err != nil {
		return fmt.Errorf("setup MFA: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Flagged %s to configure TOTP at next login", username)))
	fmt.Println(helpers.CreateMuted("   Keycloak will present the QR code / secret when the user next signs in."))
	return nil
}

// requiredActions returns the user's requiredActions as a []interface{}.
func requiredActions(user map[string]interface{}) []interface{} {
	if ra, ok := user["requiredActions"].([]interface{}); ok {
		return ra
	}
	return nil
}

// hasRequiredAction reports whether the user already carries the given action.
func hasRequiredAction(user map[string]interface{}, action string) bool {
	for _, a := range requiredActions(user) {
		if s, ok := a.(string); ok && s == action {
			return true
		}
	}
	return false
}

var (
	verifyMFACmd = &cobra.Command{
		Use:   "verify [username] [code]",
		Short: "Verify MFA code",
		Long:  "Verify a multi-factor authentication code",
		Args:  cobra.ExactArgs(2),
		RunE:  runVerifyMFA,
	}
)

func runVerifyMFA(cmd *cobra.Command, args []string) error {
	// Verifying a TOTP code is part of the interactive login flow and is not a
	// Keycloak admin-API operation. Point the user at the real path.
	return fmt.Errorf("MFA codes are verified during interactive login, not via the admin API; sign in at %s and enter the code there", settings().Issuer)
}

var (
	disableMFACmd = &cobra.Command{
		Use:   "disable [username]",
		Short: "Disable MFA for a user",
		Long:  "Disable multi-factor authentication for a specific user",
		Args:  cobra.ExactArgs(1),
		RunE:  runDisableMFA,
	}

	// Disable MFA specific flags
	forceDisable bool
)

func init() {
	disableMFACmd.Flags().BoolVarP(&forceDisable, "force", "f", false, "Force disable without confirmation")
}

func runDisableMFA(cmd *cobra.Command, args []string) error {
	username := args[0]
	kc := settings()
	ctx := context.Background()

	id, err := kc.userIDByUsername(ctx, username)
	if err != nil {
		return err
	}

	// Remove any enrolled OTP credentials.
	var creds []kcCredential
	if err := kc.adminGet(ctx, "/users/"+id+"/credentials", &creds); err != nil {
		return err
	}
	removed := 0
	for _, c := range creds {
		if c.Type == "otp" {
			if _, err := kc.adminWrite(ctx, http.MethodDelete, "/users/"+id+"/credentials/"+c.ID, nil); err != nil {
				return fmt.Errorf("removing OTP credential: %w", err)
			}
			removed++
		}
	}

	// Also clear a pending CONFIGURE_TOTP required action if present.
	var current map[string]interface{}
	if err := kc.adminGetOne(ctx, "/users/"+id, &current); err != nil {
		return err
	}
	if hasRequiredAction(current, "CONFIGURE_TOTP") {
		filtered := make([]interface{}, 0)
		for _, a := range requiredActions(current) {
			if s, ok := a.(string); ok && s == "CONFIGURE_TOTP" {
				continue
			}
			filtered = append(filtered, a)
		}
		current["requiredActions"] = filtered
		if _, err := kc.adminWrite(ctx, http.MethodPut, "/users/"+id, current); err != nil {
			return fmt.Errorf("clearing TOTP required action: %w", err)
		}
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Disabled MFA for %s (removed %d OTP credential(s))", username, removed)))
	return nil
}

var (
	generateBackupCodesCmd = &cobra.Command{
		Use:   "generate-codes [username]",
		Short: "Generate backup codes",
		Long:  "Generate backup codes for MFA recovery",
		Args:  cobra.ExactArgs(1),
		RunE:  runGenerateBackupCodes,
	}

	// Generate backup codes specific flags
	codeCount int
)

func init() {
	generateBackupCodesCmd.Flags().IntVarP(&codeCount, "count", "c", 10, "Number of backup codes to generate")
}

func runGenerateBackupCodes(cmd *cobra.Command, args []string) error {
	// Keycloak's recovery/backup codes are generated interactively for the
	// signed-in user (the CONFIGURE_RECOVERY_AUTHN_CODES required action); the
	// admin API cannot mint them out-of-band. Rather than fabricate codes, guide
	// the user to the supported path.
	username := args[0]
	return fmt.Errorf("backup codes are generated by the user at login, not via the admin API; run `adhar auth mfa setup %s` to flag TOTP, and enable Recovery Authentication Codes in the realm's authentication policy (Keycloak console)", username)
}
