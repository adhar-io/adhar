package service

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var routeCmd = &cobra.Command{
	Use:   "route",
	Short: "Show ingress routes for a service",
	Long: `List the Ingress rules (host/path) that route traffic to the named
Service. This is a read-only view of existing routing.

Examples:
  adhar service route --name=api
  adhar service route --name=web --namespace=prod`,
	RunE: runRoute,
}

func runRoute(cmd *cobra.Command, args []string) error {
	if serviceName == "" {
		return fmt.Errorf("--name is required for routing inspection")
	}

	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("🛣️ Inspecting ingress routes for service %s/%s...", ns, serviceName))

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	ingresses, err := clientset.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing ingresses in %s: %w", ns, err)
	}

	type route struct {
		Ingress string `json:"ingress"`
		Host    string `json:"host"`
		Path    string `json:"path"`
	}
	var routes []route
	for _, ing := range ingresses.Items {
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, p := range rule.HTTP.Paths {
				if p.Backend.Service != nil && p.Backend.Service.Name == serviceName {
					routes = append(routes, route{ing.Name, rule.Host, p.Path})
				}
			}
		}
	}

	if output == "json" {
		return helpers.PrintJSON(routes)
	}
	if output == "yaml" {
		return helpers.PrintYAML(routes)
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🛣️  Ingress Routes"))
	var t strings.Builder
	t.WriteString(fmt.Sprintf("%-28s %-30s %-20s\n", "INGRESS", "HOST", "PATH"))
	t.WriteString(strings.Repeat("─", 80) + "\n")
	if len(routes) == 0 {
		t.WriteString(fmt.Sprintf("(no ingress routes target service %q)\n", serviceName))
	}
	for _, r := range routes {
		host := r.Host
		if host == "" {
			host = "*"
		}
		t.WriteString(fmt.Sprintf("%-28s %-30s %-20s\n", r.Ingress, host, r.Path))
	}
	fmt.Println(helpers.BorderStyle.Width(85).Render(t.String()))
	return nil
}
