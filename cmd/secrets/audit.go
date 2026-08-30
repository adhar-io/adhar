package secrets

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Audit secret metadata and provenance",
	Long: `Audit secrets by reading their Kubernetes metadata: who last changed each
secret and when (from managedFields), how it is managed, and when it was last
rotated (adhar.io/rotated-at annotation). Values are never read or shown.

Examples:
  adhar secrets audit                    # Audit all secrets in the namespace
  adhar secrets audit --name=db-creds    # Audit a single secret (detailed)`,
	RunE: runAudit,
}

func runAudit(cmd *cobra.Command, args []string) error {
	logger.Info("🔍 Auditing secret metadata...")

	ns := resolveNamespace()
	clientset, err := getClientset()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout(timeout))
	defer cancel()

	if secretName != "" {
		sec, err := clientset.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get secret %q in namespace %q: %w", secretName, ns, err)
		}
		return auditOne(*sec)
	}

	list, err := clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list secrets in namespace %q: %w", ns, err)
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render(fmt.Sprintf("🔍 Secret audit — namespace %q (%d)", ns, len(list.Items))))
	if len(list.Items) == 0 {
		fmt.Println(helpers.CreateMuted("   No secrets found"))
		return nil
	}

	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-32s %-26s %-22s %-20s %s\n", "NAME", "TYPE", "LAST-MODIFIED-BY", "MODIFIED", "ROTATED"))
	b.WriteString(strings.Repeat("─", 124) + "\n")
	for _, s := range list.Items {
		who, when := lastModifier(s)
		rotated := s.Annotations["adhar.io/rotated-at"]
		if rotated == "" {
			rotated = "-"
		}
		b.WriteString(fmt.Sprintf("%-32s %-26s %-22s %-20s %s\n",
			truncate(s.Name, 30), truncate(string(s.Type), 24),
			truncate(who, 20), when, rotated))
	}
	fmt.Print(b.String())
	logger.Info("✅ Secret audit completed")
	return nil
}

func auditOne(s corev1.Secret) error {
	who, when := lastModifier(s)
	keys := make([]string, 0, len(s.Data))
	for k := range s.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔐 Name:            %s\n", s.Name))
	b.WriteString(fmt.Sprintf("📦 Namespace:       %s\n", s.Namespace))
	b.WriteString(fmt.Sprintf("🏷️  Type:            %s\n", s.Type))
	b.WriteString(fmt.Sprintf("🔑 Keys:            %s\n", strings.Join(keys, ", ")))
	b.WriteString(fmt.Sprintf("👤 Managed-by:      %s\n", valueOrDash(s.Labels["adhar.io/managed-by"])))
	b.WriteString(fmt.Sprintf("✍️  Last-modified:   %s by %s\n", when, who))
	b.WriteString(fmt.Sprintf("🕐 Created:         %s\n", formatAge(s.CreationTimestamp.Time)))
	b.WriteString(fmt.Sprintf("🔄 Last-rotated:    %s", valueOrDash(s.Annotations["adhar.io/rotated-at"])))
	fmt.Println(helpers.BorderStyle.Width(80).Render(b.String()))

	fmt.Printf("\n%s\n", helpers.CreateMuted("managedFields history:"))
	for _, mf := range s.ManagedFields {
		t := "-"
		if mf.Time != nil {
			t = mf.Time.Time.Format("2006-01-02 15:04:05")
		}
		fmt.Printf("   • %-24s %-8s %s\n", mf.Manager, mf.Operation, t)
	}
	return nil
}

// lastModifier returns the manager and timestamp of the most recent managedFields
// entry — the closest audit signal Kubernetes retains for who/when a secret changed.
func lastModifier(s corev1.Secret) (string, string) {
	who, when := "-", "-"
	var latest int64
	for _, mf := range s.ManagedFields {
		if mf.Time == nil {
			continue
		}
		if ut := mf.Time.Time.Unix(); ut >= latest {
			latest = ut
			who = mf.Manager
			when = mf.Time.Time.Format("2006-01-02 15:04")
		}
	}
	return who, when
}

func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
