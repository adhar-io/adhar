// Package e2e provides helpers for end-to-end testing of the Adhar platform
// bootstrap sequence on a local Kind cluster.
package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/k8s"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// RepoRoot is the repository root relative to the e2e test packages. The
	// adhar CLI must run from there: `adhar up` resolves platform/stack
	// relative to the working directory.
	RepoRoot            = "../../.."
	AdharBinaryLocation = "./adhar"
	KubeContext         = "kind-adhar"

	PlatformNamespace = "adhar-system"
	DefaultPort       = "8443"
	DefaultBaseDomain = "adhar.localtest.me"

	// Key platform objects created by the bootstrap sequence.
	ArgoCDServerDeployment = "argo-cd-argocd-server"
	GiteaDeployment        = "gitea"
	GatewayService         = "cilium-gateway-adhar-gateway"
	PlatformAppSet         = "helm-charts-local"

	GiteaCredentialSecret   = "gitea-credential"
	ArgoCDAdminSecret       = "argocd-initial-admin-secret"
	ArgoCDAdminUser         = "admin"
	GatewayHTTPNodePort     = 30080
	GatewayHTTPSNodePort    = 30443
	giteaTokenEndpointTmpl  = "/api/v1/users/%s/tokens"
	giteaRepoSearchEndpoint = "/api/v1/repos/search"
	argoCDSessionEndpoint   = "/api/v1/session"

	pollInterval = 5 * time.Second
)

// GiteaBaseURL and ArgoCDBaseURL are the subdomain-routed service URLs.
func GiteaBaseURL() string {
	return fmt.Sprintf("https://gitea.%s:%s", DefaultBaseDomain, DefaultPort)
}

func ArgoCDBaseURL() string {
	return fmt.Sprintf("https://argocd.%s:%s", DefaultBaseDomain, DefaultPort)
}

// GetHttpClient returns a client that accepts the platform's self-signed cert.
func GetHttpClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

// GetKubeClient returns a controller-runtime client pinned to the Adhar Kind
// cluster's kubeconfig context, with the platform scheme loaded.
func GetKubeClient() (client.Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: KubeContext}
	conf, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig for context %s: %w", KubeContext, err)
	}
	return client.New(conf, client.Options{Scheme: k8s.GetScheme()})
}

// RunCommand executes a command with a timeout, returning combined output.
func RunCommand(ctx context.Context, command string, timeout time.Duration) ([]byte, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmds := strings.Fields(command)
	if len(cmds) == 0 {
		return nil, fmt.Errorf("supply at least one command")
	}

	c := exec.CommandContext(cmdCtx, cmds[0], cmds[1:]...)
	b, err := c.CombinedOutput()
	if err != nil {
		return b, fmt.Errorf("error while running %s: %w, %s", command, err, b)
	}
	return b, nil
}

// RunAdhar runs the adhar CLI from the repository root with the given args.
func RunAdhar(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := exec.CommandContext(cmdCtx, AdharBinaryLocation, args...)
	c.Dir = RepoRoot
	b, err := c.CombinedOutput()
	if err != nil {
		return b, fmt.Errorf("error while running adhar %s: %w, %s", strings.Join(args, " "), err, b)
	}
	return b, nil
}

// GetSecretData reads a secret's data field decoded to strings.
func GetSecretData(ctx context.Context, kubeClient client.Client, namespace, name string) (map[string]string, error) {
	sec := corev1.Secret{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &sec); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(sec.Data))
	for k, v := range sec.Data {
		out[k] = string(v)
	}
	return out, nil
}

// WaitForPlatformReady polls the AdharPlatform resource until its aggregate
// Ready condition is True. It returns the last observed conditions on timeout
// so failures are diagnosable.
func WaitForPlatformReady(ctx context.Context, kubeClient client.Client) error {
	var lastConditions string
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for AdharPlatform Ready condition; last conditions: %s", lastConditions)
		default:
			platforms := &v1alpha1.AdharPlatformList{}
			err := kubeClient.List(ctx, platforms, client.InNamespace(PlatformNamespace))
			if err == nil && len(platforms.Items) > 0 {
				conds := platforms.Items[0].Status.Conditions
				b, _ := json.Marshal(conds)
				lastConditions = string(b)
				if meta.IsStatusConditionTrue(conds, "Ready") {
					return nil
				}
			}
			time.Sleep(pollInterval)
		}
	}
}

// WaitForDeploymentAvailable polls until the deployment has at least one
// available replica.
func WaitForDeploymentAvailable(ctx context.Context, kubeClient client.Client, namespace, name string) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for deployment %s/%s", namespace, name)
		default:
			ready, err := isDeploymentAvailable(ctx, kubeClient, namespace, name)
			if err == nil && ready {
				return nil
			}
			time.Sleep(pollInterval)
		}
	}
}

func isDeploymentAvailable(ctx context.Context, kubeClient client.Client, namespace, name string) (bool, error) {
	dep := appsv1.Deployment{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &dep); err != nil {
		return false, err
	}
	return dep.Status.AvailableReplicas > 0, nil
}

// WaitForAppsHealthy waits until every named ArgoCD Application reports
// Healthy. Sync state is logged by callers; health is the readiness signal.
func WaitForAppsHealthy(ctx context.Context, kubeClient client.Client, names []string) error {
	remaining := map[string]struct{}{}
	for _, n := range names {
		remaining[n] = struct{}{}
	}

	for {
		select {
		case <-ctx.Done():
			keys := make([]string, 0, len(remaining))
			for k := range remaining {
				keys = append(keys, k)
			}
			return fmt.Errorf("timed out waiting for apps to be healthy: %v", keys)
		default:
			for name := range remaining {
				app := argov1alpha1.Application{}
				err := kubeClient.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: name}, &app)
				if err == nil && app.Status.Health.Status == "Healthy" {
					delete(remaining, name)
				}
			}
			if len(remaining) == 0 {
				return nil
			}
			time.Sleep(pollInterval)
		}
	}
}

// GiteaListRepoNames lists repository names visible to the admin user through
// the Gitea API at the platform's external URL.
func GiteaListRepoNames(ctx context.Context, kubeClient client.Client) ([]string, error) {
	creds, err := GetSecretData(ctx, kubeClient, PlatformNamespace, GiteaCredentialSecret)
	if err != nil {
		return nil, fmt.Errorf("reading gitea credentials: %w", err)
	}

	token, err := giteaSessionToken(ctx, creds["username"], creds["password"])
	if err != nil {
		return nil, fmt.Errorf("getting gitea token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GiteaBaseURL()+giteaRepoSearchEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)

	var result struct {
		Ok   bool `json:"ok"`
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := sendAndParse(req, &result); err != nil {
		return nil, fmt.Errorf("searching gitea repos: %w", err)
	}

	names := make([]string, 0, len(result.Data))
	for _, r := range result.Data {
		names = append(names, r.Name)
	}
	return names, nil
}

func giteaSessionToken(ctx context.Context, username, password string) (string, error) {
	endpoint := GiteaBaseURL() + fmt.Sprintf(giteaTokenEndpointTmpl, username)
	body := fmt.Sprintf(`{"name":"e2e-%d", "scopes":["all"]}`, time.Now().UnixNano())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("Content-Type", "application/json")

	var tok struct {
		Token string `json:"sha1"`
	}
	if err := sendAndParse(req, &tok); err != nil {
		return "", err
	}
	if tok.Token == "" {
		return "", fmt.Errorf("received empty gitea token")
	}
	return tok.Token, nil
}

// ArgoCDSessionToken authenticates against the ArgoCD API with the initial
// admin credentials, proving the external route and the API both work.
func ArgoCDSessionToken(ctx context.Context, kubeClient client.Client) (string, error) {
	creds, err := GetSecretData(ctx, kubeClient, PlatformNamespace, ArgoCDAdminSecret)
	if err != nil {
		return "", fmt.Errorf("reading argocd admin secret: %w", err)
	}

	body, _ := json.Marshal(map[string]string{
		"username": ArgoCDAdminUser,
		"password": creds["password"],
	})
	sessionURL := ArgoCDBaseURL() + argoCDSessionEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sessionURL, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	var tok struct {
		Token string `json:"token"`
	}
	if err := sendAndParse(req, &tok); err != nil {
		return "", err
	}
	if tok.Token == "" {
		return "", fmt.Errorf("received empty argocd token")
	}
	return tok.Token, nil
}

func sendAndParse(req *http.Request, target any) error {
	resp, err := GetHttpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, b)
	}
	return json.Unmarshal(b, target)
}
