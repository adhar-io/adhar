package db

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all databases",
	Long: `List managed databases (Crossplane CompositeDatabase resources).

Examples:
  adhar db list
  adhar db list --namespace=team-a`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	ns := dbNamespace()
	logger.Info(fmt.Sprintf("📋 Listing databases in namespace %s...", ns))

	client, err := getDynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	list, err := client.Resource(compositeDatabaseGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}

	if len(list.Items) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No managed databases found."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tNAMESPACE\tENGINE\tVERSION\tSIZE\tPHASE\tREADY")
	for _, item := range list.Items {
		obj := item.Object
		ready := ""
		if v, ok := nestedBool(obj, "status", "ready"); ok {
			ready = strconv.FormatBool(v)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			stringField(obj, "metadata", "name"),
			stringField(obj, "metadata", "namespace"),
			valueOrDash(stringField(obj, "spec", "parameters", "engine")),
			valueOrDash(stringField(obj, "spec", "parameters", "engineVersion")),
			valueOrDash(stringField(obj, "spec", "parameters", "storageSize")),
			valueOrDash(stringField(obj, "status", "phase")),
			valueOrDash(ready),
		)
	}
	return w.Flush()
}

// nestedBool returns a nested bool value from an unstructured object map.
func nestedBool(obj map[string]interface{}, keys ...string) (bool, bool) {
	cur := obj
	for i, k := range keys {
		if i == len(keys)-1 {
			v, ok := cur[k].(bool)
			return v, ok
		}
		next, ok := cur[k].(map[string]interface{})
		if !ok {
			return false, false
		}
		cur = next
	}
	return false, false
}
