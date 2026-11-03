package apps

import (
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"gopkg.in/yaml.v3"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// ApplicationStatusView represents the information surfaced to CLI users.
type ApplicationStatusView struct {
	Name                string              `json:"name" yaml:"name"`
	Namespace           string              `json:"namespace" yaml:"namespace"`
	Phase               string              `json:"phase,omitempty" yaml:"phase,omitempty"`
	SyncStatus          string              `json:"syncStatus,omitempty" yaml:"syncStatus,omitempty"`
	HealthStatus        string              `json:"healthStatus,omitempty" yaml:"healthStatus,omitempty"`
	Revision            string              `json:"revision,omitempty" yaml:"revision,omitempty"`
	Message             string              `json:"message,omitempty" yaml:"message,omitempty"`
	LastSynced          string              `json:"lastSynced,omitempty" yaml:"lastSynced,omitempty"`
	CreatedAt           string              `json:"createdAt,omitempty" yaml:"createdAt,omitempty"`
	Labels              map[string]string   `json:"labels,omitempty" yaml:"labels,omitempty"`
	EnvironmentStatuses []EnvironmentStatus `json:"environmentStatuses,omitempty" yaml:"environmentStatuses,omitempty"`
	Conditions          []StatusCondition   `json:"conditions,omitempty" yaml:"conditions,omitempty"`
}

// StatusCondition captures Crossplane condition data used for detailed views.
type StatusCondition struct {
	Type               string `json:"type" yaml:"type"`
	Status             string `json:"status" yaml:"status"`
	Reason             string `json:"reason,omitempty" yaml:"reason,omitempty"`
	Message            string `json:"message,omitempty" yaml:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty" yaml:"lastTransitionTime,omitempty"`
}

// EnvironmentStatus represents status information for a specific environment deployment.
type EnvironmentStatus struct {
	Environment  string   `json:"environment,omitempty" yaml:"environment,omitempty"`
	Namespace    string   `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Application  string   `json:"application,omitempty" yaml:"application,omitempty"`
	Version      string   `json:"version,omitempty" yaml:"version,omitempty"`
	Health       string   `json:"health,omitempty" yaml:"health,omitempty"`
	SyncStatus   string   `json:"syncStatus,omitempty" yaml:"syncStatus,omitempty"`
	LastDeployed string   `json:"lastDeployed,omitempty" yaml:"lastDeployed,omitempty"`
	Endpoints    []string `json:"endpoints,omitempty" yaml:"endpoints,omitempty"`
}

// Applications are modelled as the Crossplane Application XR
// (platform.adhar.io/v1alpha1). The control plane composes each into an ArgoCD
// Application — the same path the Adhar Console uses.
var applicationGVR = schema.GroupVersionResource{
	Group:    "platform.adhar.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// dynamicClientFor is a dynamic client for the --kubeconfig the command was
// given, or the default kubeconfig.
func dynamicClientFor(kubeconfigPath string) (dynamic.Interface, error) {
	if kubeconfigPath == "" {
		kubeconfigPath = helpers.GetKubeConfigPath()
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
	}
	dc, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}
	return dc, nil
}

// ErrApplicationNotFound indicates the requested Application could not be resolved.
var ErrApplicationNotFound = errors.New("application not found")

// applicationNamespaces is where an Application is looked for when --namespace
// is not given: adhar-system first — every template deploy and every
// Console-created app registers its paved-road Application there — then the
// dev environment, where a --repo or --file deploy without --namespace puts
// one. Until 2026-11-03 status, list and delete looked in dev alone, so
// `adhar application status qua-service` answered "application not found:
// dev/qua-service" for every app the CLI and the Console had scaffolded.
func applicationNamespaces(namespaceSet bool, ns string) []string {
	if namespaceSet && ns != "" {
		return []string{ns}
	}
	return []string{globals.AdharSystemNamespace, globals.DefaultEnvironment()}
}

// findApplication returns the Application named name from the first of
// namespaces that holds one.
func findApplication(ctx context.Context, dc dynamic.Interface, namespaces []string, name string) (*unstructured.Unstructured, error) {
	for _, ns := range namespaces {
		app, err := dc.Resource(applicationGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			return app, nil
		}
		if !k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("get application %s/%s: %w", ns, name, err)
		}
	}
	return nil, fmt.Errorf("%w: %s in %s", ErrApplicationNotFound, name, strings.Join(namespaces, " or "))
}

// GetApplicationStatus retrieves the status for a single Application claim.
func GetApplicationStatus(ctx context.Context, kubeconfigPath, namespace, name string) (*ApplicationStatusView, error) {
	if namespace == "" {
		namespace = globals.DefaultEnvironment()
	}
	return resolveApplicationStatus(ctx, kubeconfigPath, []string{namespace}, name)
}

// resolveApplicationStatus looks the Application up in namespaces, in order,
// and adds what each of its environments' Argo CD Applications reports.
func resolveApplicationStatus(ctx context.Context, kubeconfigPath string, namespaces []string, name string) (*ApplicationStatusView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dc, err := dynamicClientFor(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	return applicationStatusIn(ctx, dc, namespaces, name)
}

func applicationStatusIn(ctx context.Context, dc dynamic.Interface, namespaces []string, name string) (*ApplicationStatusView, error) {
	app, err := findApplication(ctx, dc, namespaces, name)
	if err != nil {
		return nil, err
	}
	view := buildApplicationStatus(app.Object, app.GetNamespace())
	addEnvironmentStatus(ctx, dc, []*ApplicationStatusView{view})
	return view, nil
}

func buildApplicationStatus(obj map[string]interface{}, defaultNamespace string) *ApplicationStatusView {
	statusView := &ApplicationStatusView{
		Name:      stringFrom(obj, "metadata", "name"),
		Namespace: stringFrom(obj, "metadata", "namespace"),
	}

	if statusView.Namespace == "" {
		statusView.Namespace = defaultNamespace
	}

	statusView.Labels = mapStringStringFrom(obj, "metadata", "labels")
	statusView.CreatedAt = stringFrom(obj, "metadata", "creationTimestamp")

	statusMap := mapFrom(obj, "status")
	statusView.Phase = stringFrom(statusMap, "phase")
	statusView.SyncStatus = stringFrom(statusMap, "syncStatus")
	statusView.HealthStatus = stringFrom(statusMap, "healthStatus")
	statusView.LastSynced = stringFrom(statusMap, "lastSynced")
	if statusView.Message == "" {
		statusView.Message = stringFrom(statusMap, "message")
	}

	appStatus := mapFrom(statusMap, "applicationStatus")
	if statusView.SyncStatus == "" {
		statusView.SyncStatus = stringFrom(appStatus, "sync")
	}
	if statusView.HealthStatus == "" {
		statusView.HealthStatus = stringFrom(appStatus, "health")
	}
	if statusView.LastSynced == "" {
		statusView.LastSynced = stringFrom(appStatus, "lastSynced")
	}
	if statusView.Message == "" {
		statusView.Message = stringFrom(appStatus, "message")
	}
	statusView.Revision = stringFrom(appStatus, "revision")
	// The paved-road composition (application-multienv) reports no single sync
	// or health — the application runs in several environments — but a phase
	// (Pending, Progressing, Healthy) that already allows for environments
	// still awaiting their first promotion.
	if statusView.HealthStatus == "" {
		statusView.HealthStatus = statusView.Phase
	}

	statusView.EnvironmentStatuses = extractEnvironmentStatuses(statusMap)
	statusView.Conditions = extractConditions(statusMap)

	return statusView
}

func extractConditions(status map[string]interface{}) []StatusCondition {
	raw := sliceFrom(status, "conditions")
	if len(raw) == 0 {
		return nil
	}

	conditions := make([]StatusCondition, 0, len(raw))
	for _, item := range raw {
		condMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		conditions = append(conditions, StatusCondition{
			Type:               stringFromMap(condMap, "type"),
			Status:             stringFromMap(condMap, "status"),
			Reason:             stringFromMap(condMap, "reason"),
			Message:            stringFromMap(condMap, "message"),
			LastTransitionTime: stringFromMap(condMap, "lastTransitionTime"),
		})
	}

	return conditions
}

// RenderApplicationStatus renders application status in the requested format.
func RenderApplicationStatus(status *ApplicationStatusView, format string, detailed bool) error {
	if status == nil {
		return errors.New("application status is nil")
	}

	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		payload, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal status to json: %w", err)
		}
		fmt.Println(string(payload))
		return nil
	case "yaml":
		payload, err := yaml.Marshal(status)
		if err != nil {
			return fmt.Errorf("marshal status to yaml: %w", err)
		}
		fmt.Print(string(payload))
		return nil
	default:
		return renderStatusTable(status, detailed)
	}
}

func renderStatusTable(status *ApplicationStatusView, detailed bool) error {
	builder := &strings.Builder{}

	fmt.Fprintf(builder, "%s %s\n", helpers.BulletStyle.Render("Application:"), helpers.HighlightStyle.Render(status.Name))
	fmt.Fprintf(builder, "%s %s\n", helpers.BulletStyle.Render("Namespace:"), status.Namespace)
	fmt.Fprintf(builder, "%s %s\n", helpers.BulletStyle.Render("Sync Status:"), displayOrUnknown(status.SyncStatus))
	fmt.Fprintf(builder, "%s %s\n", helpers.BulletStyle.Render("Health:"), displayOrUnknown(status.HealthStatus))
	if status.Revision != "" {
		fmt.Fprintf(builder, "%s %s\n", helpers.BulletStyle.Render("Revision:"), status.Revision)
	}
	if status.LastSynced != "" {
		fmt.Fprintf(builder, "%s %s\n", helpers.BulletStyle.Render("Last Synced:"), status.LastSynced)
	}
	if status.Message != "" {
		fmt.Fprintf(builder, "%s %s\n", helpers.BulletStyle.Render("Message:"), status.Message)
	}
	if status.CreatedAt != "" {
		fmt.Fprintf(builder, "%s %s\n", helpers.BulletStyle.Render("Created:"), formatAge(status.CreatedAt))
	}

	output := helpers.CreateBox(builder.String(), 90)
	fmt.Println(output)

	if len(status.EnvironmentStatuses) > 0 {
		fmt.Println(helpers.TitleStyle.Render("Environment Statuses"))
		for _, env := range status.EnvironmentStatuses {
			helpers.Printf("- %s\n", helpers.HighlightStyle.Render(env.Environment))
			if env.Namespace != "" {
				fmt.Printf("  • Namespace: %s\n", env.Namespace)
			}
			if env.Application != "" {
				fmt.Printf("  • Argo CD Application: %s\n", env.Application)
			}
			if env.Version != "" {
				fmt.Printf("  • Version: %s\n", env.Version)
			}
			fmt.Printf("  • Sync: %s\n", displayOrUnknown(env.SyncStatus))
			fmt.Printf("  • Health: %s\n", displayOrUnknown(env.Health))
			if env.LastDeployed != "" {
				fmt.Printf("  • Last Deployed: %s\n", env.LastDeployed)
			}
			if len(env.Endpoints) > 0 {
				fmt.Printf("  • Endpoints: %s\n", strings.Join(env.Endpoints, ", "))
			}
		}
	}

	if detailed && len(status.Conditions) > 0 {
		fmt.Println(helpers.TitleStyle.Render("Conditions"))
		for _, condition := range status.Conditions {
			// Opens the line; the reason below is appended to it.
			helpers.Printf("- %s %s", condition.Type, helpers.HighlightStyle.Render(condition.Status))
			if condition.Reason != "" {
				fmt.Printf(" (reason: %s)", condition.Reason)
			}
			fmt.Println()
			if condition.LastTransitionTime != "" {
				fmt.Printf("  • Last Transition: %s\n", condition.LastTransitionTime)
			}
			if condition.Message != "" {
				fmt.Printf("  • Message: %s\n", condition.Message)
			}
		}
	}

	return nil
}

func displayOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return helpers.WarningStyle.Render("Unknown")
	}
	return value
}

func mapFrom(parent map[string]interface{}, keys ...string) map[string]interface{} {
	current := parent
	for _, key := range keys {
		val, ok := current[key].(map[string]interface{})
		if !ok {
			return map[string]interface{}{}
		}
		current = val
	}
	return current
}

func sliceFrom(parent map[string]interface{}, key string) []interface{} {
	if parent == nil {
		return nil
	}
	if val, ok := parent[key].([]interface{}); ok {
		return val
	}
	return nil
}

func stringFrom(parent map[string]interface{}, keys ...string) string {
	if len(keys) == 0 {
		return ""
	}
	return stringFromMap(mapFrom(parent, keys[:len(keys)-1]...), keys[len(keys)-1])
}

func stringFromMap(parent map[string]interface{}, key string) string {
	if parent == nil {
		return ""
	}
	if val, ok := parent[key].(string); ok {
		return val
	}
	return ""
}

func mapStringStringFrom(parent map[string]interface{}, keys ...string) map[string]string {
	result := map[string]string{}
	m := mapFrom(parent, keys...)
	for k, v := range m {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result
}

func extractEnvironmentStatuses(status map[string]interface{}) []EnvironmentStatus {
	raw := sliceFrom(status, "environmentStatuses")
	if len(raw) == 0 {
		// application-multienv: status.environments lists the project's
		// environments with the Argo CD Application each one runs as.
		var environments []EnvironmentStatus
		for _, item := range sliceFrom(status, "environments") {
			if m, ok := item.(map[string]interface{}); ok {
				environments = append(environments, EnvironmentStatus{
					Environment: stringFromMap(m, "name"),
					Namespace:   stringFromMap(m, "namespace"),
					Application: stringFromMap(m, "argoApplication"),
				})
			}
		}
		return environments
	}

	environments := make([]EnvironmentStatus, 0, len(raw))
	for _, item := range raw {
		envMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		env := EnvironmentStatus{
			Environment:  stringFromMap(mapFrom(envMap, "environmentRef"), "name"),
			Version:      stringFromMap(envMap, "version"),
			Health:       stringFromMap(envMap, "health"),
			SyncStatus:   stringFromMap(envMap, "syncStatus"),
			LastDeployed: stringFromMap(envMap, "lastDeployedTimestamp"),
			Endpoints:    sliceString(envMap, "endpoints"),
		}

		if env.Environment == "" {
			env.Environment = stringFromMap(envMap, "environment")
		}
		if env.LastDeployed == "" {
			env.LastDeployed = stringFromMap(envMap, "lastSynced")
		}

		environments = append(environments, env)
	}

	return environments
}

func sliceString(parent map[string]interface{}, key string) []string {
	raw := sliceFrom(parent, key)
	if len(raw) == 0 {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func formatAge(ts string) string {
	if ts == "" {
		return helpers.WarningStyle.Render("unknown")
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ListApplications returns all Application claims in scope.
func ListApplications(ctx context.Context, kubeconfigPath, namespace string, allNamespaces bool, selector string) ([]*ApplicationStatusView, error) {
	if namespace == "" {
		namespace = globals.DefaultEnvironment()
	}
	return listApplicationsIn(ctx, kubeconfigPath, []string{namespace}, allNamespaces, selector)
}

// listApplicationsIn lists the Applications in each of namespaces (or in all
// of them), adding what their environments' Argo CD Applications report. A
// namespace that cannot be listed is skipped while another can; when none can,
// the first error is returned.
func listApplicationsIn(ctx context.Context, kubeconfigPath string, namespaces []string, allNamespaces bool, selector string) ([]*ApplicationStatusView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dc, err := dynamicClientFor(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	return listApplicationsWith(ctx, dc, namespaces, allNamespaces, selector)
}

func listApplicationsWith(ctx context.Context, dc dynamic.Interface, namespaces []string, allNamespaces bool, selector string) ([]*ApplicationStatusView, error) {
	if allNamespaces {
		namespaces = []string{metav1.NamespaceAll}
	}
	opts := metav1.ListOptions{}
	if strings.TrimSpace(selector) != "" {
		opts.LabelSelector = selector
	}

	var statuses []*ApplicationStatusView
	var firstErr error
	listed := 0
	for _, ns := range namespaces {
		appList, err := dc.Resource(applicationGVR).Namespace(ns).List(ctx, opts)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("list applications: %w", err)
			}
			continue
		}
		listed++
		for _, item := range appList.Items {
			statuses = append(statuses, buildApplicationStatus(item.Object, ns))
		}
	}
	if listed == 0 && firstErr != nil {
		return nil, firstErr
	}
	addEnvironmentStatus(ctx, dc, statuses)
	return statuses, nil
}

var argoApplicationGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// addEnvironmentStatus fills each environment of a paved-road Application with
// what its Argo CD Application reports, and gives the Application the sync
// status they share (or the first that differs). Best effort: an Argo CD that
// cannot be read leaves the environments as the control plane listed them.
func addEnvironmentStatus(ctx context.Context, dc dynamic.Interface, views []*ApplicationStatusView) {
	selector := "adhar.io/application"
	want := 0
	for _, v := range views {
		for _, e := range v.EnvironmentStatuses {
			if e.Application != "" {
				want++
			}
		}
	}
	if want == 0 {
		return
	}
	if len(views) == 1 {
		selector += "=" + views[0].Name
	}
	list, err := dc.Resource(argoApplicationGVR).Namespace(globals.AdharSystemNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return
	}
	byName := map[string]map[string]interface{}{}
	for _, a := range list.Items {
		byName[a.GetName()] = a.Object
	}
	for _, v := range views {
		applyEnvironmentStatus(v, byName)
	}
}

// applyEnvironmentStatus is the pure half of addEnvironmentStatus.
func applyEnvironmentStatus(v *ApplicationStatusView, argoApps map[string]map[string]interface{}) {
	aggregate := ""
	for i := range v.EnvironmentStatuses {
		e := &v.EnvironmentStatuses[i]
		obj, ok := argoApps[e.Application]
		if !ok {
			continue
		}
		e.SyncStatus = stringFrom(obj, "status", "sync", "status")
		e.Health = stringFrom(obj, "status", "health", "status")
		e.Version = stringFrom(obj, "status", "sync", "revision")
		e.LastDeployed = stringFrom(obj, "status", "operationState", "finishedAt")
		switch {
		case e.SyncStatus == "":
		case aggregate == "" || aggregate == "Synced":
			aggregate = e.SyncStatus
		}
	}
	if v.SyncStatus == "" {
		v.SyncStatus = aggregate
	}
}

// RenderApplicationStatusList renders a collection of application statuses.
func RenderApplicationStatusList(statuses []*ApplicationStatusView, format string, showLabels bool) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		payload, err := json.MarshalIndent(statuses, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal status list to json: %w", err)
		}
		fmt.Println(string(payload))
		return nil
	case "yaml":
		payload, err := yaml.Marshal(statuses)
		if err != nil {
			return fmt.Errorf("marshal status list to yaml: %w", err)
		}
		fmt.Print(string(payload))
		return nil
	}

	if len(statuses) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No applications found."))
		return nil
	}

	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tNAMESPACE\tSYNC\tHEALTH\tREVISION\tAGE")
	for _, status := range statuses {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			status.Name,
			status.Namespace,
			displayOrUnknown(status.SyncStatus),
			displayOrUnknown(status.HealthStatus),
			valueOrDash(status.Revision),
			formatAge(status.CreatedAt),
		)
	}
	writer.Flush()

	if showLabels {
		fmt.Println()
		for _, status := range statuses {
			if len(status.Labels) == 0 {
				continue
			}
			fmt.Printf("%s\n", helpers.TitleStyle.Render(status.Name))
			for k, v := range status.Labels {
				fmt.Printf("  %s = %s\n", k, v)
			}
		}
	}

	return nil
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// applyApplication ensures the Application claim exists with the desired spec.
func applyApplication(ctx context.Context, kubeconfigPath string, app *unstructured.Unstructured) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if kubeconfigPath == "" {
		kubeconfigPath = helpers.GetKubeConfigPath()
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("build kubeconfig: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	namespace := app.GetNamespace()
	if namespace == "" {
		namespace = globals.DefaultEnvironment()
		app.SetNamespace(namespace)
	}

	resource := dynamicClient.Resource(applicationGVR).Namespace(namespace)

	existing, err := resource.Get(ctx, app.GetName(), metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			if _, createErr := resource.Create(ctx, app, metav1.CreateOptions{}); createErr != nil {
				return fmt.Errorf("create application: %w", createErr)
			}
			return nil
		}
		return fmt.Errorf("get application: %w", err)
	}

	app.SetResourceVersion(existing.GetResourceVersion())
	if _, err := resource.Update(ctx, app, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update application: %w", err)
	}
	return nil
}

// waitForApplicationReady waits for an application to report healthy and synced status.
func waitForApplicationReady(ctx context.Context, kubeconfigPath, namespace, name string, timeout time.Duration) (*ApplicationStatusView, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if namespace == "" {
		namespace = globals.DefaultEnvironment()
	}

	dc, err := dynamicClientFor(kubeconfigPath)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	last := ""
	for {
		app, err := findApplication(ctx, dc, []string{namespace}, name)
		if err == nil {
			status := buildApplicationStatus(app.Object, namespace)
			if isApplicationReady(status) {
				// The final view carries each environment's own state.
				addEnvironmentStatus(context.WithoutCancel(ctx), dc, []*ApplicationStatusView{status})
				return status, nil
			}
			// A paved-road app takes a CI build and a promotion to come up, so say
			// where it is rather than waiting in silence.
			now := status.Phase
			if status.Message != "" {
				now = strings.TrimPrefix(now+": "+status.Message, ": ")
			}
			if now != "" && now != last {
				logger.Info("◌ " + now)
				last = now
			}
		} else if !errors.Is(err, ErrApplicationNotFound) && ctx.Err() == nil {
			return nil, err
		}

		select {
		case <-ctx.Done():
			where := ""
			if last != "" {
				where = " (last: " + last + ")"
			}
			return nil, fmt.Errorf("timed out waiting for application %s to become ready%s: %w", name, where, ctx.Err())
		case <-ticker.C:
		}
	}
}

// isApplicationReady says whether the Application's control plane reports it
// up: the paved-road phase Healthy (application-multienv and application-argocd
// both write it), or a synced and healthy Argo CD state.
func isApplicationReady(status *ApplicationStatusView) bool {
	if status == nil {
		return false
	}
	if strings.EqualFold(status.Phase, "healthy") {
		return true
	}
	sync := strings.EqualFold(status.SyncStatus, "synced") || strings.EqualFold(status.SyncStatus, "ready")
	health := strings.EqualFold(status.HealthStatus, "healthy") || strings.EqualFold(status.HealthStatus, "ready")
	return sync && health
}
