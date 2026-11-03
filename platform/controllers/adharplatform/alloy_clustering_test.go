package adharplatform

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Alloy runs as a DaemonSet but discovers pods CLUSTER-WIDE, so every component
// that acts on those targets runs on every agent unless clustering divides them
// between the agents. loki.source.kubernetes tails each container as a follow
// stream THROUGH kube-apiserver: without clustering, 10 agents x ~460
// containers held 4,612 open pods/log streams, 33k apiserver goroutines and two
// of its cores, on a 16 GiB control plane the kubelet was evicting pods from
// (DigitalOcean, 2026-11-03) — and every log line reached Loki ten times, every
// annotated pod was scraped ten times. With clustering: 492 streams (one per
// container), 20.9k goroutines, 1.4 cores.
//
// Clustering only divides targets between agents that FORM a cluster: the
// flag, a join address, and a headless Service that resolves to every agent
// (not-ready ones included, or a restarting agent never rejoins). Each half
// without the other leaves every agent alone in a cluster of one, tailing
// everything again.
func TestAlloyDividesClusterWideTargetsAcrossItsAgents(t *testing.T) {
	fromValues, rendered := alloyConfig(t)
	if fromValues == "" || rendered != fromValues {
		t.Fatal("manifests/install.yaml's config.alloy differs from values.yaml's alloy.configMap.content: " +
			"the generator's source and its output disagree")
	}

	clustered := regexp.MustCompile(`(?s)\n\tclustering \{\s*enabled = true\s*\}`)
	consumers := regexp.MustCompile(`(?m)^((?:loki\.source\.kubernetes|prometheus\.scrape) "[^"]+") \{$`).
		FindAllStringSubmatch(fromValues, -1)
	var names []string
	for _, m := range consumers {
		names = append(names, m[1])
		if !clustered.MatchString(alloyBlock(t, fromValues, m[1])) {
			t.Errorf("%s has no `clustering { enabled = true }`: its targets come from cluster-wide discovery, "+
				"so every agent in the DaemonSet takes all of them", m[1])
		}
	}
	for _, want := range []string{`loki.source.kubernetes "pods"`, `prometheus.scrape "annotated_pods"`} {
		if !slices.Contains(names, want) {
			t.Errorf("the Alloy config no longer has %s; update this test with whatever replaced it", want)
		}
	}

	var values struct {
		Alloy struct {
			Clustering struct {
				Enabled bool `yaml:"enabled"`
			} `yaml:"clustering"`
		} `yaml:"alloy"`
	}
	if err := yaml.Unmarshal([]byte(readStackFile(t, "observability/alloy/values.yaml")), &values); err != nil {
		t.Fatal(err)
	}
	if !values.Alloy.Clustering.Enabled {
		t.Error("observability/alloy/values.yaml lacks alloy.clustering.enabled: true; the next generate-manifests.sh " +
			"run drops the --cluster.* flags and the agents stop forming a cluster")
	}

	var args []string
	var podLabels map[string]interface{}
	services := map[string]map[string]interface{}{}
	for _, d := range readYAMLDocs(t, filepath.Join(stackPackagesDir(t), "observability/alloy/manifests/install.yaml")) {
		switch d["kind"] {
		case "DaemonSet":
			if dig(d, "metadata", "name") != "alloy" {
				continue
			}
			podLabels, _ = dig(d, "spec", "template", "metadata", "labels").(map[string]interface{})
			containers, _ := dig(d, "spec", "template", "spec", "containers").([]interface{})
			for _, c := range containers {
				if cm, ok := c.(map[string]interface{}); ok && cm["name"] == "alloy" {
					for _, a := range cm["args"].([]interface{}) {
						args = append(args, a.(string))
					}
				}
			}
		case "Service":
			if name, ok := dig(d, "metadata", "name").(string); ok {
				services[name] = d
			}
		}
	}
	if !slices.Contains(args, "--cluster.enabled=true") {
		t.Errorf("the alloy container does not run with --cluster.enabled=true (args %v)", args)
	}
	join := ""
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--cluster.join-addresses="); ok {
			join = v
		}
	}
	svc := services[join]
	switch {
	case join == "":
		t.Errorf("the alloy container has no --cluster.join-addresses (args %v): no agent can find a peer", args)
	case svc == nil:
		t.Errorf("--cluster.join-addresses=%s names no Service in the package", join)
	default:
		if dig(svc, "spec", "clusterIP") != "None" {
			t.Errorf("Service %s must be headless (clusterIP: None) so its name resolves to every agent, not one VIP", join)
		}
		if dig(svc, "spec", "publishNotReadyAddresses") != true {
			t.Errorf("Service %s must publish not-ready addresses, or a restarting agent never finds its peers", join)
		}
		selector, _ := dig(svc, "spec", "selector").(map[string]interface{})
		if len(selector) == 0 {
			t.Errorf("Service %s selects nothing", join)
		}
		for k, v := range selector {
			if podLabels[k] != v {
				t.Errorf("Service %s selects %s=%v, which the alloy DaemonSet's pods do not carry", join, k, v)
			}
		}
	}
}
