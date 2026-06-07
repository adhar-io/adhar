package db

import (
	"context"
	"fmt"
	"strconv"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var statusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Show database status",
	Long: `Show the status of a managed database (Crossplane CompositeDatabase).

The name can be supplied as an argument or via --name.

Examples:
  adhar db status myapp
  adhar db status --name=myapp --namespace=team-a`,
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	name := dbName
	if len(args) > 0 {
		name = args[0]
	}
	if name == "" {
		return fmt.Errorf("database name is required (pass as argument or --name)")
	}

	ns := dbNamespace()
	logger.Info(fmt.Sprintf("📊 Checking status for database: %s", name))

	client, err := getDynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	obj, err := client.Resource(compositeDatabaseGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("database %q not found in namespace %q", name, ns)
		}
		return fmt.Errorf("get database: %w", err)
	}

	o := obj.Object
	ready := "-"
	if v, ok := nestedBool(o, "status", "ready"); ok {
		ready = strconv.FormatBool(v)
	}

	builder := ""
	add := func(label, value string) {
		builder += fmt.Sprintf("%s %s\n", helpers.BulletStyle.Render(label), valueOrDash(value))
	}
	add("Database:", stringField(o, "metadata", "name"))
	add("Namespace:", stringField(o, "metadata", "namespace"))
	add("Engine:", stringField(o, "spec", "parameters", "engine"))
	add("Version:", stringField(o, "spec", "parameters", "engineVersion"))
	add("Size:", stringField(o, "spec", "parameters", "storageSize"))
	add("Phase:", stringField(o, "status", "phase"))
	add("Ready:", ready)
	add("Endpoint:", stringField(o, "status", "endpoint"))
	add("Message:", stringField(o, "status", "message"))
	fmt.Println(helpers.CreateBox(builder, 90))
	return nil
}
