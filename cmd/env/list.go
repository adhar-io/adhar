package env

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	listLabel string
	listAll   bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all environments",
	Long: `List environments. Environments are modeled as Kubernetes namespaces. By
default only namespaces labelled ` + "`adhar.io/environment`" + ` are shown; use
--all to list every namespace, or --label to filter by a custom selector.

Examples:
  adhar env list                          # Adhar-managed environments
  adhar env list --all                    # Every namespace
  adhar env list --label=team=payments    # Custom label selector`,
	RunE: runList,
}

func init() {
	listCmd.Flags().StringVarP(&listLabel, "label", "l", "", "Label selector to filter environments (overrides default)")
	listCmd.Flags().BoolVar(&listAll, "all", false, "List all namespaces, not only Adhar environments")
}

func runList(cmd *cobra.Command, args []string) error {
	clientset, err := getClientset()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := metav1.ListOptions{}
	switch {
	case listLabel != "":
		opts.LabelSelector = listLabel
	case !listAll:
		opts.LabelSelector = envLabel
	}

	nss, err := clientset.CoreV1().Namespaces().List(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to list namespaces: %w", err)
	}

	fmt.Println(helpers.TitleStyle.Render("🌍 Environments"))
	if len(nss.Items) == 0 {
		fmt.Println(helpers.CreateMuted("   No environments found (try --all to list every namespace)"))
		return nil
	}

	type row struct {
		Name    string
		EnvName string
		Status  string
		Age     string
	}
	rows := make([]row, 0, len(nss.Items))
	for _, ns := range nss.Items {
		envName := ns.Labels[envLabel]
		if envName == "" {
			envName = "-"
		}
		rows = append(rows, row{
			Name:    ns.Name,
			EnvName: envName,
			Status:  string(ns.Status.Phase),
			Age:     envAge(ns.CreationTimestamp.Time),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-32s %-18s %-12s %-8s\n", "NAMESPACE", "ENVIRONMENT", "STATUS", "AGE"))
	b.WriteString(strings.Repeat("─", 74) + "\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("%-32s %-18s %-12s %-8s\n", r.Name, r.EnvName, r.Status, r.Age))
	}
	fmt.Print(b.String())
	return nil
}
