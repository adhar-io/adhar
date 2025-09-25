package apps

import (
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
	Version      string   `json:"version,omitempty" yaml:"version,omitempty"`
	Health       string   `json:"health,omitempty" yaml:"health,omitempty"`
	SyncStatus   string   `json:"syncStatus,omitempty" yaml:"syncStatus,omitempty"`
	LastDeployed string   `json:"lastDeployed,omitempty" yaml:"lastDeployed,omitempty"`
	Endpoints    []string `json:"endpoints,omitempty" yaml:"endpoints,omitempty"`
}

var applicationGVR = schema.GroupVersionResource{
	Group:    "platform.adhar.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// ErrApplicationNotFound indicates the requested Application claim could not be resolved.
var ErrApplicationNotFound = errors.New("application not found")

// GetApplicationStatus retrieves the status for a single Application claim.
func GetApplicationStatus(ctx context.Context, kubeconfigPath, namespace, name string) (*ApplicationStatusView, error) {
	if namespace == "" {
		namespace = "default"
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if kubeconfigPath == "" {
		kubeconfigPath = helpers.GetKubeConfigPath()
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	app, err := dynamicClient.Resource(applicationGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s", ErrApplicationNotFound, namespace, name)
		}
		return nil, fmt.Errorf("get application: %w", err)
	}

	return buildApplicationStatus(app.Object, namespace), nil
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
			fmt.Printf("- %s\n", helpers.HighlightStyle.Render(env.Environment))
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
			fmt.Printf("- %s %s", condition.Type, helpers.HighlightStyle.Render(condition.Status))
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
		return nil
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
	if ctx == nil {
		ctx = context.Background()
	}

	if kubeconfigPath == "" {
		kubeconfigPath = helpers.GetKubeConfigPath()
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	listNamespace := namespace
	if allNamespaces {
		listNamespace = metav1.NamespaceAll
	} else if listNamespace == "" {
		listNamespace = "default"
	}

	opts := metav1.ListOptions{}
	if strings.TrimSpace(selector) != "" {
		opts.LabelSelector = selector
	}

	appList, err := dynamicClient.Resource(applicationGVR).Namespace(listNamespace).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}

	statuses := make([]*ApplicationStatusView, 0, len(appList.Items))
	for _, item := range appList.Items {
		status := buildApplicationStatus(item.Object, namespace)
		statuses = append(statuses, status)
	}

	return statuses, nil
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
		namespace = "default"
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
		namespace = "default"
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		status, err := GetApplicationStatus(ctx, kubeconfigPath, namespace, name)
		if err == nil && isApplicationReady(status) {
			return status, nil
		}
		if err != nil && !errors.Is(err, ErrApplicationNotFound) {
			return nil, err
		}

		select {
		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("timed out waiting for application %s to become ready: %w", name, err)
			}
			return nil, fmt.Errorf("timed out waiting for application %s to become ready", name)
		case <-ticker.C:
		}
	}
}

func isApplicationReady(status *ApplicationStatusView) bool {
	if status == nil {
		return false
	}
	sync := strings.EqualFold(status.SyncStatus, "synced") || strings.EqualFold(status.SyncStatus, "ready")
	health := strings.EqualFold(status.HealthStatus, "healthy") || strings.EqualFold(status.HealthStatus, "ready")
	return sync && health
}

// *** End Patch
