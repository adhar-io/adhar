package auth

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	loginCmd = &cobra.Command{
		Use:   "login [username]",
		Short: "Authenticate with the platform",
		Long: `Authenticate against the Keycloak realm using the OIDC password grant.

Obtains an access token from the realm token endpoint (derived from --issuer).
The token is printed; combine with --output json to capture it programmatically.

Examples:
  adhar auth login admin --insecure
  adhar auth login --issuer https://kc.example/realms/adhar
  adhar auth login admin --password secret --output json`,
		Args: cobra.MaximumNArgs(1),
		RunE: runLogin,
	}

	// Login specific flags
	username   string
	password   string
	rememberMe bool
	forceLogin bool
)

func init() {
	loginCmd.Flags().StringVarP(&password, "password", "", "", "Password (prompted if omitted)")
	loginCmd.Flags().BoolVarP(&rememberMe, "remember", "r", false, "Remember login session")
	loginCmd.Flags().BoolVarP(&forceLogin, "force", "f", false, "Force re-authentication")
}

func runLogin(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		username = args[0]
	}

	if username == "" {
		fmt.Print("Username: ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		username = strings.TrimSpace(line)
	}
	if username == "" {
		return fmt.Errorf("username is required")
	}

	if password == "" {
		pw, err := promptPassword("Password: ")
		if err != nil {
			return err
		}
		password = pw
	}
	if password == "" {
		return fmt.Errorf("password is required")
	}

	kc := settings()
	fmt.Printf("🔐 Authenticating %q against %s\n", username, kc.Issuer)

	tr, err := kc.passwordGrant(context.Background(), username, password)
	if err != nil {
		return err
	}

	if output == "json" {
		return helpers.PrintJSON(tr)
	}

	fmt.Println(helpers.CreateSuccess("✅ Successfully authenticated"))
	fmt.Printf("👤 User:    %s\n", username)
	fmt.Printf("⏰ Expires: %ds\n", tr.ExpiresIn)
	fmt.Printf("🔑 Access token:\n%s\n", tr.AccessToken)
	fmt.Println(helpers.CreateMuted("   Reuse it for admin calls: adhar auth user list --admin-token <token>"))
	return nil
}

// promptPassword reads a password from the terminal without echoing it. Falls
// back to a plain (echoed) read when stdin is not a terminal.
func promptPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}
		return string(b), nil
	}
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line), nil
}
