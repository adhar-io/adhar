package get

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// statusCmd represents the status command.
var statusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"st", "health"},
	Short:   "Show live platform health and component readiness",
	Long:    "Query the Kubernetes cluster for Adhar core components, pods, and resources to present a concise health snapshot.",
	RunE:    runGetStatus,
}

type componentProbe struct {
	name      string
	icon      string
	namespace string
	selector  string
	kinds     []string // deployment|statefulset|daemonset
}

type componentStatus struct {
	name      string
	icon      string
	namespace string
	desired   int32
	ready     int32
	state     string
	version   string
}

type workloadSummary struct {
	total      int
	running    int
	pending    int
	failed     int
	succeeded  int
	crashloops int
	restarts   int
}

type clusterSummary struct {
	k8sVersion string
	nodesReady int
	nodesTotal int
	namespaces int
	services   int
	secrets    int
	configmaps int
	pvs        int
}

var (
	watchStatus    bool
	serviceDetails bool
)

func init() {
	statusCmd.Flags().BoolVarP(&watchStatus, "watch", "w", false, "Watch status changes in real-time (not implemented)")
	statusCmd.Flags().BoolVar(&serviceDetails, "service-details", false, "Reserved for future detailed output")
}

func runGetStatus(_ *cobra.Command, _ []string) error {
	logger.Info("📡 Probing platform status...")

	clientset, err := getKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to build kubernetes client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cluster, err := summarizeCluster(ctx, clientset)
	if err != nil {
		return err
	}

	workloads, err := summarizeWorkloads(ctx, clientset)
	if err != nil {
		return err
	}

	components := summarizeComponents(ctx, clientset)

	renderStatus(cluster, workloads, components)
	return nil
}

func summarizeCluster(ctx context.Context, clientset *kubernetes.Clientset) (clusterSummary, error) {
	var summary clusterSummary

	if v, err := clientset.Discovery().ServerVersion(); err == nil {
		summary.k8sVersion = v.GitVersion
	}

	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return summary, fmt.Errorf("listing nodes: %w", err)
	}
	summary.nodesTotal = len(nodes.Items)
	for _, n := range nodes.Items {
		if nodeReady(&n) {
			summary.nodesReady++
		}
	}

	namespaces, _ := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	services, _ := clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	secrets, _ := clientset.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	configmaps, _ := clientset.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
	pvs, _ := clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})

	summary.namespaces = len(namespaces.Items)
	summary.services = len(services.Items)
	summary.secrets = len(secrets.Items)
	summary.configmaps = len(configmaps.Items)
	summary.pvs = len(pvs.Items)

	return summary, nil
}

func summarizeWorkloads(ctx context.Context, clientset *kubernetes.Clientset) (workloadSummary, error) {
	var ws workloadSummary

	pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return ws, fmt.Errorf("listing pods: %w", err)
	}

	ws.total = len(pods.Items)
	for i := range pods.Items {
		p := pods.Items[i]
		switch p.Status.Phase {
		case corev1.PodRunning:
			ws.running++
		case corev1.PodPending:
			ws.pending++
		case corev1.PodFailed:
			ws.failed++
		case corev1.PodSucceeded:
			ws.succeeded++
		}

		for _, cs := range p.Status.ContainerStatuses {
			ws.restarts += int(cs.RestartCount)
			if cs.State.Waiting != nil && strings.Contains(strings.ToLower(cs.State.Waiting.Reason), "crashloop") {
				ws.crashloops++
				break
			}
		}
	}

	return ws, nil
}

func summarizeComponents(ctx context.Context, clientset *kubernetes.Clientset) []componentStatus {
	probes := []componentProbe{
		{name: "ArgoCD API", icon: "🚀", namespace: "adhar-system", selector: "app.kubernetes.io/name=argocd-server", kinds: []string{"deployment"}},
		{name: "ArgoCD Controller", icon: "🧭", namespace: "adhar-system", selector: "app.kubernetes.io/name=argocd-application-controller", kinds: []string{"statefulset"}},
		{name: "ArgoCD Repo", icon: "📦", namespace: "adhar-system", selector: "app.kubernetes.io/name=argocd-repo-server", kinds: []string{"deployment"}},
		{name: "Gitea", icon: "🦊", namespace: "adhar-system", selector: "app=gitea", kinds: []string{"deployment"}},
	}

	out := make([]componentStatus, 0, len(probes)+1)
	for _, probe := range probes {
		cs := inspectComponent(ctx, clientset, probe)
		out = append(out, cs)
	}
	// Cilium runs as both an agent DaemonSet and an operator Deployment.
	// Check both namespaces and common label patterns so the status reflects reality.
	out = append(out, inspectCilium(ctx, clientset))
	return out
}

func inspectComponent(ctx context.Context, clientset *kubernetes.Clientset, probe componentProbe) componentStatus {
	cs := componentStatus{
		name:      probe.name,
		icon:      probe.icon,
		namespace: probe.namespace,
		state:     "❌ Not Found",
	}

	sel, _ := labels.Parse(probe.selector)
	listOpts := metav1.ListOptions{LabelSelector: sel.String()}

	for _, kind := range probe.kinds {
		switch kind {
		case "deployment":
			list, err := clientset.AppsV1().Deployments(probe.namespace).List(ctx, listOpts)
			if err == nil && len(list.Items) > 0 {
				return deploymentStatus(list.Items[0], cs)
			}
		case "statefulset":
			list, err := clientset.AppsV1().StatefulSets(probe.namespace).List(ctx, listOpts)
			if err == nil && len(list.Items) > 0 {
				return statefulSetStatus(list.Items[0], cs)
			}
		case "daemonset":
			list, err := clientset.AppsV1().DaemonSets(probe.namespace).List(ctx, listOpts)
			if err == nil && len(list.Items) > 0 {
				return daemonSetStatus(list.Items[0], cs)
			}
		}
	}

	// Fallback to pods if no controller found
	pods, err := clientset.CoreV1().Pods(probe.namespace).List(ctx, listOpts)
	if err == nil && len(pods.Items) > 0 {
		var ready int32
		for _, p := range pods.Items {
			if podReady(&p) {
				ready++
			}
		}
		cs.desired = int32(len(pods.Items))
		cs.ready = ready
		cs.state = readinessState(cs.ready, cs.desired)
	}

	return cs
}

// inspectCilium inspects the Cilium agent DaemonSet and operator Deployment to
// provide an accurate readiness signal regardless of namespace or label
// variations between clusters.
func inspectCilium(ctx context.Context, clientset *kubernetes.Clientset) componentStatus {
	cs := componentStatus{
		name:      "Cilium",
		icon:      "🕸️",
		namespace: "kube-system",
		state:     "❌ Not Found",
	}

	namespaces := []string{"adhar-system", "kube-system"}
	agentSelectors := []string{
		"k8s-app=cilium",
		"app.kubernetes.io/name=cilium-agent",
	}

	operatorSelectors := []string{
		"k8s-app=cilium-operator",
		"app.kubernetes.io/name=cilium-operator",
		"name=cilium-operator",
	}

	var agentFound bool

	// Try to resolve the agent DaemonSet first
	for _, ns := range namespaces {
		for _, selStr := range agentSelectors {
			sel, err := labels.Parse(selStr)
			if err != nil {
				continue
			}
			list, err := clientset.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
			if err == nil && len(list.Items) > 0 {
				ds := list.Items[0]
				ready := ds.Status.NumberReady
				if ds.Status.NumberAvailable > ready {
					ready = ds.Status.NumberAvailable
				}
				desired := ds.Status.DesiredNumberScheduled
				if desired == 0 && ready > 0 {
					desired = ready
				}

				cs.namespace = ns
				cs.desired = desired
				cs.ready = ready
				cs.version = firstContainerVersion(ds.Spec.Template.Spec.Containers)
				cs.state = readinessState(cs.ready, cs.desired)
				agentFound = true
				break
			}
		}
		if agentFound {
			break
		}
	}

	operatorHealthy := false

	// Check operator Deployment health; if the agent is healthy but operator is
	// missing, mark as degraded to reflect partial readiness.
	for _, ns := range namespaces {
		for _, selStr := range operatorSelectors {
			sel, err := labels.Parse(selStr)
			if err != nil {
				continue
			}
			list, err := clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
			if err == nil && len(list.Items) > 0 {
				dep := list.Items[0]
				if dep.Status.ReadyReplicas > 0 {
					operatorHealthy = true
				}

				// If the agent wasn't found, use the operator to populate basics
				if !agentFound {
					cs.namespace = ns
					cs.desired = safeReplicaCount(dep.Spec.Replicas)
					cs.ready = dep.Status.ReadyReplicas
					cs.version = firstContainerVersion(dep.Spec.Template.Spec.Containers)
					cs.state = readinessState(cs.ready, cs.desired)
				}
				break
			}
		}
		if operatorHealthy {
			break
		}
	}

	// If agent is present but operator is missing or not ready, show degraded
	if agentFound && !operatorHealthy && (cs.state == "✅ Healthy" || cs.state == "⚠️ Degraded") {
		cs.state = "⚠️ Degraded"
	}

	return cs
}

func deploymentStatus(dep appsv1.Deployment, base componentStatus) componentStatus {
	base.desired = safeReplicaCount(dep.Spec.Replicas)
	base.ready = dep.Status.ReadyReplicas
	base.version = firstContainerVersion(dep.Spec.Template.Spec.Containers)
	base.state = readinessState(base.ready, base.desired)
	return base
}

func statefulSetStatus(sts appsv1.StatefulSet, base componentStatus) componentStatus {
	base.desired = safeReplicaCount(sts.Spec.Replicas)
	base.ready = sts.Status.ReadyReplicas
	base.version = firstContainerVersion(sts.Spec.Template.Spec.Containers)
	base.state = readinessState(base.ready, base.desired)
	return base
}

func daemonSetStatus(ds appsv1.DaemonSet, base componentStatus) componentStatus {
	ready := ds.Status.NumberReady
	if ds.Status.NumberAvailable > ready {
		ready = ds.Status.NumberAvailable
	}
	desired := ds.Status.DesiredNumberScheduled
	if desired == 0 && ready > 0 {
		desired = ready
	}
	base.desired = desired
	base.ready = ready
	base.version = firstContainerVersion(ds.Spec.Template.Spec.Containers)
	base.state = readinessState(base.ready, base.desired)
	return base
}

func readinessState(ready, desired int32) string {
	switch {
	case desired == 0:
		return "⚠️ Pending"
	case ready == desired:
		return "✅ Healthy"
	case ready > 0:
		return "⚠️ Degraded"
	default:
		return "❌ Unavailable"
	}
}

func firstContainerVersion(containers []corev1.Container) string {
	if len(containers) == 0 {
		return ""
	}
	image := containers[0].Image
	if idx := strings.LastIndex(image, ":"); idx != -1 && idx+1 < len(image) {
		return image[idx+1:]
	}
	return ""
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func safeReplicaCount(val *int32) int32 {
	if val == nil {
		return 1
	}
	return *val
}

func renderStatus(cluster clusterSummary, workloads workloadSummary, components []componentStatus) {
	now := time.Now().Format("15:04:05 MST")

	overallState := "✅ Healthy"
	if cluster.nodesReady < cluster.nodesTotal || !allHealthy(components) || workloads.crashloops > 0 || workloads.failed > 0 {
		overallState = "⚠️ Attention Needed"
	}

	header := fmt.Sprintf(
		"%s  Adhar Platform Status\n%s",
		overallState,
		helpers.InfoStyle.Render(fmt.Sprintf("Kubernetes %s  •  %s", fallback(cluster.k8sVersion, "unknown"), now)),
	)
	fmt.Println(helpers.BorderStyle.Width(96).Render(header))

	clusterContent := fmt.Sprintf(
		"%s\n  %s Nodes: %d/%d ready\n  %s Namespaces: %d  Services: %d  ConfigMaps: %d  Secrets: %d  PVs: %d\n  %s Pods: %d total (running %d / pending %d / failed %d / succeeded %d)\n  %s Restarts: %d  CrashLoops: %d",
		helpers.TitleStyle.Render("Cluster & Workloads"),
		helpers.HighlightStyle.Render("🔧"),
		cluster.nodesReady, cluster.nodesTotal,
		helpers.HighlightStyle.Render("📂"),
		cluster.namespaces, cluster.services, cluster.configmaps, cluster.secrets, cluster.pvs,
		helpers.HighlightStyle.Render("📦"),
		workloads.total, workloads.running, workloads.pending, workloads.failed, workloads.succeeded,
		helpers.HighlightStyle.Render("♻️"),
		workloads.restarts, workloads.crashloops,
	)
	fmt.Println(helpers.BorderStyle.Width(96).Render(clusterContent))

	var b strings.Builder
	b.WriteString(helpers.TitleStyle.Render("Core Components"))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("%-28s %-16s %-10s %-10s %-10s\n", "Component", "State", "Ready", "Desired", "Version"))
	b.WriteString(strings.Repeat("─", 90) + "\n")
	for _, c := range components {
		b.WriteString(fmt.Sprintf("%-28s %-16s %-10d %-10d %-10s\n",
			c.icon+" "+c.name, stateBadge(c.state), c.ready, c.desired, fallback(c.version, "n/a"),
		))
	}
	fmt.Println(helpers.BorderStyle.Width(96).Render(b.String()))

	if cluster.nodesReady == cluster.nodesTotal && allHealthy(components) && workloads.crashloops == 0 && workloads.failed == 0 {
		fmt.Println(helpers.SuccessStyle.Render("✅ Platform looks healthy"))
	} else {
		fmt.Println(helpers.WarningStyle.Render("⚠️  Some components need attention"))
	}
}

func stateBadge(state string) string {
	switch {
	case strings.Contains(state, "Healthy"):
		return helpers.SuccessStyle.Render(state)
	case strings.Contains(state, "Degraded") || strings.Contains(state, "Pending"):
		return helpers.WarningStyle.Render(state)
	default:
		return helpers.ErrorStyle.Render(state)
	}
}

func allHealthy(components []componentStatus) bool {
	for _, c := range components {
		if c.state != "✅ Healthy" {
			return false
		}
	}
	return true
}

func fallback(val, alt string) string {
	if strings.TrimSpace(val) == "" {
		return alt
	}
	return val
}

// collectPlatformStatus is used by other commands to fetch a compact health snapshot.
type PlatformStatus struct {
	OverallStatus  string
	HealthScore    int
	PlatformUptime time.Duration
}

func collectPlatformStatus(clientset *kubernetes.Clientset) (*PlatformStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cluster, err := summarizeCluster(ctx, clientset)
	if err != nil {
		return nil, err
	}
	workloads, err := summarizeWorkloads(ctx, clientset)
	if err != nil {
		return nil, err
	}
	components := summarizeComponents(ctx, clientset)

	score := 100
	if cluster.nodesReady < cluster.nodesTotal {
		score -= 15
	}
	for _, c := range components {
		switch c.state {
		case "⚠️ Degraded":
			score -= 10
		case "❌ Unavailable", "❌ Not Found":
			score -= 20
		}
	}
	if workloads.crashloops > 0 || workloads.failed > 0 {
		score -= 10
	}
	if score < 0 {
		score = 0
	}

	overall := "✅ Healthy"
	switch {
	case score < 40:
		overall = "❌ Critical"
	case score < 80:
		overall = "⚠️ Degraded"
	}

	// Approximate uptime from oldest pod in adhar-system.
	pods, _ := clientset.CoreV1().Pods("adhar-system").List(ctx, metav1.ListOptions{})
	uptime := platformUptimeFromPods(pods.Items)

	return &PlatformStatus{
		OverallStatus:  overall,
		HealthScore:    score,
		PlatformUptime: uptime,
	}, nil
}

func platformUptimeFromPods(pods []corev1.Pod) time.Duration {
	var oldest time.Time
	for _, p := range pods {
		if oldest.IsZero() || p.CreationTimestamp.Time.Before(oldest) {
			oldest = p.CreationTimestamp.Time
		}
	}
	if oldest.IsZero() {
		return 0
	}
	return time.Since(oldest)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
