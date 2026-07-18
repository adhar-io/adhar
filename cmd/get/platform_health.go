package get

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ArgoCD application health states used for the package summary.
const (
	healthHealthy     = "Healthy"
	healthProgressing = "Progressing"
	healthUnknown     = "Unknown"
)

// PlatformConditionInfo is a display-friendly view of an AdharPlatform condition.
type PlatformConditionInfo struct {
	Type    string
	Status  string
	Reason  string
	Message string
}

// PackageAppHealth is one ArgoCD-managed platform package's health.
type PackageAppHealth struct {
	Name   string
	Health string
	Sync   string
}

// PackageHealthSummary is the one-view readiness summary of enabled packages.
type PackageHealthSummary struct {
	Total    int
	Healthy  int
	Syncing  int
	Degraded int
	Packages []PackageAppHealth
}

// getControllerRuntimeClient builds a controller-runtime client with the
// platform scheme (Adhar CRDs + ArgoCD types) from the default kubeconfig.
func getControllerRuntimeClient() (client.Client, error) {
	kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	return client.New(config, client.Options{Scheme: k8s.GetScheme()})
}

// collectPlatformConditions reads the AdharPlatform resource's conditions.
// Returns nil (no error) when no AdharPlatform exists — e.g. a non-Adhar cluster.
func collectPlatformConditions(ctx context.Context) []PlatformConditionInfo {
	cl, err := getControllerRuntimeClient()
	if err != nil {
		return nil
	}

	platforms := &v1alpha1.AdharPlatformList{}
	if err := cl.List(ctx, platforms, client.InNamespace(globals.AdharSystemNamespace)); err != nil || len(platforms.Items) == 0 {
		return nil
	}

	conds := platforms.Items[0].Status.Conditions
	out := make([]PlatformConditionInfo, 0, len(conds))
	for _, c := range conds {
		out = append(out, PlatformConditionInfo{
			Type:    c.Type,
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// collectPackageHealth summarizes the health of every ArgoCD-managed platform
// package (the enabled entries of the platform ApplicationSet).
func collectPackageHealth(ctx context.Context) *PackageHealthSummary {
	cl, err := getControllerRuntimeClient()
	if err != nil {
		return nil
	}

	apps := &argov1alpha1.ApplicationList{}
	if err := cl.List(ctx, apps, client.InNamespace(globals.AdharSystemNamespace)); err != nil || len(apps.Items) == 0 {
		return nil
	}

	summary := &PackageHealthSummary{}
	for i := range apps.Items {
		app := apps.Items[i]
		health := string(app.Status.Health.Status)
		if health == "" {
			health = healthUnknown
		}
		sync := string(app.Status.Sync.Status)
		if sync == "" {
			sync = healthUnknown
		}

		summary.Total++
		switch health {
		case healthHealthy:
			summary.Healthy++
		case healthProgressing, "Suspended", healthUnknown:
			summary.Syncing++
		default: // Degraded, Missing
			summary.Degraded++
		}
		summary.Packages = append(summary.Packages, PackageAppHealth{
			Name:   app.Name,
			Health: health,
			Sync:   sync,
		})
	}
	sort.Slice(summary.Packages, func(i, j int) bool { return summary.Packages[i].Name < summary.Packages[j].Name })
	return summary
}

func healthIcon(health string) string {
	switch health {
	case healthHealthy:
		return "✅"
	case healthProgressing:
		return "🔄"
	case "Degraded", "Missing":
		return "❌"
	default:
		return "⚪"
	}
}

// displayPlatformHealth renders the platform conditions and the package
// readiness dashboard sections of `adhar get status`.
func displayPlatformHealth(conditions []PlatformConditionInfo, packages *PackageHealthSummary) {
	if len(conditions) > 0 {
		fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🧩 Platform Conditions"))

		var b strings.Builder
		fmt.Fprintf(&b, "%-20s %-8s %-28s %s\n", "CONDITION", "STATUS", "REASON", "MESSAGE")
		b.WriteString(strings.Repeat("─", 75) + "\n")
		for _, c := range conditions {
			icon := "✅"
			if c.Status != "True" {
				icon = "❌"
			}
			msg := c.Message
			if len(msg) > 40 {
				msg = msg[:37] + "..."
			}
			fmt.Fprintf(&b, "%-20s %-8s %-28s %s\n", c.Type, icon+" "+c.Status, c.Reason, msg)
		}
		fmt.Println(helpers.BorderStyle.Width(100).Render(b.String()))
	}

	if packages != nil && packages.Total > 0 {
		fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📦 Platform Packages"))

		var b strings.Builder
		fmt.Fprintf(&b, "✅ Healthy: %d   🔄 Progressing: %d   ❌ Degraded: %d   (total: %d)\n",
			packages.Healthy, packages.Syncing, packages.Degraded, packages.Total)
		b.WriteString(strings.Repeat("─", 75) + "\n")
		fmt.Fprintf(&b, "%-35s %-18s %-12s\n", "PACKAGE", "HEALTH", "SYNC")
		for _, p := range packages.Packages {
			fmt.Fprintf(&b, "%-35s %-18s %-12s\n", p.Name, healthIcon(p.Health)+" "+p.Health, p.Sync)
		}
		fmt.Println(helpers.BorderStyle.Width(80).Render(b.String()))
	}
}

// AccessURL is one platform UI endpoint routed through the Gateway.
type AccessURL struct {
	Name string
	URL  string
}

// collectAccessURLs lists every HTTPRoute hostname as a browsable URL, using
// the platform's configured HTTPS port.
func collectAccessURLs(ctx context.Context) []AccessURL {
	cl, err := getControllerRuntimeClient()
	if err != nil {
		return nil
	}

	port := "8443"
	platforms := &v1alpha1.AdharPlatformList{}
	if err := cl.List(ctx, platforms, client.InNamespace(globals.AdharSystemNamespace)); err == nil && len(platforms.Items) > 0 {
		if p := platforms.Items[0].Spec.BuildCustomization.Port; p != "" {
			port = p
		}
	}

	routes := &unstructured.UnstructuredList{}
	routes.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRouteList",
	})
	if err := cl.List(ctx, routes); err != nil {
		return nil
	}

	var out []AccessURL
	for i := range routes.Items {
		r := routes.Items[i]
		hosts, _, _ := unstructured.NestedStringSlice(r.Object, "spec", "hostnames")
		for _, h := range hosts {
			if h == "localhost" || strings.Contains(h, "*") {
				continue
			}
			out = append(out, AccessURL{
				Name: r.GetName(),
				URL:  fmt.Sprintf("https://%s:%s", h, port),
			})
			break // first real hostname per route
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// displayAccessURLs renders the browsable platform endpoints and quick actions.
func displayAccessURLs(urls []AccessURL) {
	if len(urls) == 0 {
		return
	}
	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🔗 Access URLs"))

	var b strings.Builder
	for _, u := range urls {
		fmt.Fprintf(&b, "%-18s %s\n", u.Name, u.URL)
	}
	b.WriteString(strings.Repeat("─", 75) + "\n")
	b.WriteString("🔑 Credentials: adhar get secrets   ·   📦 Apps: adhar get apps\n")
	fmt.Println(helpers.BorderStyle.Width(80).Render(b.String()))
}

// attachPlatformHealth enriches PlatformStatus with CR conditions, package
// health, and access URLs; all best-effort so `get status` still works on
// foreign clusters.
func attachPlatformHealth(status *PlatformStatus) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status.Platform = collectPlatformConditions(ctx)
	status.Packages = collectPackageHealth(ctx)
	status.URLs = collectAccessURLs(ctx)
}
