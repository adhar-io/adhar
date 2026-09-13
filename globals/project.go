package globals

const (
	ProjectName string = "adhar"

	// Default cluster name for Kind clusters
	DefaultClusterName string = "adhar"

	// DefaultKubernetesVersion is the version Adhar provisions
	DefaultKubernetesVersion string = "v1.37.0"

	// Cloud Providers
	CloudProviderGKE   string = "gke"
	CloudProviderAWS   string = "aws"
	CloudProviderDO    string = "do"
	CloudProviderAzure string = "azure"
	CloudProviderCivo  string = "civo"
	CloudProviderKind  string = "kind"

	// Git Providers
	GitProviderGitea     string = "gitea"
	GitProviderGitlab    string = "gitlab"
	GitProviderGithub    string = "github"
	GitProviderBitbucket string = "bitbucket"

	// Default namespaces for platform components
	AdharSystemNamespace string = "adhar-system"

	// Default secret names
	SelfSignedCertSecretName = "adhar-cert"
	SelfSignedCertCMName     = "adhar-cert"
	SelfSignedCertCMKeyName  = "ca.crt"
	DefaultSANWildcard       = "*.adhar.localtest.me"
	DefaultHostName          = "adhar.localtest.me"

	// KeycloakRealm is the platform realm every Adhar service authenticates
	// against, and KeycloakCLIClientID is its public, direct-grant OIDC client.
	// Both are named in three places that MUST agree — `adhar auth` (the CLI
	// session), the kube-apiserver's --oidc-* flags when `oidcAuth: "true"` is
	// set on an environment, and the realm the keycloak package provisions — so
	// they live here rather than being re-spelled per package.
	KeycloakRealm       = "adhar"
	KeycloakCLIClientID = "adhar-cli"

	// KubernetesOIDCAudience is what kube-apiserver's --oidc-client-id must be,
	// and it is NOT KeycloakCLIClientID. A token minted by `adhar auth` carries
	// `azp: adhar-cli` (the client that asked for it) but `aud: [kubernetes,
	// account]` — and the API server matches the AUDIENCE. Setting
	// --oidc-client-id=adhar-cli makes every token fail with a bare
	// "Unauthorized", because the audience does not contain it. The Kind config
	// (platform/providers/kind/resources/kind.yaml.tmpl) has always used
	// "kubernetes"; the comment in security/keycloak/manifests/k8s-rbac.yaml said
	// "adhar-cli" and was simply wrong.
	KubernetesOIDCAudience = "kubernetes"

	// ArgoCDResourcesFinalizer is the ONLY finalizer name ArgoCD honours for
	// cascade deletion. The `argocd.` segment is not optional: the API server
	// accepts any finalizer string, so the shortened
	// `resources-finalizer.argoproj.io` is stored and then never removed by
	// anything — the Application hangs in Terminating and the resources it owns
	// are never pruned. Four ApplicationSets and this controller all carried the
	// short form, which is why closing a preview PR left its Application and
	// namespace behind.
	ArgoCDResourcesFinalizer = "resources-finalizer.argocd.argoproj.io"

	// GiteaPlatformOrg is the Gitea organization owning the platform GitOps
	// repos (packages, environments). Keycloak groups map onto its teams via
	// the auth source's --group-team-map (gitea-oauth-config.yaml):
	// platform-admin -> Owners,
	// platform-developer -> developers (read),
	// platform-viewer -> viewers (read). Membership syncs on every SSO login.
	GiteaPlatformOrg = "adhar"

	// Gitea bootstrap admin credentials. Day-0 values; rotated in-cluster by
	// the credential-rotation package. Also present in the embedded gitea
	// manifest and platform/stack/argocd-auth.yaml — keep in sync.
	GiteaAdminUser     = "gitea_admin"
	GiteaAdminPassword = "r8sA8CPHD9!bt6d"

	// Platform GitOps repository names created under GiteaPlatformOrg.
	GitOpsRepoPackages     = "packages"
	GitOpsRepoEnvironments = "environments"
	// ClusterSpecConfigMapName holds the provider facts the in-cluster
	// controllers need to talk back to the cloud the cluster was created on
	// (provider, region, cluster name, node group, instance size, Kubernetes
	// version, plus the sanitized provider config as JSON). The CLI writes it
	// at bootstrap because nothing else in the cluster knows how it was made;
	// the node autoscaler reads it to reconstruct the same provider client
	// `adhar cluster scale` uses. It carries no credentials.
	ClusterSpecConfigMapName = "adhar-cluster-spec"

	// ClusterSSHSecretName holds the cluster's kubeadm SSH private key (key
	// "id_ed25519"), mirrored from ~/.adhar/clusters/<name>/ at bootstrap.
	// Joining a new worker means running kubeadm on the control plane over
	// SSH, so an in-cluster autoscaler cannot work without it. Only created
	// for self-managed (kubeadm) clusters.
	ClusterSSHSecretName = "adhar-cluster-ssh"
	// ClusterSSHSecretKey is the key inside ClusterSSHSecretName.
	ClusterSSHSecretKey = "id_ed25519"

	// GitOpsRepoTemplates holds the service/application templates the Console and
	// the `adhar apps deploy --template` CLI both instantiate through the
	// CompositeApplication control-plane layer — the single source of truth for
	// templates, served from Gitea (not the local filesystem or a hardcoded set).
	GitOpsRepoTemplates = "templates"
)

var (
	Version   string = "0.0.1"   // Default version, set at build time
	GitCommit string = "unknown" // Default git commit, set at build time
	BuildDate string = "unknown" // Default build date, set at build time
)
