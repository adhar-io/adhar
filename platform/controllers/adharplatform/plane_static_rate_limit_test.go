package adharplatform

import (
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Plane's Caddy frontends must not rate-limit static files per client IP.
//
// The fork's images allow 300 static requests a minute per {client_ip}. Behind
// the platform every request reaches Caddy from the SSO proxy and resolves to
// the load balancer's node address, so all users share one budget, and the
// onboarding page loads a few hundred JS chunks on a cold cache: the first
// visit after sign-in tripped the limit, Caddy held requests ~50 s, the proxy
// cancelled them and /onboarding/ never rendered (AWS, 2026-10-31). The package
// mounts the image's own Caddyfile with only the rate limit removed.
func TestPlaneFrontendsServeStaticFilesWithoutAPerIPLimit(t *testing.T) {
	body := readStackFile(t, "application/plane/manifests/install.yaml")
	want := map[string]struct{ container, key, fallback string }{
		"plane-web-wl":   {"plane-web", "web.Caddyfile", "try_files {path} /index.html"},
		"plane-admin-wl": {"plane-admin", "admin.Caddyfile", "try_files {path} /god-mode/index.html"},
	}
	var caddyfiles map[string]interface{}
	mounted := map[string]bool{}
	for _, doc := range regexp.MustCompile(`(?m)^---\s*$`).Split(body, -1) {
		var obj map[string]interface{}
		if yaml.Unmarshal([]byte(doc), &obj) != nil || obj == nil {
			continue
		}
		name, _ := dig(obj, "metadata", "name").(string)
		if obj["kind"] == "ConfigMap" && name == "plane-caddyfiles" {
			caddyfiles, _ = obj["data"].(map[string]interface{})
			continue
		}
		w, ok := want[name]
		if obj["kind"] != "Deployment" || !ok {
			continue
		}
		spec, _ := dig(obj, "spec", "template", "spec").(map[string]interface{})
		fromConfigMap := map[string]bool{}
		volumes, _ := spec["volumes"].([]interface{})
		for _, v := range volumes {
			vm, _ := v.(map[string]interface{})
			if dig(vm, "configMap", "name") == "plane-caddyfiles" {
				fromConfigMap[vm["name"].(string)] = true
			}
		}
		for _, c := range containersOf(dig(obj, "spec").(map[string]interface{})) {
			if c["name"] != w.container {
				continue
			}
			mounts, _ := c["volumeMounts"].([]interface{})
			for _, m := range mounts {
				mm, _ := m.(map[string]interface{})
				if mm["mountPath"] == "/etc/caddy/Caddyfile" && mm["subPath"] == w.key && fromConfigMap[mm["name"].(string)] {
					mounted[name] = true
				}
			}
		}
	}
	for deploy, w := range want {
		if !mounted[deploy] {
			t.Errorf("%s does not mount plane-caddyfiles/%s over /etc/caddy/Caddyfile; the image's per-IP limit throttles every user as one", deploy, w.key)
		}
		cf, _ := caddyfiles[w.key].(string)
		if cf == "" {
			t.Errorf("ConfigMap plane-caddyfiles has no %s", w.key)
			continue
		}
		if strings.Contains(cf, "rate_limit") {
			t.Errorf("%s still rate-limits static files", w.key)
		}
		if !strings.Contains(cf, w.fallback) {
			t.Errorf("%s lost the image's SPA fallback %q", w.key, w.fallback)
		}
	}
	if !strings.Contains(readStackFile(t, "application/plane/generate-manifests.sh"), "name: plane-caddyfiles") {
		t.Error("generate-manifests.sh does not add plane-caddyfiles; regenerating would bring the rate limit back")
	}
}
