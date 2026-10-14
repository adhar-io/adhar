package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Nexus must repair itself, because its bootstrap is a HOOK.
//
// Nexus keeps the accepted EULA and every repository definition in its own
// volume. Lose that volume and it returns with only Nexus's default
// repositories and an UNACCEPTED EULA — at which point every /repository/...
// request answers 403 while the admin REST API keeps working, so the pod is
// Ready, the Argo CD Application is Healthy, and nothing reports a fault.
//
// The bootstrap Job is a PostSync hook, and a hook does not run again once the
// app has converged. Live on 2026-10-13 the nexus-data PVC was recreated and
// nothing re-ran it: npm-hosted and maven-hosted were gone, maven-releases was
// empty, and every Maven build failed for hours with
//
//	Non-resolvable parent POM ... com.adhar.kit:adhar-kit-parent:pom:0.1.0
//	... status code: 403, reason phrase: Forbidden
//
// while `kubectl get job nexus-config` showed Complete from before the volume
// was replaced. Re-running it by hand fixed it in twelve seconds.
func TestNexusRepairsItselfOnASchedule(t *testing.T) {
	kinds := map[string]map[string]any{}
	for _, doc := range nexusConfigDocs(t) {
		k, _ := doc["kind"].(string)
		md, _ := doc["metadata"].(map[string]any)
		name, _ := md["name"].(string)
		kinds[k+"/"+name] = doc
	}

	if _, ok := kinds["CronJob/nexus-reconcile"]; !ok {
		t.Fatal("the nexus package has no reconcile CronJob; its bootstrap is a PostSync hook, " +
			"so a Nexus that loses its volume serves 403 to every build until somebody notices")
	}
	if _, ok := kinds["ConfigMap/nexus-bootstrap-script"]; !ok {
		t.Error("the bootstrap script is not in a ConfigMap, so the Job and the CronJob cannot " +
			"run the SAME script and will drift apart")
	}

	// Both workloads must run the one script — a second copy is a second thing
	// to keep correct.
	for _, key := range []string{"Job/nexus-config", "CronJob/nexus-reconcile"} {
		doc, ok := kinds[key]
		if !ok {
			t.Fatalf("%s is missing", key)
		}
		body, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		s := string(body)
		if !strings.Contains(s, "nexus-bootstrap-script") {
			t.Errorf("%s does not mount the shared bootstrap script", key)
		}
		if strings.Contains(s, "accepting the Nexus") {
			t.Errorf("%s carries an inline copy of the bootstrap script instead of mounting it", key)
		}
	}

	// The script has to stay safe to repeat, since the CronJob repeats it.
	script := nexusBootstrapScript(t, kinds["ConfigMap/nexus-bootstrap-script"])
	if !strings.Contains(script, "eula") && !strings.Contains(script, "EULA") {
		t.Error("the bootstrap script no longer accepts the EULA; without it Nexus answers 403 " +
			"to every /repository/ request while looking perfectly healthy")
	}
}

func nexusBootstrapScript(t *testing.T, cm map[string]any) string {
	t.Helper()
	data, _ := cm["data"].(map[string]any)
	s, _ := data["bootstrap.sh"].(string)
	if s == "" {
		t.Fatal("the bootstrap ConfigMap carries no bootstrap.sh")
	}
	return s
}

func nexusConfigDocs(t *testing.T) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stackPackagesDir(t),
		"application/nexus/manifests/config-job.yaml"))
	if err != nil {
		t.Fatalf("reading the nexus config manifest: %v", err)
	}
	var out []map[string]any
	for _, chunk := range strings.Split(string(b), "\n---") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(chunk), &doc); err != nil || doc == nil {
			continue
		}
		out = append(out, doc)
	}
	return out
}
