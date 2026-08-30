package policy

import (
	"context"
	"fmt"
	"os"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	validateCmd = &cobra.Command{
		Use:   "validate [policy-file]",
		Short: "Validate a policy file",
		Long: `Validate a Kyverno policy file. By default this performs a server-side
dry-run apply against the cluster (full schema + admission validation). With
--client it only parses the file client-side (valid YAML, apiVersion/kind
present) — useful when no cluster is reachable.

Examples:
  adhar policy validate my-clusterpolicy.yaml
  adhar policy validate --file=my-clusterpolicy.yaml
  adhar policy validate my-clusterpolicy.yaml --client`,
		Args: cobra.MaximumNArgs(1),
		RunE: runValidatePolicy,
	}

	clientOnly bool
)

func init() {
	validateCmd.Flags().BoolVar(&clientOnly, "client", false, "Client-side parse only (no cluster dry-run)")
}

func runValidatePolicy(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		policyFile = args[0]
	}
	if policyFile == "" {
		return fmt.Errorf("policy file is required. Use --file flag or provide as argument")
	}

	fmt.Println(helpers.TitleStyle.Render("🔍 Validating policy file: " + policyFile))

	data, err := os.ReadFile(policyFile)
	if err != nil {
		return fmt.Errorf("read policy file: %w", err)
	}

	// Client-side parse always runs first.
	objs, err := decodeManifests(data)
	if err != nil {
		fmt.Println(helpers.CreateError("Parse failed: " + err.Error()))
		return err
	}
	fmt.Printf("   ✅ Parsed %d manifest document(s)\n", len(objs))
	for _, o := range objs {
		fmt.Printf("      • %s/%s\n", o.GetKind(), o.GetName())
	}

	if clientOnly {
		fmt.Println(helpers.CreateSuccess("Client-side validation passed."))
		return nil
	}

	// Server-side dry-run validation.
	dyn, mapper, err := kubeClients()
	if err != nil {
		fmt.Println(helpers.CreateMuted("   Cluster unreachable — skipping server-side dry-run (use --client to silence)."))
		fmt.Println(helpers.CreateMuted("   " + err.Error()))
		return nil
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	if ctx == nil {
		ctx = context.Background()
	}

	failed := false
	for _, obj := range objs {
		if err := applyManifest(ctx, dyn, mapper, obj, namespace, true); err != nil {
			failed = true
			fmt.Println(helpers.CreateError(fmt.Sprintf("   ✗ %s/%s: %v", obj.GetKind(), obj.GetName(), err)))
			continue
		}
		fmt.Printf("   ✅ %s/%s valid (server dry-run)\n", obj.GetKind(), obj.GetName())
	}
	if failed {
		return fmt.Errorf("policy validation failed")
	}
	fmt.Println(helpers.CreateSuccess("Policy validation passed."))
	return nil
}
