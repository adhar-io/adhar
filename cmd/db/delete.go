package db

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var deleteCmd = &cobra.Command{
	Use:   "delete [name]",
	Short: "Delete a database",
	Long: `Delete a managed database (Crossplane CompositeDatabase).

The name can be supplied as an argument or via --name.

Examples:
  adhar db delete myapp
  adhar db delete --name=myapp --namespace=team-a --force`,
	RunE: runDelete,
}

var dbForce bool

func init() {
	deleteCmd.Flags().BoolVarP(&dbForce, "force", "f", false, "Force deletion without confirmation")
}

func runDelete(cmd *cobra.Command, args []string) error {
	name := dbName
	if len(args) > 0 {
		name = args[0]
	}
	if name == "" {
		return fmt.Errorf("database name is required (pass as argument or --name)")
	}

	ns := dbNamespace()

	if !dbForce {
		fmt.Printf("Delete database %q in namespace %q? [y/N]: ", name, ns)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			logger.Info("Aborted.")
			return nil
		}
	}

	logger.Info(fmt.Sprintf("🗑️  Deleting database: %s", name))

	client, err := getDynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	err = client.Resource(compositeDatabaseGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("database %q not found in namespace %q", name, ns)
		}
		return fmt.Errorf("delete database: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Database %s deleted from namespace %s", name, ns)))
	return nil
}
