package civo

import (
	"strings"
	"testing"

	"github.com/civo/civogo"
)

// Civo COMPUTE mode installs neither the Civo cloud-controller-manager nor the
// Civo block-storage CSI, and that is deliberate.
//
// Both resolve CIVO_CLUSTER_ID against Civo's MANAGED Kubernetes API, and a
// self-managed kubeadm cluster on plain instances has no such object. Verified
// live on mum1 (2026-10-02): the CCM logs "Unable to get kubernetes cluster" and
// nil-panics in the service controller; the CSI plugin never opens its socket.
//
// Installing them was worse than pointless. `--cloud-provider=external` makes
// kubelet add node.cloudprovider.kubernetes.io/uninitialized:NoSchedule, which
// only a CCM removes — so a CCM that cannot start leaves EVERY node permanently
// unschedulable and the platform never comes up at all.
func TestComputeModeInstallsNoCivoCCMOrCSI(t *testing.T) {
	p := &Provider{config: &Config{Token: "tok", Region: "mum1"}, token: "tok"}
	var joined string
	for _, s := range p.cloudIntegrationSteps("dev", "adhar-cluster-dev") {
		joined += s.Cmd + "\n"
	}

	// Still needed: the API secret (other tooling reads it) and node-local storage.
	for _, want := range []string{
		"create secret generic civo-api-access", "--from-literal='api-key=tok'",
		"--from-literal='region=mum1'", "--from-literal='cluster-id=adhar-cluster-dev'",
		"provisioner: rancher.io/local-path",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Civo integration lacks %q", want)
		}
	}

	// Deliberately absent — each of these broke the cluster.
	for _, unwanted := range []string{
		"civo-cloud-controller-manager",
		"civo-csi",
		"external-snapshotter",
	} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("compute mode must not install %q: it needs a Civo MANAGED cluster that does not exist here", unwanted)
		}
	}
}

// An absent CCM and a node told to expect one is the combination that bricks the
// cluster, so the flag must stay false for compute mode.
func TestComputeModeDoesNotClaimAnExternalCloudProvider(t *testing.T) {
	if civoComputeHasExternalCCM {
		t.Error("compute mode has no working CCM, so kubelet must not get --cloud-provider=external: " +
			"the uninitialized taint it adds would never be removed and every node would stay unschedulable")
	}
}

// The CCM/CSI Secret must carry a real token however the token was supplied.
//
// This is the bug that broke the first live Civo cluster. The integration read
// config.Token, which is only set when the token is written INLINE in the config
// file — and every shipped example uses `useEnvironment: true` instead, precisely
// to keep a credential out of a committed file. So the Secret got an empty
// api-key, the CCM crash-looped on "CIVO_API_KEY ... must be set", the
// uninitialized taint was never lifted, and the Gateway's LoadBalancer never got
// an address: a cluster with no reachable URL, from one empty string.
func TestCCMSecretCarriesTheTokenHoweverItWasSupplied(t *testing.T) {
	t.Setenv("CIVO_TOKEN", "from-environment")

	for _, tc := range []struct {
		name   string
		config *Config
		want   string
	}{
		{"inline token", &Config{Token: "inline-token", Region: "mum1"}, "inline-token"},
		{"useEnvironment", &Config{UseEnvironment: true, Region: "mum1"}, "from-environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewProvider(tc.config)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			var joined string
			for _, s := range p.cloudIntegrationSteps("dev", "adhar-cluster-dev") {
				joined += s.Cmd + "\n"
			}
			if !strings.Contains(joined, "api-key="+tc.want) {
				t.Errorf("the civo-api-access Secret does not carry the %s token; the CCM will crash-loop on an empty CIVO_API_KEY", tc.name)
			}
			if strings.Contains(joined, "api-key='") && strings.Contains(joined, "api-key=''") {
				t.Error("the api-key is empty")
			}
		})
	}
}

// Instance sizes the Civo tests name repeatedly.
const (
	sizeMedium = "g3.medium"
	sizeLarge  = "g3.large"
	sizeXLarge = "g3.xlarge"

	// The default worker node-group name.
	groupWorkers = "workers"

	// Civo's instance status for a running machine.
	statusActive = "ACTIVE"
)

// A control plane too small to stay reachable is worth saying out loud BEFORE
// the instances are billed. The numbers come from a live mum1 bring-up where a
// g3.medium control plane bootstrapped and then stopped answering its API and
// SSH entirely under the production profile's reconcile load.
func TestControlPlaneSizeAdviceWarnsBeforeTheInstancesExist(t *testing.T) {
	cases := []struct {
		name     string
		size     string
		cpu, ram int
		warn     bool
	}{
		{"the size that wedged a live cluster", sizeMedium, 2, 4096, true},
		{"enough CPU but half the RAM", "custom", 4, 4096, true},
		{"enough RAM but half the CPU", "custom", 2, 8192, true},
		{"the smallest size that fits", sizeLarge, 4, 8192, false},
		{"comfortably larger", sizeXLarge, 6, 16384, false},
		// sizeShape returns 0,0,0 for a size the region does not list and warns
		// about that itself; inventing a shape from the name would be worse.
		{"unknown size", "g9.enormous", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := controlPlaneSizeAdvice(tc.size, tc.cpu, tc.ram)
			if tc.warn && got == "" {
				t.Errorf("%s (%d vCPU, %d MB) should warn: it is below the platform's control-plane minimum",
					tc.size, tc.cpu, tc.ram)
			}
			if !tc.warn && got != "" {
				t.Errorf("%s (%d vCPU, %d MB) should not warn, got %q", tc.size, tc.cpu, tc.ram, got)
			}
			// A warning has to be actionable: name the size and the way out.
			if tc.warn && got != "" {
				for _, want := range []string{tc.size, sizeLarge, "controlPlaneMachineType"} {
					if !strings.Contains(got, want) {
						t.Errorf("the warning does not mention %q: %s", want, got)
					}
				}
			}
		})
	}
}

// `adhar down` refuses to treat an untagged cluster as Adhar's own without
// saying so, and that warning is only useful if it stays quiet for clusters we
// did build. Civo compute clusters are identified by an `adhar-cluster-<name>`
// instance tag that nothing else writes, so the cluster object must report the
// ownership tag the CLI actually reads (cmd/helpers.IsAdharManaged).
func TestComputeClusterReportsAdharOwnership(t *testing.T) {
	p := &Provider{config: &Config{Region: "mum1"}}
	c := p.computeClusterFromInstances("adhar", []civogo.Instance{
		{Hostname: "adhar-adhar-master-1", Status: statusActive, PublicIP: "203.0.113.10",
			Tags: []string{computeClusterTag("adhar"), computeMasterTag}},
		{Hostname: "adhar-adhar-workers-1", Status: statusActive,
			Tags: []string{computeClusterTag("adhar"), computeWorkerTag}},
	})
	if c == nil {
		t.Fatal("no cluster built")
		return
	}
	if got := c.Tags["adhar.io/managed-by"]; got != "adhar" {
		t.Errorf("adhar.io/managed-by = %q, want \"adhar\" — `adhar down` would warn that a cluster "+
			"Adhar created is not Adhar-managed, on every teardown", got)
	}
}

// Civo installs marketplace applications on a managed cluster unless told not
// to, and "no applications specified" means "all the defaults", not "none".
//
// As of 2026-10-07 `GET /v2/kubernetes/applications` marks two as default:
// traefik2-nodeport and metrics-server. Both duplicate something the platform
// installs itself — the Cilium Gateway is the ingress here, and
// observability/metrics-server owns v1beta1.metrics.k8s.io — so a cluster that
// takes the defaults comes up with two ingress controllers and two
// metrics-servers racing for one aggregated APIService.
func TestManagedClusterOptsOutOfCivoDefaultApps(t *testing.T) {
	for _, app := range []string{"traefik2-nodeport", "metrics-server"} {
		if !strings.Contains(civoRemovedDefaultApps, "-"+app) {
			t.Errorf("the managed create does not remove Civo's default %q; it would run alongside the platform's own", app)
		}
	}
	// Every entry must be a REMOVAL. An entry without the leading "-" installs
	// that application instead of removing it, which is the opposite of intent.
	for _, entry := range strings.Split(civoRemovedDefaultApps, ",") {
		if !strings.HasPrefix(strings.TrimSpace(entry), "-") {
			t.Errorf("%q does not start with '-', so Civo would INSTALL it rather than remove it", entry)
		}
	}
}
