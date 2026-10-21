package provider

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Trusting the platform's Keycloak on a self-managed (kubeadm) control plane.
//
// Headlamp signs a person in with Keycloak and then calls the Kubernetes API
// WITH THAT PERSON'S TOKEN, so RBAC applies to them and not to a service
// account. That only works if the kube-apiserver trusts Keycloak as a token
// issuer. Kind clusters get the --oidc-* flags at creation (kind.yaml.tmpl);
// cloud clusters never did, because setting them during `kubeadm init` points
// the API server at an issuer that does not exist yet and once kept it from
// starting at all (provider.go, the oidcAuth rejection). The result on GCP was
// a Headlamp login that "worked" at Keycloak and then failed every API call
// with 401 (2026-10-21).
//
// So it is a day-2 step, taken once Keycloak is serving, and it uses the
// STRUCTURED authentication configuration rather than the flags:
//
//   - one file, /etc/kubernetes/authn/adhar.yaml, that the API server watches
//     and reloads on change — later edits need no restart;
//   - several audiences (`kubernetes` for `adhar auth` tokens, `headlamp` for
//     the token Headlamp forwards), which the single --oidc-client-id flag
//     cannot express;
//   - the same claim mappings and `oidc:` prefixes the keycloak package's
//     RBAC bindings expect (k8s-rbac.yaml), and Kind uses.
//
// The remote script refuses to change anything it cannot verify first: the
// issuer must answer from the control-plane node itself, the node must be a
// kubeadm control plane, and the legacy --oidc-* flags (which conflict) must be
// absent. Adding the flag restarts the API server once; if it is not healthy
// again within three minutes the original manifest is restored.

// APIServerOIDC describes the issuer the API server should trust.
type APIServerOIDC struct {
	IssuerURL string
	// Audiences the API server accepts; any one matching is enough.
	Audiences []string
}

// DefaultAPIServerOIDCAudiences are the clients whose tokens reach the API
// server: the CLI's `kubernetes` client and Headlamp.
var DefaultAPIServerOIDCAudiences = []string{"kubernetes", "headlamp"}

const (
	apiServerAuthnDir      = "/etc/kubernetes/authn"
	apiServerAuthnFile     = apiServerAuthnDir + "/adhar.yaml"
	apiServerManifest      = "/etc/kubernetes/manifests/kube-apiserver.yaml"
	apiServerManifestBkDir = "/etc/kubernetes/adhar-backup"
)

// AuthenticationConfigYAML renders the AuthenticationConfiguration file.
func AuthenticationConfigYAML(cfg APIServerOIDC) (string, error) {
	u, err := url.Parse(cfg.IssuerURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("issuer %q must be an https URL", cfg.IssuerURL)
	}
	auds := append([]string(nil), cfg.Audiences...)
	if len(auds) == 0 {
		auds = append(auds, DefaultAPIServerOIDCAudiences...)
	}
	sort.Strings(auds)
	var b strings.Builder
	b.WriteString("# Managed by adhar (platform/providers/apiserver_oidc.go). The API server\n")
	b.WriteString("# reloads this file when it changes.\n")
	b.WriteString("apiVersion: apiserver.config.k8s.io/v1\n")
	b.WriteString("kind: AuthenticationConfiguration\n")
	b.WriteString("jwt:\n")
	b.WriteString("  - issuer:\n")
	fmt.Fprintf(&b, "      url: %q\n", cfg.IssuerURL)
	b.WriteString("      audiences:\n")
	for _, a := range auds {
		fmt.Fprintf(&b, "        - %q\n", a)
	}
	b.WriteString("      audienceMatchPolicy: MatchAny\n")
	b.WriteString("    claimMappings:\n")
	b.WriteString("      username:\n")
	b.WriteString("        claim: preferred_username\n")
	b.WriteString("        prefix: \"oidc:\"\n")
	b.WriteString("      groups:\n")
	b.WriteString("        claim: groups\n")
	b.WriteString("        prefix: \"oidc:\"\n")
	return b.String(), nil
}

// APIServerOIDCScript is the shell script run as root on a control-plane node.
// It prints one line starting with ADHAR_OIDC: unchanged, configured, or an
// error the caller reports.
func APIServerOIDCScript(cfg APIServerOIDC) (string, error) {
	conf, err := AuthenticationConfigYAML(cfg)
	if err != nil {
		return "", err
	}
	discovery := strings.TrimSuffix(cfg.IssuerURL, "/") + "/.well-known/openid-configuration"
	return fmt.Sprintf(`set -u
M=%[1]s
[ -f "$M" ] || { echo "ADHAR_OIDC: skipped: $M not found (not a kubeadm control plane)"; exit 0; }
if grep -q -- '--oidc-issuer-url' "$M"; then
  echo "ADHAR_OIDC: skipped: the --oidc-* flags are already set and conflict with --authentication-config"; exit 0
fi
# The API server fetches the issuer's keys from THIS node, with the system
# trust store: prove both before pointing it there.
if ! curl -fsS --max-time 15 -o /dev/null %[2]q; then
  echo "ADHAR_OIDC: error: %[2]s does not answer from the control-plane node with a trusted certificate; nothing changed"; exit 1
fi
mkdir -p %[3]s
umask 022
cat > %[4]s.tmp <<'ADHAR_AUTHN_EOF'
%[5]sADHAR_AUTHN_EOF
mv %[4]s.tmp %[4]s
if grep -q -- '--authentication-config=' "$M"; then
  echo "ADHAR_OIDC: unchanged: the API server already reads %[4]s (it reloads the file)"; exit 0
fi
mkdir -p %[6]s
B=%[6]s/kube-apiserver.yaml.$(date +%%s)
cp "$M" "$B"
# Edit a copy, then move it into place in one step: the kubelet must never
# see a half-written static pod manifest.
T=/tmp/adhar-kube-apiserver.yaml
awk -v cfg=%[4]s -v dir=%[3]s '
  { print }
  /^    - kube-apiserver$/ && !f { print "    - --authentication-config=" cfg; f=1 }
  /^    volumeMounts:$/ && !m { print "    - mountPath: " dir; print "      name: adhar-authn"; print "      readOnly: true"; m=1 }
  /^  volumes:$/ && !v { print "  - hostPath:"; print "      path: " dir; print "      type: DirectoryOrCreate"; print "    name: adhar-authn"; v=1 }
  END { if (!(f && m && v)) exit 3 }' "$B" > "$T" || { echo "ADHAR_OIDC: error: unexpected kube-apiserver manifest layout; nothing changed"; exit 1; }
mv "$T" "$M"
# Wait for the NEW API server: the flag in its command line and /livez ok.
for i in $(seq 1 36); do
  sleep 5
  if pgrep -af kube-apiserver | grep -q -- '--authentication-config=' \
     && curl -fsk --max-time 5 https://127.0.0.1:6443/livez >/dev/null 2>&1; then
    echo "ADHAR_OIDC: configured: the API server trusts %[7]s (backup $B)"; exit 0
  fi
done
cp "$B" "$M"
echo "ADHAR_OIDC: error: the API server did not become healthy with --authentication-config; restored $B"
exit 1
`, apiServerManifest, discovery, apiServerAuthnDir, apiServerAuthnFile, conf, apiServerManifestBkDir, cfg.IssuerURL), nil
}

// ComputeSSHUserProvider is an OPTIONAL capability of the providers that build
// a kubeadm cluster on raw instances (`clusterMode: compute`): the login user
// on those instances. With the cluster's SSH key (LoadClusterSSHKey) that is
// what day-2 control-plane steps like the OIDC trust above need.
type ComputeSSHUserProvider interface {
	ComputeSSHUser() string
}

// RunAPIServerOIDC runs APIServerOIDCScript on the control-plane node over SSH
// and returns its ADHAR_OIDC result line. A result of "skipped" is not an
// error: the node is not ours to change, or is already configured another way.
func RunAPIServerOIDC(signer ssh.Signer, user, host string, cfg APIServerOIDC) (string, error) {
	script, err := APIServerOIDCScript(cfg)
	if err != nil {
		return "", err
	}
	// base64 through the SSH command line: the script contains quotes, a
	// heredoc and $-expansions that must reach the node's shell untouched.
	cmd := "echo " + base64.StdEncoding.EncodeToString([]byte(script)) + " | base64 -d | sudo bash"
	out, runErr := SSHRun(signer, user, host, cmd, 6*time.Minute)
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "ADHAR_OIDC: ") {
			line = strings.TrimPrefix(l, "ADHAR_OIDC: ")
		}
	}
	if line == "" {
		if runErr != nil {
			return "", fmt.Errorf("configuring the API server's OIDC trust on %s: %w", host, runErr)
		}
		return "", fmt.Errorf("configuring the API server's OIDC trust on %s: no result from the node", host)
	}
	if strings.HasPrefix(line, "error: ") {
		return line, fmt.Errorf("%s", strings.TrimPrefix(line, "error: "))
	}
	return line, nil
}

// EnableClusterOIDC makes a compute-mode cluster's API server trust the
// platform's Keycloak. It reads everything it needs from the cluster itself:
// the mode (adhar-cluster-spec — never a `provided` or managed cluster, whose
// control plane is not ours), the control-plane node's external address, and
// the issuer from Keycloak's own HTTPRoute.
func EnableClusterOIDC(ctx context.Context, prov Provider, clusterName string, kubeconfig []byte) (string, error) {
	sshProv, ok := prov.(ComputeSSHUserProvider)
	if !ok {
		return "skipped: this provider does not build self-managed control planes", nil
	}
	rc, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return "", fmt.Errorf("parsing the kubeconfig: %w", err)
	}
	kc, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return "", err
	}
	spec, err := kc.CoreV1().ConfigMaps("adhar-system").Get(ctx, "adhar-cluster-spec", metav1.GetOptions{})
	if err == nil && spec.Data["clusterMode"] != "" && spec.Data["clusterMode"] != "compute" {
		return fmt.Sprintf("skipped: clusterMode %s — the control plane is not managed by adhar", spec.Data["clusterMode"]), nil
	}
	nodes, err := kc.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/control-plane"})
	if err != nil {
		return "", fmt.Errorf("listing control-plane nodes: %w", err)
	}
	host := ""
	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeExternalIP && host == "" {
				host = a.Address
			}
		}
	}
	if host == "" {
		return "", fmt.Errorf("no control-plane node with an external address")
	}
	issuer, err := keycloakIssuer(ctx, rc)
	if err != nil {
		return "", err
	}
	signer, err := LoadClusterSSHKey(clusterName)
	if err != nil {
		return "", err
	}
	return RunAPIServerOIDC(signer, sshProv.ComputeSSHUser(), host, APIServerOIDC{IssuerURL: issuer})
}

// keycloakIssuer is the platform realm's issuer URL, from the hostname of the
// keycloak package's HTTPRoute — the same URL every OIDC client is given.
func keycloakIssuer(ctx context.Context, rc *rest.Config) (string, error) {
	dc, err := dynamic.NewForConfig(rc)
	if err != nil {
		return "", err
	}
	gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	route, err := dc.Resource(gvr).Namespace("adhar-system").Get(ctx, "keycloak", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("Keycloak's HTTPRoute is not there yet (%w); run `adhar cluster oidc` once Keycloak is up", err)
	}
	hosts, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if len(hosts) == 0 {
		return "", fmt.Errorf("Keycloak's HTTPRoute has no hostname")
	}
	return "https://" + hosts[0] + "/realms/adhar", nil
}
