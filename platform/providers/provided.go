package provider

// Provided mode: the cluster ALREADY EXISTS.
//
// `clusterMode: provided` is the third mode (see managed.go). The platform
// creates nothing — no network, no instances, no managed control plane — and
// installs itself onto a cluster the operator already runs, reached through
// their own kubeconfig. The cluster's lifecycle is NOT ours: nothing here
// creates or deletes it, and every lifecycle call on this provider refuses
// rather than improvising, because the one thing that must never happen is
// `adhar down` deleting someone else's production cluster.
//
// It deliberately replaces the cloud provider for cluster operations only. The
// configured cloud still matters for everything else the platform reads out of
// a provider block — the DNS zone external-dns publishes, the cert-manager
// DNS-01 solver, the Crossplane credentials — so the provider type stays
// whatever the config says and only CreateCluster/DeleteCluster/scaling change
// hands.
//
// Two shapes of existing cluster are supported, and the preflight below is what
// distinguishes them:
//
//   - A cluster already running Cilium. The platform reuses it and must not
//     reapply its own Cilium manifests over the operator's configuration.
//   - A cluster with NO CNI yet (a bare kubeadm cluster, nodes NotReady). The
//     platform installs Cilium exactly as it does in compute mode.
//
// A cluster running a DIFFERENT CNI is refused. The platform's data path is
// Cilium with kubeProxyReplacement and the Gateway is a Cilium Gateway, so
// bringing the platform up means replacing the cluster's networking — which is
// not a thing to do silently to infrastructure the operator did not ask this
// tool to manage.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"adhar-io/adhar/platform/types"
)

const (
	// providedMinKubeMinor is the oldest Kubernetes 1.x minor the platform is
	// built against. Below it the Gateway API v1 types and the CRDs the
	// bootstrap installs are not guaranteed to be servable.
	providedMinKubeMinor = 28

	// defaultStorageClassAnnotation marks the StorageClass a PVC with no
	// explicit class gets. The platform's packages ask for ~90 volumes without
	// naming a class, so a provided cluster without one cannot come up.
	defaultStorageClassAnnotation = "storageclass.kubernetes.io/is-default-class"
)

// foreignCNIDaemonSets maps a DaemonSet name to the CNI it belongs to. Matched
// by exact name in any namespace — these are the upstream defaults, and a
// renamed install simply falls through to "no CNI found", which is the
// permissive outcome rather than the destructive one.
var foreignCNIDaemonSets = map[string]string{
	"calico-node":     "Calico",
	"canal":           "Canal",
	"kube-flannel-ds": "Flannel",
	"weave-net":       "Weave Net",
	"aws-node":        "the Amazon VPC CNI",
	"azure-cni":       "the Azure CNI",
	"kindnet":         "kindnet (Kind's default CNI)",
	"kube-ovn-cni":    "Kube-OVN",
	"antrea-agent":    "Antrea",
	"cilium":          "", // ours, handled separately
}

// Compile-time proof that a provided cluster is usable everywhere a cloud one
// is, and that its checks replace the cloud preflight.
var (
	_ Provider    = (*ProvidedProvider)(nil)
	_ Preflighter = (*ProvidedProvider)(nil)
)

// ProvidedProvider adopts an existing cluster through a kubeconfig.
type ProvidedProvider struct {
	// providerName is the cloud this config block names, kept for reporting and
	// so the rest of the bootstrap (DNS, Crossplane credentials) is unchanged.
	providerName string
	region       string

	kubeconfigPath string
	kubeContext    string

	// kubeconfig is the single-context kubeconfig handed to the bootstrap.
	kubeconfig string
	restConfig *rest.Config

	// adopted is set by CreateCluster, which adopts rather than creates.
	adopted *types.Cluster

	// client is the Kubernetes client for the provided cluster. An interface,
	// and settable, so the checks below can be exercised against a fake cluster:
	// every decision in this file is about what is ALREADY RUNNING somewhere,
	// which is otherwise only testable by having a cluster of each shape.
	client kubernetes.Interface
}

// NewProvidedProvider builds the provider for `clusterMode: provided` from the
// same provider map every other provider is constructed from.
func NewProvidedProvider(providerName, region string, config map[string]interface{}) (*ProvidedProvider, error) {
	path, err := resolveProvidedKubeconfigPath(config)
	if err != nil {
		return nil, err
	}
	return &ProvidedProvider{
		providerName:   strings.ToLower(strings.TrimSpace(providerName)),
		region:         region,
		kubeconfigPath: path,
		kubeContext:    providedConfigString(config, "kubeContext", "kube_context", "context"),
	}, nil
}

// providedConfigString reads a string from the provider map, looking at the top
// level and inside a nested `config:` section — provider blocks differ in where
// they put their settings.
func providedConfigString(config map[string]interface{}, keys ...string) string {
	sections := []map[string]interface{}{config}
	if nested, ok := config["config"].(map[string]interface{}); ok {
		sections = append(sections, nested)
	}
	for _, section := range sections {
		for _, key := range keys {
			if v, ok := section[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// resolveProvidedKubeconfigPath finds the kubeconfig for the existing cluster:
// the config's own `kubeconfig`, else $KUBECONFIG (first entry), else
// ~/.kube/config. `~` and environment variables are expanded, because a path in
// a config file is written the way it is typed in a shell.
func resolveProvidedKubeconfigPath(config map[string]interface{}) (string, error) {
	raw := providedConfigString(config, "kubeconfig", "kubeConfig", "kubeconfigPath", "kubeconfig_path")
	source := "the provider's kubeconfig setting"
	if raw == "" {
		if env := strings.TrimSpace(os.Getenv("KUBECONFIG")); env != "" {
			raw = strings.Split(env, string(os.PathListSeparator))[0]
			source = "$KUBECONFIG"
		}
	}
	if raw == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("clusterMode: provided needs a kubeconfig and none was given: %w", err)
		}
		raw = filepath.Join(home, ".kube", "config")
		source = "the default kubeconfig"
	}

	expanded := os.ExpandEnv(raw)
	if expanded == "~" || strings.HasPrefix(expanded, "~"+string(os.PathSeparator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding %q: %w", raw, err)
		}
		expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~"))
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolving kubeconfig path %q: %w", raw, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("kubeconfig %s (%s) is not readable: %w — "+
			"clusterMode: provided installs onto an existing cluster, so set `kubeconfig:` "+
			"in the provider block to a file that reaches it", abs, source, err)
	}
	return abs, nil
}

// Name returns the configured provider name with the mode appended, so every
// line that prints the provider says which cluster lifecycle is in play.
func (p *ProvidedProvider) Name() string {
	if p.providerName == "" {
		return ClusterModeProvided
	}
	return p.providerName + " (" + ClusterModeProvided + ")"
}

// Region returns the configured region. It is informational in this mode: no
// resource is created, so nothing is placed in it.
func (p *ProvidedProvider) Region() string { return p.region }

// Authenticate loads the kubeconfig and proves the API server answers. There
// are no cloud credentials to check in this mode — reaching the cluster IS the
// authentication.
func (p *ProvidedProvider) Authenticate(ctx context.Context, _ *types.Credentials) error {
	if err := p.selectContext(); err != nil {
		return err
	}
	clientset, err := p.clientset()
	if err != nil {
		return fmt.Errorf("building a client for context %q: %w", p.kubeContext, err)
	}
	if _, err := clientset.Discovery().ServerVersion(); err != nil {
		return fmt.Errorf("the cluster in context %q did not answer: %w — "+
			"check the kubeconfig reaches it (`kubectl --context %s get nodes`)", p.kubeContext, err, p.kubeContext)
	}
	return nil
}

// selectContext reduces the kubeconfig to the ONE context the platform will
// install onto and builds a client config for it. Separated from the reachability
// probe above because choosing the wrong context is the failure that matters and
// it is decided entirely from a file.
func (p *ProvidedProvider) selectContext() error {
	raw, err := clientcmd.LoadFromFile(p.kubeconfigPath)
	if err != nil {
		return fmt.Errorf("reading kubeconfig %s: %w", p.kubeconfigPath, err)
	}

	ctxName := p.kubeContext
	if ctxName == "" {
		ctxName = raw.CurrentContext
	}
	if ctxName == "" {
		return fmt.Errorf("kubeconfig %s has no current context and no `kubeContext:` was set: "+
			"name the context of the cluster to install onto", p.kubeconfigPath)
	}
	if _, ok := raw.Contexts[ctxName]; !ok {
		return fmt.Errorf("kubeconfig %s has no context %q (contexts: %s)",
			p.kubeconfigPath, ctxName, strings.Join(contextNames(raw), ", "))
	}

	// Minify to the one context before anything else sees it. The bootstrap
	// writes this file out and exports it as KUBECONFIG, and `adhar up` also
	// merges it into the user's default kubeconfig — carrying every other
	// cluster the operator has along for the ride, with whichever one happens
	// to be current, is both noisy and a way to install onto the wrong cluster.
	minified := raw.DeepCopy()
	minified.CurrentContext = ctxName
	if err := clientcmdapi.MinifyConfig(minified); err != nil {
		return fmt.Errorf("reducing kubeconfig %s to context %q: %w", p.kubeconfigPath, ctxName, err)
	}
	out, err := clientcmd.Write(*minified)
	if err != nil {
		return fmt.Errorf("serialising kubeconfig for context %q: %w", ctxName, err)
	}
	p.kubeconfig = string(out)
	p.kubeContext = ctxName

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(out)
	if err != nil {
		return fmt.Errorf("building a client for context %q: %w", ctxName, err)
	}
	p.restConfig = restConfig
	return nil
}

func contextNames(cfg *clientcmdapi.Config) []string {
	names := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		names = append(names, name)
	}
	return names
}

// ValidatePermissions checks the credentials in the kubeconfig can read the
// cluster at all. The real check is Preflight below.
func (p *ProvidedProvider) ValidatePermissions(ctx context.Context) error {
	clientset, err := p.clientset()
	if err != nil {
		return err
	}
	if _, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		return fmt.Errorf("listing namespaces in context %q: %w — the platform install needs cluster-admin "+
			"on the provided cluster", p.kubeContext, err)
	}
	return nil
}

func (p *ProvidedProvider) clientset() (kubernetes.Interface, error) {
	if p.client != nil {
		return p.client, nil
	}
	if p.restConfig == nil {
		return nil, fmt.Errorf("the provided cluster has not been contacted yet (Authenticate first)")
	}
	clientset, err := kubernetes.NewForConfig(p.restConfig)
	if err != nil {
		return nil, err
	}
	p.client = clientset
	return p.client, nil
}

// Preflight examines the EXISTING cluster instead of a cloud account: this is
// the one mode where the thing being checked is already running, so the checks
// are about whether the platform can install onto it without breaking it.
func (p *ProvidedProvider) Preflight(ctx context.Context, _ *types.ClusterSpec) []Check {
	checks := []Check{}
	clientset, err := p.clientset()
	if err != nil {
		return []Check{{
			Name:   "provided cluster reachable",
			Status: CheckFail,
			Detail: err.Error(),
			Fix:    "set `kubeconfig:` (and `kubeContext:`) in the provider block to a file that reaches the cluster",
		}}
	}

	// Version. The bootstrap installs Gateway API v1 and its own CRDs; an old
	// control plane cannot serve them, and finding that out from a CRD apply
	// failure half-way through the install is a poor way to learn it.
	version, err := clientset.Discovery().ServerVersion()
	switch {
	case err != nil:
		checks = append(checks, Check{
			Name: "Kubernetes version", Status: CheckFail,
			Detail: fmt.Sprintf("could not read the server version: %v", err),
			Fix:    "check the kubeconfig context reaches the cluster",
		})
	default:
		minor, convErr := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(version.Minor), "+"))
		switch {
		case convErr != nil:
			checks = append(checks, Check{
				Name: "Kubernetes version", Status: CheckWarn,
				Detail: fmt.Sprintf("server reports %s, which could not be compared with the minimum (1.%d)",
					version.GitVersion, providedMinKubeMinor),
			})
		case version.Major == "1" && minor < providedMinKubeMinor:
			checks = append(checks, Check{
				Name: "Kubernetes version", Status: CheckFail,
				Detail: fmt.Sprintf("the cluster runs %s; the platform is built against 1.%d and newer",
					version.GitVersion, providedMinKubeMinor),
				Fix: fmt.Sprintf("upgrade the provided cluster to 1.%d or later", providedMinKubeMinor),
			})
		default:
			checks = append(checks, Check{
				Name: "Kubernetes version", Status: CheckPass,
				Detail: fmt.Sprintf("the cluster runs %s", version.GitVersion),
			})
		}
	}

	checks = append(checks, p.cniChecks(ctx, clientset)...)

	// Nodes. One schedulable node is the floor; the platform's enabled packages
	// want several, but how many is the operator's business on a cluster they
	// already run.
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	switch {
	case err != nil:
		checks = append(checks, Check{
			Name: "nodes", Status: CheckFail,
			Detail: fmt.Sprintf("could not list nodes: %v", err),
			Fix:    "the platform install needs cluster-admin on the provided cluster",
		})
	case len(nodes.Items) == 0:
		checks = append(checks, Check{
			Name: "nodes", Status: CheckFail,
			Detail: "the cluster has no nodes",
			Fix:    "join at least one node before installing the platform",
		})
	default:
		checks = append(checks, Check{
			Name: "nodes", Status: CheckPass,
			Detail: fmt.Sprintf("%d node(s) registered", len(nodes.Items)),
		})
	}

	// Default StorageClass. In compute mode the platform installs
	// local-path-provisioner and makes it the default; on a cluster it does not
	// own it changes no storage defaults, so the cluster has to have one — ~90
	// of the platform's PersistentVolumeClaims name no class at all.
	classes, err := clientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	switch {
	case err != nil:
		checks = append(checks, Check{
			Name: "default StorageClass", Status: CheckWarn,
			Detail: fmt.Sprintf("could not list StorageClasses: %v", err),
		})
	default:
		defaultClass := ""
		for i := range classes.Items {
			if classes.Items[i].Annotations[defaultStorageClassAnnotation] == "true" {
				defaultClass = classes.Items[i].Name
				break
			}
		}
		if defaultClass == "" {
			checks = append(checks, Check{
				Name: "default StorageClass", Status: CheckWarn,
				Detail: "the cluster has no default StorageClass; the platform's packages request ~90 volumes without naming one, " +
					"and in provided mode nothing installs a provisioner or changes the cluster's storage defaults",
				Fix: "mark one StorageClass default: kubectl annotate sc <name> " +
					defaultStorageClassAnnotation + "=true",
			})
		} else {
			checks = append(checks, Check{
				Name: "default StorageClass", Status: CheckPass,
				Detail: "default class " + defaultClass,
			})
		}
	}

	return checks
}

// cniChecks decides which of the two supported shapes the provided cluster is,
// and refuses the third.
func (p *ProvidedProvider) cniChecks(ctx context.Context, clientset kubernetes.Interface) []Check {
	sets, err := clientset.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return []Check{{
			Name: "container network", Status: CheckFail,
			Detail: fmt.Sprintf("could not list DaemonSets to identify the cluster's CNI: %v", err),
			Fix:    "the platform install needs cluster-admin on the provided cluster",
		}}
	}

	var ciliumDS *appsv1.DaemonSet
	foreign := map[string]string{} // CNI name -> namespace/name
	for i := range sets.Items {
		ds := &sets.Items[i]
		cni, known := foreignCNIDaemonSets[ds.Name]
		if !known {
			continue
		}
		if ds.Name == "cilium" {
			ciliumDS = ds
			continue
		}
		foreign[cni] = ds.Namespace + "/" + ds.Name
	}

	if len(foreign) > 0 {
		names := make([]string, 0, len(foreign))
		for cni, where := range foreign {
			names = append(names, fmt.Sprintf("%s (%s)", cni, where))
		}
		return []Check{{
			Name: "container network", Status: CheckFail,
			Detail: fmt.Sprintf("the cluster already runs %s; the platform's data path is Cilium with "+
				"kubeProxyReplacement and its Gateway is a Cilium Gateway, so installing would mean replacing "+
				"the networking of a cluster it does not own", strings.Join(names, ", ")),
			Fix: "provide a cluster running Cilium (with gatewayAPI.enabled=true), or one with no CNI yet — " +
				"the platform installs Cilium itself on a bare cluster. Use clusterMode: compute or managed to " +
				"have the platform build the cluster instead.",
		}}
	}

	if ciliumDS == nil {
		return []Check{{
			Name: "container network", Status: CheckPass,
			Detail: "no CNI installed; the platform will install Cilium (nodes stay NotReady until it is up)",
		}}
	}

	checks := []Check{{
		Name: "container network", Status: CheckPass,
		Detail: fmt.Sprintf("Cilium already runs as %s/%s and is left exactly as configured — "+
			"the platform does not reapply its own Cilium manifests over it", ciliumDS.Namespace, ciliumDS.Name),
	}}

	// Gateway API has to be enabled in THEIR Cilium: the platform's Gateway is a
	// Cilium Gateway, and without it no HTTPRoute resolves, which means not one
	// platform URL answers. Cheap to fix, fatal to miss.
	cm, err := clientset.CoreV1().ConfigMaps(ciliumDS.Namespace).Get(ctx, "cilium-config", metav1.GetOptions{})
	switch {
	case err != nil:
		checks = append(checks, Check{
			Name: "Cilium Gateway API", Status: CheckWarn,
			Detail: fmt.Sprintf("could not read %s/cilium-config to confirm Gateway API is enabled: %v",
				ciliumDS.Namespace, err),
			Fix: "confirm the cluster's Cilium runs with gatewayAPI.enabled=true",
		})
	case cm.Data["enable-gateway-api"] != "true":
		checks = append(checks, Check{
			Name: "Cilium Gateway API", Status: CheckFail,
			Detail: "the cluster's Cilium has Gateway API support disabled (enable-gateway-api is not \"true\"), " +
				"so the platform Gateway would never be Programmed and no platform URL would answer",
			Fix: "helm upgrade cilium --reuse-values --set gatewayAPI.enabled=true (and install the Gateway API CRDs), " +
				"then re-run",
		})
	default:
		checks = append(checks, Check{
			Name: "Cilium Gateway API", Status: CheckPass,
			Detail: "the cluster's Cilium serves Gateway API",
		})
	}
	return checks
}

// CreateCluster ADOPTS the existing cluster. It creates nothing: the name and
// the spec describe the platform that will be installed, not infrastructure to
// build. Idempotent, because `adhar up` is the command people re-run.
func (p *ProvidedProvider) CreateCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	if p.restConfig == nil && p.client == nil {
		if err := p.Authenticate(ctx, nil); err != nil {
			return nil, err
		}
	}
	clientset, err := p.clientset()
	if err != nil {
		return nil, err
	}
	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("reading the version of the provided cluster: %w", err)
	}

	name := ""
	if spec != nil {
		name = spec.Name
	}
	if name == "" {
		name = p.kubeContext
	}

	endpoint := ""
	if p.restConfig != nil {
		endpoint = p.restConfig.Host
	}

	cluster := &types.Cluster{
		ID:        name,
		Name:      name,
		Provider:  p.providerName,
		Region:    p.region,
		Version:   version.GitVersion,
		Status:    types.ClusterStatusRunning,
		Endpoint:  endpoint,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			// Read back by operations that must not touch a cluster they did not
			// build: `adhar down`, the node autoscaler, `adhar cluster scale`.
			"mode":        ClusterModeProvided,
			"kubeconfig":  p.kubeconfigPath,
			"kubeContext": p.kubeContext,
		},
	}
	p.adopted = cluster
	return cluster, nil
}

// DeleteCluster REFUSES. This is the whole point of the mode: the platform did
// not create this cluster, so it does not get to delete it. `adhar down` checks
// the mode before it ever reaches here; this is the backstop for anything that
// does not.
func (p *ProvidedProvider) DeleteCluster(_ context.Context, clusterID string) error {
	return fmt.Errorf("refusing to delete %q: it is a provided cluster (clusterMode: %s), "+
		"so the platform never created it and will not destroy it — "+
		"remove the platform instead (kubectl delete adharplatform --all -n adhar-system), "+
		"or delete the cluster with the tool that created it",
		clusterID, ClusterModeProvided)
}

// UpdateCluster refuses: the cluster's shape belongs to whoever built it.
func (p *ProvidedProvider) UpdateCluster(_ context.Context, clusterID string, _ *types.ClusterSpec) error {
	return providedNotOurs(fmt.Sprintf("change the shape of cluster %q", clusterID))
}

// GetCluster returns the adopted cluster.
func (p *ProvidedProvider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	if p.adopted != nil && (clusterID == "" || clusterID == p.adopted.ID || clusterID == p.adopted.Name) {
		return p.adopted, nil
	}
	spec := &types.ClusterSpec{}
	spec.Name = clusterID
	return p.CreateCluster(ctx, spec)
}

// ListClusters returns the adopted cluster, and nothing before one is adopted.
//
// Nothing is INVENTED here on purpose. `adhar down` finds a cluster to delete by
// listing every configured provider and matching the name, so a provider that
// claimed to hold a cluster named whatever it was asked about would hand the
// teardown a live cluster to delete.
func (p *ProvidedProvider) ListClusters(_ context.Context) ([]*types.Cluster, error) {
	if p.adopted == nil {
		return []*types.Cluster{}, nil
	}
	return []*types.Cluster{p.adopted}, nil
}

// GetKubeconfig returns the single-context kubeconfig for the provided cluster.
func (p *ProvidedProvider) GetKubeconfig(ctx context.Context, _ string) (string, error) {
	if p.kubeconfig == "" {
		if err := p.Authenticate(ctx, nil); err != nil {
			return "", err
		}
	}
	return p.kubeconfig, nil
}

// providedNotOurs is the refusal every node- and infrastructure-lifecycle call
// returns. It names the mode, because the operator asked for exactly this.
func providedNotOurs(what string) error {
	return fmt.Errorf("cannot %s: the cluster was provided (clusterMode: %s), so the platform "+
		"manages the software on it and never its infrastructure", what, ClusterModeProvided)
}

// Node management: the nodes belong to whoever built the cluster. In particular
// the node autoscaler must not act — it adds capacity by creating cloud
// instances and kubeadm-joining them, which on a provided cluster would both
// bill the wrong account and build nodes the cluster's own tooling knows nothing
// about.
func (p *ProvidedProvider) AddNodeGroup(_ context.Context, _ string, _ *types.NodeGroupSpec) (*types.NodeGroup, error) {
	return nil, providedNotOurs("add a node group")
}

func (p *ProvidedProvider) RemoveNodeGroup(_ context.Context, _ string, name string) error {
	return providedNotOurs("remove node group " + name)
}

func (p *ProvidedProvider) ScaleNodeGroup(_ context.Context, _ string, name string, replicas int) error {
	return providedNotOurs(fmt.Sprintf("scale node group %s to %d", name, replicas))
}

func (p *ProvidedProvider) GetNodeGroup(_ context.Context, _ string, name string) (*types.NodeGroup, error) {
	return nil, providedNotOurs("read node group " + name)
}

// ListNodeGroups reports no groups rather than an error: a provided cluster has
// no node groups the platform knows about, and callers that merely enumerate
// them (status displays) should show nothing, not fail.
func (p *ProvidedProvider) ListNodeGroups(_ context.Context, _ string) ([]*types.NodeGroup, error) {
	return []*types.NodeGroup{}, nil
}

// Infrastructure: none of it is ours.
func (p *ProvidedProvider) CreateVPC(_ context.Context, _ *types.VPCSpec) (*types.VPC, error) {
	return nil, providedNotOurs("create a network")
}
func (p *ProvidedProvider) DeleteVPC(_ context.Context, id string) error {
	return providedNotOurs("delete network " + id)
}
func (p *ProvidedProvider) GetVPC(_ context.Context, id string) (*types.VPC, error) {
	return nil, providedNotOurs("read network " + id)
}
func (p *ProvidedProvider) CreateLoadBalancer(_ context.Context, _ *types.LoadBalancerSpec) (*types.LoadBalancer, error) {
	return nil, providedNotOurs("create a load balancer")
}
func (p *ProvidedProvider) DeleteLoadBalancer(_ context.Context, id string) error {
	return providedNotOurs("delete load balancer " + id)
}
func (p *ProvidedProvider) GetLoadBalancer(_ context.Context, id string) (*types.LoadBalancer, error) {
	return nil, providedNotOurs("read load balancer " + id)
}
func (p *ProvidedProvider) CreateStorage(_ context.Context, _ *types.StorageSpec) (*types.Storage, error) {
	return nil, providedNotOurs("create a volume")
}
func (p *ProvidedProvider) DeleteStorage(_ context.Context, id string) error {
	return providedNotOurs("delete volume " + id)
}
func (p *ProvidedProvider) GetStorage(_ context.Context, id string) (*types.Storage, error) {
	return nil, providedNotOurs("read volume " + id)
}

// Lifecycle: upgrading or restoring a cluster the platform did not build is the
// operator's own tooling's job.
func (p *ProvidedProvider) UpgradeCluster(_ context.Context, clusterID, version string) error {
	return providedNotOurs(fmt.Sprintf("upgrade cluster %s to %s", clusterID, version))
}
func (p *ProvidedProvider) BackupCluster(_ context.Context, clusterID string) (*types.Backup, error) {
	return nil, providedNotOurs("back up cluster " + clusterID)
}
func (p *ProvidedProvider) RestoreCluster(_ context.Context, backupID, target string) error {
	return providedNotOurs(fmt.Sprintf("restore %s into %s", backupID, target))
}

// GetClusterHealth answers from the cluster itself: nodes and their Ready
// condition are visible through the kubeconfig, with no cloud API involved.
func (p *ProvidedProvider) GetClusterHealth(ctx context.Context, clusterID string) (*types.HealthStatus, error) {
	clientset, err := p.clientset()
	if err != nil {
		return nil, err
	}
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes of provided cluster %s: %w", clusterID, err)
	}
	ready := 0
	for i := range nodes.Items {
		for _, cond := range nodes.Items[i].Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				ready++
				break
			}
		}
	}
	status := "healthy"
	if ready < len(nodes.Items) || ready == 0 {
		status = "degraded"
	}
	return &types.HealthStatus{
		Status: status,
		Components: map[string]types.ComponentHealth{
			"nodes": {
				Status:  status,
				Message: fmt.Sprintf("%d of %d nodes Ready", ready, len(nodes.Items)),
			},
		},
		LastCheck: time.Now(),
	}, nil
}

// GetClusterMetrics has no cloud API to ask. Rather than return zeros that look
// like measurements, it says so.
func (p *ProvidedProvider) GetClusterMetrics(_ context.Context, clusterID string) (*types.Metrics, error) {
	return nil, providedNotOurs("report cloud metrics for cluster " + clusterID)
}

// Addons are the cluster owner's, not the platform's.
func (p *ProvidedProvider) InstallAddon(_ context.Context, _ string, name string, _ map[string]interface{}) error {
	return providedNotOurs("install the cloud addon " + name)
}
func (p *ProvidedProvider) UninstallAddon(_ context.Context, _ string, name string) error {
	return providedNotOurs("uninstall the cloud addon " + name)
}
func (p *ProvidedProvider) ListAddons(_ context.Context, _ string) ([]string, error) {
	return []string{}, nil
}

// Cost: the platform has no bill for a cluster it did not create, and reporting
// 0.0 would read as "this is free".
func (p *ProvidedProvider) GetClusterCost(_ context.Context, clusterID string) (float64, error) {
	return 0, providedNotOurs("price cluster " + clusterID)
}
func (p *ProvidedProvider) GetCostBreakdown(_ context.Context, clusterID string) (map[string]float64, error) {
	return nil, providedNotOurs("price cluster " + clusterID)
}

// InvestigateCluster prints what can be seen through the kubeconfig.
func (p *ProvidedProvider) InvestigateCluster(ctx context.Context, clusterID string) error {
	health, err := p.GetClusterHealth(ctx, clusterID)
	if err != nil {
		return err
	}
	fmt.Printf("provided cluster %s (context %s, %s): %s — %s\n",
		clusterID, p.kubeContext, p.kubeconfigPath, health.Status, health.Components["nodes"].Message)
	return nil
}
