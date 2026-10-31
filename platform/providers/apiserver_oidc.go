package provider

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
// absent.
//
// A control plane is now BORN with the flag: node prep writes an issuer-less
// file and `kubeadm init` points the API server at it (KubeadmInitMaster), so
// this step only rewrites the file, then waits until the API server reports
// that it runs it. No restart. Adding the flag here meant one, and on AWS it
// came 23 minutes into `adhar up` with ~76 Argo CD apps syncing: the API
// server refused connections for ~25 s, kube-controller-manager and
// kube-scheduler crash-restarted, and cilium-operator came back without its
// Gateway API controllers, so every HTTPRoute created after it went unserved
// for 27 minutes (2026-10-30). The restart path remains for clusters built
// before that, and restores the original manifest if the API server is not
// healthy again within three minutes.

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

// authenticationConfigAPIVersion is the API both files are written in, the
// issuer-less one a control plane starts with and Keycloak's. It is GA from
// Kubernetes 1.34; an older API server does not know it and EXITS at startup
// (1.32.0: `no kind "AuthenticationConfiguration" is registered for version
// "apiserver.config.k8s.io/v1"`), which during `kubeadm init` means no cluster.
const authenticationConfigAPIVersion = "apiserver.config.k8s.io/v1"

// authenticationConfigMinMinor is the first 1.x minor whose API server reads
// authenticationConfigAPIVersion.
const authenticationConfigMinMinor = 34

// authenticationConfigAtInit reports whether a control plane of this
// Kubernetes minor ("1.37") is built with --authentication-config. Older ones
// are built as before and get the flag from the day-2 step's restart path.
func authenticationConfigAtInit(k8sMinor string) bool {
	major, minor, ok := strings.Cut(minorOf(k8sMinor, ""), ".")
	if !ok {
		return false
	}
	ma, err := strconv.Atoi(major)
	if err != nil {
		return false
	}
	mi, err := strconv.Atoi(minor)
	if err != nil {
		return false
	}
	return ma > 1 || (ma == 1 && mi >= authenticationConfigMinMinor)
}

// issuerlessAuthenticationConfigYAML is the file a control plane starts with.
// No JWT issuer, so until the day-2 step writes Keycloak's the API server
// authenticates exactly as it would without the flag. An empty jwt list is
// valid: verified against the 1.34.0, 1.36.2 and 1.37.0 API servers.
func issuerlessAuthenticationConfigYAML() string {
	return authenticationConfigHeader +
		"apiVersion: " + authenticationConfigAPIVersion + "\n" +
		"kind: AuthenticationConfiguration\n" +
		"# No token issuer until Keycloak serves: the day-2 OIDC step writes it here.\n" +
		"jwt: []\n"
}

const authenticationConfigHeader = "# Managed by adhar (platform/providers/apiserver_oidc.go). The API server\n" +
	"# reloads this file when it changes.\n"

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
	b.WriteString(authenticationConfigHeader)
	b.WriteString("apiVersion: " + authenticationConfigAPIVersion + "\n")
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
// It prints one line starting with ADHAR_OIDC: configured, unchanged, skipped,
// or an error the caller reports.
func APIServerOIDCScript(cfg APIServerOIDC) (string, error) {
	conf, err := AuthenticationConfigYAML(cfg)
	if err != nil {
		return "", err
	}
	discovery := strings.TrimSuffix(cfg.IssuerURL, "/") + "/.well-known/openid-configuration"
	return fmt.Sprintf(`set -u
M=%[1]s
F=%[4]s
[ -f "$M" ] || { echo "ADHAR_OIDC: skipped: $M not found (not a kubeadm control plane)"; exit 0; }
if grep -q -- '--oidc-issuer-url' "$M"; then
  echo "ADHAR_OIDC: skipped: the --oidc-* flags are already set and conflict with --authentication-config"; exit 0
fi
if grep -q -- '--authentication-config=' "$M" && ! grep -qF -- "--authentication-config=$F" "$M"; then
  echo "ADHAR_OIDC: skipped: the API server reads an --authentication-config file that is not $F"; exit 0
fi
# The API server fetches the issuer's keys from THIS node, with the system
# trust store: prove both before pointing it there.
if ! curl -fsS --max-time 15 -o /dev/null %[2]q; then
  echo "ADHAR_OIDC: error: %[2]s does not answer from the control-plane node with a trusted certificate; nothing changed"; exit 1
fi
mkdir -p %[3]s
umask 022
cat > "$F.tmp" <<'ADHAR_AUTHN_EOF'
%[5]sADHAR_AUTHN_EOF
if cmp -s "$F.tmp" "$F"; then CHANGED=0; else CHANGED=1; fi
mv "$F.tmp" "$F"
if grep -qF -- "--authentication-config=$F" "$M"; then
  # Already wired (KubeadmInitMaster, or an earlier run's restart): the API
  # server reloads the file and swaps the issuer in once it has fetched its
  # keys, with no restart. Done means it reports THIS file's sha256 as the
  # configuration it runs. An issuer it cannot reach leaves the previous one
  # in force and it retries every minute, so the file stays. Every 10 s: a
  # reload takes at least that long, and /metrics is not cheap to render.
  H="sha256:$(sha256sum "$F" | cut -d' ' -f1)"
  I='^apiserver_authentication_config_controller_last_config_info{'
  X=$(mktemp)
  for i in $(seq 1 18); do
    if kubectl --kubeconfig /etc/kubernetes/admin.conf get --raw /metrics >"$X" 2>/dev/null; then
      if ! grep -q "$I" "$X"; then
        rm -f "$X"
        echo "ADHAR_OIDC: configured: the API server reloads $F (this version does not report which configuration it runs)"; exit 0
      fi
      if grep "$I" "$X" | grep -qF "hash=\"$H\""; then
        rm -f "$X"
        if [ "$CHANGED" = 1 ]; then
          echo "ADHAR_OIDC: configured: the API server trusts %[7]s (reloaded $F, no restart)"
        else
          echo "ADHAR_OIDC: unchanged: the API server already trusts %[7]s"
        fi
        exit 0
      fi
    fi
    sleep 10
  done
  rm -f "$X"
  echo "ADHAR_OIDC: error: the API server has not loaded $F after three minutes; it keeps retrying (its log says why: \"failed to update authentication config\")"; exit 1
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
	rc, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return "", fmt.Errorf("parsing the kubeconfig: %w", err)
	}
	return EnableClusterOIDCFromREST(ctx, prov, clusterName, rc)
}

// APIServerOIDCMarkerKey is the adhar-cluster-spec ConfigMap key that records
// the issuer the API server was configured to trust. Its presence means the job
// is done: the adhar-controlplane manager stops trying, and a re-run of `adhar up` or
// `adhar cluster oidc` only refreshes it.
const APIServerOIDCMarkerKey = "apiserverOIDCIssuer"

// EnableClusterOIDCFromREST is EnableClusterOIDC for a caller that already has
// a REST config — the adhar-controlplane manager, which finishes the job when `adhar
// up`'s one attempt ran before Keycloak was serving (2026-10-23: a fresh
// DigitalOcean cluster came up with no OIDC trust at all, and Headlamp's login
// failed on every API call until it was applied by hand). On success it records
// the issuer under APIServerOIDCMarkerKey in adhar-cluster-spec.
func EnableClusterOIDCFromREST(ctx context.Context, prov Provider, clusterName string, rc *rest.Config) (string, error) {
	sshProv, ok := prov.(ComputeSSHUserProvider)
	if !ok {
		return "skipped: this provider does not build self-managed control planes", nil
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
	line, err := RunAPIServerOIDC(signer, sshProv.ComputeSSHUser(), host, APIServerOIDC{IssuerURL: issuer})
	if err != nil {
		return line, err
	}
	if spec != nil && spec.Name != "" {
		patch := fmt.Sprintf(`{"data":{%q:%q}}`, APIServerOIDCMarkerKey, issuer)
		if _, perr := kc.CoreV1().ConfigMaps("adhar-system").Patch(ctx, "adhar-cluster-spec", types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
			return line, fmt.Errorf("configured, but could not record it in adhar-cluster-spec: %w", perr)
		}
	}
	return line, nil
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
