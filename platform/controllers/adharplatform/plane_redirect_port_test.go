package adharplatform

import (
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"adhar-io/adhar/api/v1alpha1"
)

// Plane's root redirect sends a signed-out visitor to its own Keycloak login.
// A local platform on 8443 needs the port stated, or the Location points at
// 443 where nothing listens; behind a real 443 it is left to the listener
// rather than hardcoded.
func TestPlaneRootRedirectStatesOnlyANonDefaultPort(t *testing.T) {
	pkg := filepath.Join("..", "..", "stack", "packages", "application", "plane")
	for _, tc := range []struct {
		spec v1alpha1.BuildCustomizationSpec
		want any // nil = no port field
	}{
		{v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: "dev.adhar.io", Port: "443"}, nil},
		{v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: "adhar.localtest.me", Port: "8443"}, float64(8443)},
	} {
		spec := tc.spec
		spec.Normalize()
		out, cleanup, err := stageStack(pkg, spec, spec.TXTOwnerID())
		if err != nil {
			t.Fatalf("staging plane for port %s: %v", spec.Port, err)
		}
		rendered := read(t, filepath.Join(out, "manifests", "httproute.yaml"))
		cleanup()
		found := false
		for _, doc := range strings.Split(rendered, "\n---") {
			var route struct {
				Kind string
				Spec struct {
					Rules []struct {
						Filters []struct {
							Type            string
							RequestRedirect map[string]any `json:"requestRedirect"`
						}
					}
				}
			}
			if err := yaml.Unmarshal([]byte(doc), &route); err != nil || route.Kind != "HTTPRoute" {
				continue
			}
			for _, r := range route.Spec.Rules {
				for _, f := range r.Filters {
					if f.Type != "RequestRedirect" {
						continue
					}
					found = true
					if got := f.RequestRedirect["port"]; got != tc.want {
						t.Errorf("port %s: redirect port = %v, want %v", spec.Port, got, tc.want)
					}
				}
			}
		}
		if !found {
			t.Fatalf("port %s: no RequestRedirect in Plane's HTTPRoute", spec.Port)
		}
	}
}

// A visitor holding an anonymous Plane session (left by any abandoned or failed
// sign-in) bypasses the root redirect and lands on Plane's sign-in page, which
// waits for a click on its only button. The web pod serves its own index.html
// with auto-sso.js loaded first; these pin the pieces that must agree, in the
// RENDERED manifest Argo CD applies — the init container runs the web
// container's own image (so the patch follows every bump), both files are
// mounted over the SPA, and the script carries its no-loop guards.
func TestPlaneSignInStartsKeycloakWithoutAClick(t *testing.T) {
	pkg := filepath.Join("..", "..", "stack", "packages", "application", "plane")
	script := read(t, filepath.Join(pkg, "auto-sso.js"))
	for what, want := range map[string]string{
		"only on the sign-in page":              `location.pathname !== "/"`,
		"never after an auth error":             `q.has("error_code")`,
		"never twice in a row":                  "Date.now() - last < 30000",
		"only for a visitor with no Plane user": `me.status !== 401 && me.status !== 403`,
		"only when Keycloak is the only method": `!c.is_keycloak_enabled || others.some(`,
		"starts Plane's own Keycloak flow":      `location.replace("/auth/keycloak/"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("auto-sso.js: %s (missing %q)", what, want)
		}
	}
	var web, cm map[string]any
	for _, doc := range strings.Split(read(t, filepath.Join(pkg, "manifests", "install.yaml")), "\n---") {
		var o map[string]any
		if yaml.Unmarshal([]byte(doc), &o) != nil || o == nil {
			continue
		}
		meta, _ := o["metadata"].(map[string]any)
		switch {
		case o["kind"] == "Deployment" && meta["name"] == "plane-web-wl":
			web = o
		case o["kind"] == "ConfigMap" && meta["name"] == "plane-web-auto-sso":
			cm = o
		}
	}
	if web == nil || cm == nil {
		t.Fatalf("install.yaml: plane-web-wl Deployment found=%v, plane-web-auto-sso ConfigMap found=%v", web != nil, cm != nil)
	}
	if got := cm["data"].(map[string]any)["auto-sso.js"]; strings.TrimSpace(got.(string)) != strings.TrimSpace(script) {
		t.Error("the shipped ConfigMap differs from auto-sso.js: rerun generate-manifests.sh")
	}
	pod := web["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	inits, _ := pod["initContainers"].([]any)
	if len(inits) == 0 {
		t.Fatal("plane-web-wl has no init container to add the script to index.html")
	}
	initC, webC := inits[0].(map[string]any), pod["containers"].([]any)[0].(map[string]any)
	if initC["image"] != webC["image"] {
		t.Errorf("init image %v != web image %v: the patched index.html would come from another build", initC["image"], webC["image"])
	}
	mounts := map[string]bool{}
	for _, m := range webC["volumeMounts"].([]any) {
		mounts[m.(map[string]any)["mountPath"].(string)] = true
	}
	for _, p := range []string{"/usr/share/caddy/html/index.html", "/usr/share/caddy/html/adhar-auto-sso.js"} {
		if !mounts[p] {
			t.Errorf("plane-web does not mount %s", p)
		}
	}
}
