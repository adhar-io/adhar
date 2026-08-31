package kind

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/utils/files"
)

type PortMapping struct {
	HostPort      string
	ContainerPort string
}

type TemplateConfig struct {
	v1alpha1.BuildCustomizationSpec
	KubernetesVersion string
	ExtraPortsMapping []PortMapping
	RegistryConfig    string
	RegistryCertsDir  string
	HTTPPort          int
	HTTPSPort         int
	// OIDCIssuerURL, when set, configures kube-apiserver to trust the platform
	// Keycloak realm so tokens minted for users (headlamp sign-in,
	// `adhar auth login`) authenticate against the Kubernetes API and map onto
	// the RBAC bindings in the keycloak package (ADR-0008).
	OIDCIssuerURL string
	// OIDCCAPath is the in-node path of the platform certificate used as
	// --oidc-ca-file (the issuer is served with the platform's own cert).
	OIDCCAPath string
	// PKIHostPath is the host directory holding the pre-generated platform
	// certificate, mounted into the node at OIDCCAPath's directory.
	PKIHostPath string
}

//go:embed resources/* testdata/custom-kind.yaml.tmpl
var configFS embed.FS

func loadConfig(path string, httpClient HttpClient) ([]byte, error) {
	var rawConfigTempl []byte
	var err error
	if path != "" {
		if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
			resp, err := httpClient.Get(path)
			if err != nil {
				return nil, fmt.Errorf("fetching remote kind config: %w", err)
			}
			defer resp.Body.Close()
			if !(resp.StatusCode < 300 && resp.StatusCode >= 200) {
				return nil, fmt.Errorf("got %d status code when fetching kind config", resp.StatusCode)
			}
			rawConfigTempl, err = io.ReadAll(resp.Body)
			if err != nil {
				return nil, fmt.Errorf("reading remote kind config body: %w", err)
			}
		} else {
			rawConfigTempl, err = os.ReadFile(path)
		}
	} else {
		rawConfigTempl, err = fs.ReadFile(configFS, "resources/kind.yaml.tmpl")
	}

	if err != nil {
		return nil, fmt.Errorf("reading kind config: %w", err)
	}
	return rawConfigTempl, nil
}

func parsePortMappings(extraPortsMapping string) []PortMapping {
	var portMappingPairs []PortMapping
	if len(extraPortsMapping) > 0 {
		// Split pairs of ports "11=1111","22=2222",etc
		pairs := strings.Split(extraPortsMapping, ",")
		// Create a slice to store PortMapping pairs.
		portMappingPairs = make([]PortMapping, len(pairs))
		// Parse each pair into PortPair objects.
		for i, pair := range pairs {
			parts := strings.Split(pair, ":")
			if len(parts) == 2 {
				portMappingPairs[i] = PortMapping{parts[0], parts[1]}
			}
		}
	}
	return portMappingPairs
}

func findRegistryConfig(registryConfigPaths []string) string {
	for _, s := range registryConfigPaths {
		path := os.ExpandEnv(s)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func renderRegistryCertsDir(cfg v1alpha1.BuildCustomizationSpec) (string, error) {
	// Render out the template
	rawConfigTempl, err := fs.ReadFile(configFS, "resources/hosts.toml.tmpl")
	if err != nil {
		return "", fmt.Errorf("reading insecure registry config %w", err)
	}

	var retBuff []byte
	if retBuff, err = files.ApplyTemplate(rawConfigTempl, cfg); err != nil {
		return "", fmt.Errorf("templating insecure registry config %w", err)
	}

	// Generate the directory structure and write the file to hosts.toml
	dir, err := os.MkdirTemp("", "idpbuilder-registry-certs.d-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir %w", err)
	}

	var hostAndPort string
	if cfg.UsePathRouting {
		hostAndPort = fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)
	} else {
		hostAndPort = fmt.Sprintf("gitea.%s:%s", cfg.Host, cfg.Port)
	}
	hostCertsDir := filepath.Join(dir, hostAndPort)
	err = os.Mkdir(hostCertsDir, 0700)
	if err != nil {
		return "", fmt.Errorf("creating temp dir for host %w", err)
	}
	hostsFile := filepath.Join(hostCertsDir, "hosts.toml")

	err = os.WriteFile(hostsFile, retBuff, 0700)
	if err != nil {
		return "", fmt.Errorf("writing insecure registry config %w", err)
	}

	// Harbor: the kind node pulls kpack-built images tagged with the in-cluster
	// Service DNS harbor-core.adhar-system.svc.cluster.local, which the node's
	// resolver cannot resolve. We dial Harbor's PINNED core ClusterIP over HTTPS
	// instead (the core cert carries that IP as a SAN — see the harbor package's
	// generate-manifests.sh and 10-internal-tls-certs.yaml), verified against the
	// platform CA already mounted at /etc/adhar/pki/tls.crt. No skip_verify.
	harborCertsDir := filepath.Join(dir, HarborCoreRegistryHost)
	if err = os.Mkdir(harborCertsDir, 0700); err != nil {
		return "", fmt.Errorf("creating temp dir for harbor host %w", err)
	}
	harborHosts := fmt.Sprintf(`server = "https://%s"

[host."https://%s"]
  capabilities = ["pull", "resolve"]
  ca = "/etc/adhar/pki/tls.crt"
`, HarborCorePinnedClusterIP, HarborCorePinnedClusterIP)
	if err = os.WriteFile(filepath.Join(harborCertsDir, "hosts.toml"), []byte(harborHosts), 0700); err != nil {
		return "", fmt.Errorf("writing harbor registry config %w", err)
	}

	return dir, nil
}

const (
	// HarborCoreRegistryHost is the in-cluster Service DNS that kpack tags images
	// with; it names the node's certs.d directory for Harbor.
	HarborCoreRegistryHost = "harbor-core.adhar-system.svc.cluster.local"
	// HarborCorePinnedClusterIP is the fixed ClusterIP pinned onto the harbor-core
	// Service (application/harbor: generate-manifests.sh injects it, and the core
	// internal-TLS cert carries it as an IP SAN). It must stay in sync with that
	// value so the node can dial Harbor over TLS without cluster DNS.
	HarborCorePinnedClusterIP = "10.96.222.222"
)
