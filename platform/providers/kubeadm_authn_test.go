package provider

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A control plane is BORN with --authentication-config, on a file with no
// issuer, so the day-2 OIDC step only rewrites the file. Adding the flag later
// restarted the API server 23 minutes into an AWS `adhar up`, under ~76 syncing
// Argo CD apps; cilium-operator lost its Gateway API controllers to it and
// every HTTPRoute created afterwards went unserved for 27 minutes (2026-10-30).

// nodeRoot runs a script written for a node's root filesystem inside a
// temporary directory: the node paths it uses are moved beneath the directory,
// and executables on PATH stand in for the tools that reach a cluster.
type nodeRoot struct {
	t   *testing.T
	dir string
}

func newNodeRoot(t *testing.T) *nodeRoot {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	r := &nodeRoot{t: t, dir: t.TempDir()}
	for _, d := range []string{"bin", "tmp", "etc/kubernetes/manifests"} {
		if err := os.MkdirAll(r.path("/"+d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The node has GNU coreutils; a developer's Mac may only have shasum.
	if _, err := exec.LookPath("sha256sum"); err != nil {
		if _, err := exec.LookPath("shasum"); err != nil {
			t.Skip("neither sha256sum nor shasum is available")
		}
		r.stub("sha256sum", `exec shasum -a 256 "$@"`)
	}
	return r
}

// path maps a node path into the temporary root.
func (r *nodeRoot) path(p string) string { return filepath.Join(r.dir, p) }

// stub puts an executable on the root's PATH, ahead of the real one.
func (r *nodeRoot) stub(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(r.path("/bin/"+name), []byte("#!/bin/bash\n"+body+"\n"), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

func (r *nodeRoot) write(p, content string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(r.path(p)), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(r.path(p), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *nodeRoot) read(p string) string {
	r.t.Helper()
	b, err := os.ReadFile(r.path(p))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func (r *nodeRoot) exists(p string) bool {
	_, err := os.Stat(r.path(p))
	return err == nil
}

// run executes script with bash, its node paths localized in one pass.
func (r *nodeRoot) run(script string) (string, error) {
	local := strings.NewReplacer(
		"/etc/kubernetes", r.dir+"/etc/kubernetes",
		"/var/lib/adhar", r.dir+"/var/lib/adhar",
		"/tmp/adhar-", r.dir+"/tmp/adhar-",
	).Replace(script)
	cmd := exec.Command("bash", "-c", local)
	cmd.Env = append(os.Environ(), "PATH="+r.path("/bin")+":"+os.Getenv("PATH"), "ROOT="+r.dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestTheAuthenticationConfigIsWiredFromKubernetes134(t *testing.T) {
	for minor, want := range map[string]bool{
		"1.37": true, "v1.37.2": true, "1.34": true, "2.0": true,
		// 1.32.0 exits at startup on apiserver.config.k8s.io/v1, so
		// `kubeadm init` would fail with the flag.
		"1.33": false, "1.32": false, "1.29": false, "": false, "latest": false,
	} {
		if got := authenticationConfigAtInit(minor); got != want {
			t.Errorf("authenticationConfigAtInit(%q) = %v, want %v", minor, got, want)
		}
	}
	if !authenticationConfigAtInit(KubeadmDefaultK8sMinor) {
		t.Errorf("the default minor %s must be built with the flag", KubeadmDefaultK8sMinor)
	}
}

func TestNodePrepWritesTheIssuerlessAuthenticationConfigOnce(t *testing.T) {
	script := KubeadmNodePrepScript("1.37")
	step := nodePrepAuthnConfig("1.37")
	if step == "" || !strings.Contains(script, step) {
		t.Fatal("node prep does not write the API server's authentication config")
	}
	// Before the marker: the provider runs `kubeadm init` once it sees it.
	if strings.Index(script, step) > strings.Index(script, "touch "+KubeadmCloudInitMarker) {
		t.Error("the authentication config is written after the completion marker")
	}
	// EC2 refuses user data over 16 KiB.
	if len(script) > 16*1024 {
		t.Errorf("node prep is %d bytes, over the 16 KiB EC2 user-data limit", len(script))
	}
	if strings.Contains(KubeadmNodePrepScript("1.33"), apiServerAuthnFile) || nodePrepAuthnConfig("1.33") != "" {
		t.Error("a 1.33 node got the file; its API server cannot read it")
	}

	r := newNodeRoot(t)
	if out, err := r.run(step); err != nil {
		t.Fatalf("the step failed: %v\n%s", err, out)
	}
	if got := r.read(apiServerAuthnFile); got != issuerlessAuthenticationConfigYAML() {
		t.Fatalf("node prep wrote:\n%s", got)
	}
	// Once the day-2 step has written Keycloak's issuer, node prep running
	// again must not take it away.
	keycloak, err := AuthenticationConfigYAML(APIServerOIDC{IssuerURL: "https://keycloak.cloud.adhar.io/realms/adhar"})
	if err != nil {
		t.Fatal(err)
	}
	r.write(apiServerAuthnFile, keycloak)
	if out, err := r.run(step); err != nil {
		t.Fatalf("the step failed on a second run: %v\n%s", err, out)
	}
	if r.read(apiServerAuthnFile) != keycloak {
		t.Error("a second node prep replaced the issuer the day-2 step wrote")
	}
}

func TestTheIssuerlessAuthenticationConfigIsValidAndEmpty(t *testing.T) {
	var cfg struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		JWT        []any  `yaml:"jwt"`
	}
	out := issuerlessAuthenticationConfigYAML()
	if err := yaml.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("not valid YAML: %v\n%s", err, out)
	}
	keycloak, _ := AuthenticationConfigYAML(APIServerOIDC{IssuerURL: "https://keycloak.cloud.adhar.io/realms/adhar"})
	if cfg.APIVersion != authenticationConfigAPIVersion || !strings.Contains(keycloak, "apiVersion: "+cfg.APIVersion+"\n") {
		t.Errorf("apiVersion %q: the day-2 file and this one must share one API version", cfg.APIVersion)
	}
	if cfg.Kind != "AuthenticationConfiguration" || len(cfg.JWT) != 0 {
		t.Errorf("want an AuthenticationConfiguration with no issuers:\n%s", out)
	}
}

// kubeadmConfig parses the multi-document kubeadm configuration by kind.
func kubeadmConfig(t *testing.T, out string) map[string]map[string]any {
	t.Helper()
	docs := map[string]map[string]any{}
	dec := yaml.NewDecoder(strings.NewReader(out))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("not valid YAML: %v\n%s", err, out)
		}
		if d["apiVersion"] != "kubeadm.k8s.io/v1beta4" {
			t.Errorf("%v: kubeadm >= 1.31 reads v1beta4", d["apiVersion"])
		}
		docs[d["kind"].(string)] = d
	}
	return docs
}

func TestKubeadmInitConfigIsTheFlagFormPlusTheAuthenticationConfig(t *testing.T) {
	docs := kubeadmConfig(t, kubeadmInitConfigYAML("54.151.218.140", "10.0.1.164", "10.244.0.0/16", "ip-10-0-1-164.ap-southeast-1.compute.internal"))
	init, cluster := docs["InitConfiguration"], docs["ClusterConfiguration"]
	if init == nil || cluster == nil {
		t.Fatalf("want an InitConfiguration and a ClusterConfiguration, got %v", docs)
	}
	// Everything the flag form passes, because kubeadm rejects --config mixed
	// with those flags (kubeadm 1.37 dry-runs of both forms differ only in the
	// authentication config).
	apiServer, _ := cluster["apiServer"].(map[string]any)
	networking, _ := cluster["networking"].(map[string]any)
	nodeReg, _ := init["nodeRegistration"].(map[string]any)
	for what, got := range map[string]any{
		"--skip-phases":                init["skipPhases"],
		"--node-name":                  nodeReg["name"],
		"--control-plane-endpoint":     cluster["controlPlaneEndpoint"],
		"--pod-network-cidr":           networking["podSubnet"],
		"--apiserver-cert-extra-sans":  apiServer["certSANs"],
		"no kubernetesVersion pinning": cluster["kubernetesVersion"],
	} {
		want := map[string]string{
			"--skip-phases":                "[addon/kube-proxy]",
			"--node-name":                  "ip-10-0-1-164.ap-southeast-1.compute.internal",
			"--control-plane-endpoint":     "54.151.218.140",
			"--pod-network-cidr":           "10.244.0.0/16",
			"--apiserver-cert-extra-sans":  "[54.151.218.140 10.0.1.164]",
			"no kubernetesVersion pinning": "<nil>",
		}[what]
		if s := strings.TrimSpace(strings.ReplaceAll(stringOf(got), "  ", " ")); s != want {
			t.Errorf("%s: config has %s, want %s", what, s, want)
		}
	}

	args, _ := apiServer["extraArgs"].([]any)
	if len(args) != 1 || stringOf(args[0]) != "map[name:authentication-config value:"+apiServerAuthnFile+"]" {
		t.Errorf("extraArgs = %v, want only authentication-config=%s", args, apiServerAuthnFile)
	}
	vols, _ := apiServer["extraVolumes"].([]any)
	if len(vols) != 1 {
		t.Fatalf("extraVolumes = %v", vols)
	}
	v := vols[0].(map[string]any)
	// The DIRECTORY: the day-2 step renames a new file into place, and a
	// bind-mounted file would keep showing the API server the old inode.
	if v["hostPath"] != path.Dir(apiServerAuthnFile) || v["mountPath"] != path.Dir(apiServerAuthnFile) || v["readOnly"] != true {
		t.Errorf("extraVolumes[0] = %v, want %s mounted read-only at the same path", v, path.Dir(apiServerAuthnFile))
	}

	// No node name: kubeadm takes the hostname, as with the flag form.
	plain := kubeadmConfig(t, kubeadmInitConfigYAML("10.0.0.5", "10.0.0.5", KubeadmPodCIDR, ""))
	if _, ok := plain["InitConfiguration"]["nodeRegistration"]; ok {
		t.Error("nodeRegistration set without a node name")
	}
	if sans := stringOf(plain["ClusterConfiguration"]["apiServer"].(map[string]any)["certSANs"]); sans != "[10.0.0.5]" {
		t.Errorf("certSANs = %s, want the one address once", sans)
	}
}

func stringOf(v any) string {
	if v == nil {
		return "<nil>"
	}
	var b bytes.Buffer
	switch x := v.(type) {
	case []any:
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(" ")
			}
			b.WriteString(stringOf(e))
		}
		b.WriteString("]")
	case map[string]any:
		b.WriteString("map[")
		first := true
		for _, k := range []string{"name", "value"} {
			if e, ok := x[k]; ok {
				if !first {
					b.WriteString(" ")
				}
				b.WriteString(k + ":" + stringOf(e))
				first = false
			}
		}
		b.WriteString("]")
	default:
		b.WriteString(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(fmtAny(x), "\n", " "), "\t", " ")))
	}
	return b.String()
}

func fmtAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		out, _ := yaml.Marshal(x)
		return strings.TrimSpace(string(out))
	}
}

// The init command picks its form ON THE NODE: the config form only where node
// prep wrote the file the API server is pointed at, or it would not start.
func TestKubeadmInitCommandUsesTheConfigOnlyWhereNodePrepWroteTheFile(t *testing.T) {
	const pub, priv, name = "54.151.218.140", "10.0.1.164", "ip-10-0-1-164.ap-southeast-1.compute.internal"
	cmd := kubeadmInitCommand(pub, priv, KubeadmPodCIDR, name, nil)
	if strings.Contains(cmd, "\n") {
		t.Fatalf("the command must be one line for SSHRun:\n%s", cmd)
	}
	flagForm := "init --pod-network-cidr=" + KubeadmPodCIDR + " --skip-phases=addon/kube-proxy --control-plane-endpoint=" + pub +
		" --apiserver-cert-extra-sans=" + pub + "," + priv + " --node-name=" + name

	run := func(t *testing.T, cmd string, prepare func(r *nodeRoot)) (*nodeRoot, string) {
		r := newNodeRoot(t)
		// kubeadm records how it was called, and the config it was given.
		r.stub("kubeadm", `echo "$@" >"$ROOT/kubeadm.args"
for a in "$@"; do case "$a" in --config=*) cp "${a#--config=}" "$ROOT/kubeadm.config";; esac; done`)
		prepare(r)
		if out, err := r.run(cmd); err != nil {
			t.Fatalf("the init command failed: %v\n%s", err, out)
		}
		if !r.exists("/kubeadm.args") {
			return r, ""
		}
		return r, strings.TrimSpace(r.read("/kubeadm.args"))
	}

	t.Run("node prep wrote the file", func(t *testing.T) {
		r, args := run(t, cmd, func(r *nodeRoot) { r.write(apiServerAuthnFile, issuerlessAuthenticationConfigYAML()) })
		if args != "init --config="+r.path(kubeadmInitConfigPath) {
			t.Fatalf("kubeadm %s; want only --config (kubeadm refuses it mixed with other flags)", args)
		}
		if got := r.read("/kubeadm.config"); got != kubeadmInitConfigYAML(pub, priv, KubeadmPodCIDR, name) {
			t.Errorf("kubeadm read:\n%s", got)
		}
	})
	t.Run("a node prepared without it", func(t *testing.T) {
		_, args := run(t, cmd, func(*nodeRoot) {})
		if args != flagForm {
			t.Errorf("kubeadm %s\nwant %s", args, flagForm)
		}
	})
	t.Run("an empty file is no file", func(t *testing.T) {
		_, args := run(t, cmd, func(r *nodeRoot) { r.write(apiServerAuthnFile, "") })
		if args != flagForm {
			t.Errorf("kubeadm %s", args)
		}
	})
	t.Run("already initialized", func(t *testing.T) {
		_, args := run(t, cmd, func(r *nodeRoot) {
			r.write(apiServerAuthnFile, issuerlessAuthenticationConfigYAML())
			r.write("/etc/kubernetes/admin.conf", "apiVersion: v1\n")
		})
		if args != "" {
			t.Errorf("kubeadm ran on an initialized control plane: %s", args)
		}
	})
	t.Run("the legacy oidc flags were asked for", func(t *testing.T) {
		// The API server refuses to start with both.
		legacy := kubeadmInitCommand(pub, priv, KubeadmPodCIDR, name, map[string]string{"oidc-issuer-url": "https://keycloak.x/realms/adhar"})
		_, args := run(t, legacy, func(r *nodeRoot) { r.write(apiServerAuthnFile, issuerlessAuthenticationConfigYAML()) })
		if args != flagForm {
			t.Errorf("kubeadm %s", args)
		}
	})
}

// One file, one directory, everywhere: node prep writes it, kubeadm mounts and
// points at it, the day-2 step rewrites and checks for it.
func TestEveryStepAgreesOnTheAuthenticationConfigFile(t *testing.T) {
	cfg := kubeadmInitConfigYAML("10.0.0.5", "10.0.0.6", KubeadmPodCIDR, "")
	day2, err := APIServerOIDCScript(APIServerOIDC{IssuerURL: "https://keycloak.cloud.adhar.io/realms/adhar"})
	if err != nil {
		t.Fatal(err)
	}
	for where, s := range map[string]string{
		"node prep":      nodePrepAuthnConfig("1.37"),
		"kubeadm config": cfg,
		"init command":   kubeadmInitCommand("10.0.0.5", "10.0.0.6", KubeadmPodCIDR, "", nil),
		"day-2 step":     day2,
		"the decoded config": func() string {
			c := kubeadmInitCommand("10.0.0.5", "10.0.0.6", KubeadmPodCIDR, "", nil)
			i := strings.Index(c, "echo ") + len("echo ")
			b, _ := base64.StdEncoding.DecodeString(c[i : i+strings.Index(c[i:], " ")])
			return string(b)
		}(),
	} {
		if !strings.Contains(s, apiServerAuthnFile) {
			t.Errorf("%s does not name %s", where, apiServerAuthnFile)
		}
	}
	if path.Dir(apiServerAuthnFile) != apiServerAuthnDir {
		t.Errorf("%s is not in %s, the directory kubeadm mounts", apiServerAuthnFile, apiServerAuthnDir)
	}
	if cfg != func() string {
		c := kubeadmInitCommand("10.0.0.5", "10.0.0.6", KubeadmPodCIDR, "", nil)
		i := strings.Index(c, "echo ") + len("echo ")
		b, _ := base64.StdEncoding.DecodeString(c[i : i+strings.Index(c[i:], " ")])
		return string(b)
	}() {
		t.Error("the init command does not carry kubeadmInitConfigYAML")
	}
}
