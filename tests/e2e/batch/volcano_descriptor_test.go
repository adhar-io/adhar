package batch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// FU-13 landed the D7 descriptor and the seed-time root queue in core/volcano
// as *.yaml.tmpl. The canary's preflight reads the descriptor's keys, its A2.10
// scenario removes the descriptor by switching off the element whose
// manifestPath holds it, and its leaves hang under the root queue it names:
// the shipped files must say what the canary reads.

var volcanoManifests = filepath.Join(repoStack, "packages", "core", "volcano", "manifests")

func readManifest(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(volcanoManifests, name))
	if err != nil {
		t.Fatalf("core/volcano ships no %s (FU-13): %v", name, err)
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s does not parse as YAML before its seed-time render: %v", name, err)
	}
	return m
}

func TestTheShippedDescriptorIsWhatThePreflightReads(t *testing.T) {
	d := readManifest(t, "scheduler-descriptor.yaml.tmpl")
	md, _ := d["metadata"].(map[string]interface{})
	if d["kind"] != "ConfigMap" || md["name"] != DescriptorName || md["namespace"] != "adhar-system" {
		t.Fatalf("the descriptor is %v %v/%v, the canary reads ConfigMap adhar-system/%s", d["kind"], md["namespace"], md["name"], DescriptorName)
	}
	data, _ := d["data"].(map[string]interface{})
	if data["scheduler"] != "volcano" || data["rootQueue"] != RootQueue {
		t.Errorf("descriptor scheduler %v rootQueue %v, the preflight wants volcano / %s", data["scheduler"], data["rootQueue"], RootQueue)
	}
	for _, k := range []string{"version", "gang", "prodDeservedCPU", "prodDeservedMemory"} {
		if v, _ := data[k].(string); v == "" {
			t.Errorf("descriptor has no %s, which the preflight requires", k)
		}
	}
	q := readManifest(t, "root-queue.yaml.tmpl")
	qmd, _ := q["metadata"].(map[string]interface{})
	if q["kind"] != "Queue" || qmd["name"] != RootQueue {
		t.Errorf("the root queue is %v %v, the canary's leaves hang under Queue %s", q["kind"], qmd["name"], RootQueue)
	}
}

// A2.10 finds the element by the descriptor's Argo CD tracking id; on a fresh
// cluster that is the element whose manifestPath holds the file.
func TestTheDescriptorShipsInTheElementA2_10SwitchesOff(t *testing.T) {
	_, declared, err := appsetElements(readLocalAppSet(t))
	if err != nil {
		t.Fatal(err)
	}
	if !declared["volcano"] {
		t.Fatal("the local ApplicationSet declares no volcano element")
	}
	appset := string(readLocalAppSet(t))
	i := strings.Index(appset, `name: "volcano"`)
	if i < 0 {
		t.Fatal(`no name: "volcano" in the local ApplicationSet`)
	}
	rest := appset[i:]
	if j := strings.Index(rest[1:], "- name: "); j > 0 {
		rest = rest[:j+1]
	}
	if !strings.Contains(rest, `manifestPath: "core/volcano/manifests"`) {
		t.Errorf("the volcano element does not sync core/volcano/manifests, where the descriptor ships:\n%s", rest)
	}
}
