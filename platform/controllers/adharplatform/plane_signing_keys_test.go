package adharplatform

import (
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Plane's signing keys must be minted per cluster, never the chart's defaults.
//
// The plane-ce chart renders SECRET_KEY and LIVE_SERVER_SECRET_KEY from its
// values.yaml, so every Plane installed from it shared one Django key: anyone
// who has read the chart can forge session cookies and password-reset tokens.
// The package generates both once (manifests/secret-keys.yaml, CreatedOnce),
// strips them from the chart's Secrets, and every consumer reads the generated
// Secret — the chart's workloads, the instance-setup hook and the
// project-access CronJob, all of which run Django.
func TestPlaneSigningKeysAreGeneratedPerCluster(t *testing.T) {
	install := readStackFile(t, "application/plane/manifests/install.yaml")
	if regexp.MustCompile(`(?m)^\s+(SECRET_KEY|LIVE_SERVER_SECRET_KEY):`).MatchString(install) {
		t.Error("install.yaml still sets SECRET_KEY or LIVE_SERVER_SECRET_KEY: the chart's public default signs every Plane's sessions")
	}
	for _, file := range []string{
		"application/plane/manifests/install.yaml",
		"application/plane/manifests/instance-setup.yaml",
		"application/plane/manifests/project-access.yaml",
	} {
		for _, doc := range regexp.MustCompile(`(?m)^---\s*$`).Split(readStackFile(t, file), -1) {
			var obj map[string]interface{}
			if yaml.Unmarshal([]byte(doc), &obj) != nil || obj == nil {
				continue
			}
			spec, _ := obj["spec"].(map[string]interface{})
			if obj["kind"] == "CronJob" {
				spec, _ = dig(obj, "spec", "jobTemplate", "spec").(map[string]interface{})
			}
			if spec == nil {
				continue
			}
			for _, c := range containersOf(spec) {
				refs := map[string]bool{}
				envFrom, _ := c["envFrom"].([]interface{})
				for _, e := range envFrom {
					if n, _ := dig(e.(map[string]interface{}), "secretRef", "name").(string); n != "" {
						refs[n] = true
					}
				}
				if (refs["plane-app-secrets"] || refs["plane-live-secrets"]) && !refs["plane-generated-keys"] {
					t.Errorf("%s: %v/%v container %v reads Plane's secrets but not plane-generated-keys; it would run without a signing key",
						file, obj["kind"], dig(obj, "metadata", "name"), c["name"])
				}
			}
		}
	}
	keys := readStackFile(t, "application/plane/manifests/secret-keys.yaml")
	for _, want := range []string{"refreshPolicy: CreatedOnce", `SECRET_KEY: "{{ .SECRET_KEY }}"`, `LIVE_SERVER_SECRET_KEY: "{{ .LIVE_SERVER_SECRET_KEY }}"`} {
		if !strings.Contains(keys, want) {
			t.Errorf("secret-keys.yaml lacks %q", want)
		}
	}
	if !strings.Contains(readStackFile(t, "application/plane/generate-manifests.sh"), "name: plane-generated-keys") {
		t.Error("generate-manifests.sh does not wire plane-generated-keys; regenerating would bring the chart's default keys back")
	}
}
