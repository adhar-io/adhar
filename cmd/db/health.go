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
	"k8s.io/client-go/dynamic"
)

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check database health",
	Long: `Check the health of managed databases by reading the CompositeDatabase XR
status together with the composed CloudNativePG Cluster status (phase and ready
instance count). Read-only.

Examples:
  adhar db health
  adhar db health --name=myapp --namespace=team-a`,
	RunE: runHealth,
}

func runHealth(cmd *cobra.Command, args []string) error {
	logger.Info("🏥 Checking database health...")

	client, err := getDynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if dbName != "" {
		return checkDatabaseHealth(ctx, client, dbNamespace(), dbName)
	}
	return checkAllDatabasesHealth(ctx, client, dbNamespace())
}

func checkDatabaseHealth(ctx context.Context, client dynamic.Interface, ns, name string) error {
	xr, err := client.Resource(compositeDatabaseGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("database %q not found in namespace %q", name, ns)
		}
		return fmt.Errorf("get database: %w", err)
	}

	xo := xr.Object
	xrReady := "-"
	if v, ok := nestedBool(xo, "status", "ready"); ok {
		xrReady = strconv.FormatBool(v)
	}

	builder := ""
	add := func(label, value string) {
		builder += fmt.Sprintf("%s %s\n", helpers.BulletStyle.Render(label), valueOrDash(value))
	}
	add("Database:", stringField(xo, "metadata", "name"))
	add("Namespace:", stringField(xo, "metadata", "namespace"))
	add("Engine:", stringField(xo, "spec", "parameters", "engine"))
	add("XR Phase:", stringField(xo, "status", "phase"))
	add("XR Ready:", xrReady)

	// Composed CNPG Cluster status — the live backing health signal.
	cluster, cerr := client.Resource(cnpgClusterGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	healthy := false
	if cerr != nil {
		if k8serrors.IsNotFound(cerr) {
			add("Cluster:", "not found (non-CNPG engine or not yet provisioned)")
		} else {
			add("Cluster:", "error: "+cerr.Error())
		}
	} else {
		co := cluster.Object
		phase := stringField(co, "status", "phase")
		instances, _ := nestedInt64(co, "spec", "instances")
		ready, _ := nestedInt64(co, "status", "readyInstances")
		add("Cluster Phase:", phase)
		add("Instances Ready:", fmt.Sprintf("%d/%d", ready, instances))
		if pip := stringField(co, "status", "currentPrimary"); pip != "" {
			add("Primary:", pip)
		}
		healthy = instances > 0 && ready == instances &&
			(phase == "" || phase == "Cluster in healthy state")
	}

	if healthy {
		add("Health:", "✅ Healthy")
	} else {
		add("Health:", "⚠️  Degraded / not ready")
	}
	fmt.Println(helpers.CreateBox(builder, 90))
	return nil
}

func checkAllDatabasesHealth(ctx context.Context, client dynamic.Interface, ns string) error {
	list, err := client.Resource(compositeDatabaseGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}
	if len(list.Items) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No managed databases found."))
		return nil
	}
	for i := range list.Items {
		name := list.Items[i].GetName()
		if err := checkDatabaseHealth(ctx, client, ns, name); err != nil {
			fmt.Println(helpers.CreateMuted(fmt.Sprintf("   %s: %v", name, err)))
		}
	}
	return nil
}
