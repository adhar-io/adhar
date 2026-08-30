package gitops

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var repoNamespace string

var repoCmd = &cobra.Command{
	Use:   "repo",
	Short: "List Git repositories ArgoCD uses",
	Long: `List the Git repositories registered with ArgoCD.

Repositories are stored as Secrets labelled
argocd.argoproj.io/secret-type=repository; this reads them from the control plane
and shows their URLs.

Examples:
  adhar gitops repo
  adhar gitops repo --namespace adhar-system`,
	RunE: runRepo,
}

func init() {
	repoCmd.Flags().StringVarP(&repoNamespace, "namespace", "n", argoNamespace, "ArgoCD namespace")
}

func runRepo(cmd *cobra.Command, args []string) error {
	logger.Info(fmt.Sprintf("📚 Listing Git repositories in namespace %s...", repoNamespace))

	client, err := helpers.DynamicClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	list, err := client.Resource(secretsGVR).Namespace(repoNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "argocd.argoproj.io/secret-type=repository",
	})
	if err != nil {
		return fmt.Errorf("list repository secrets: %w", err)
	}

	if len(list.Items) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No ArgoCD repositories found."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tURL\tTYPE\tPROJECT")
	for _, item := range list.Items {
		obj := item.Object
		name := valueOrDash(secretDataString(obj, "name"))
		if name == "-" {
			name = stringField(obj, "metadata", "name")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			name,
			valueOrDash(secretDataString(obj, "url")),
			valueOrDash(secretDataString(obj, "type")),
			valueOrDash(secretDataString(obj, "project")),
		)
	}
	return w.Flush()
}
