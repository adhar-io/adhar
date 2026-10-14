package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// `imagePullSecrets: [{name: x}]` and the block form's `- name: x`.
	pullSecretInline = regexp.MustCompile(`imagePullSecrets:\s*\[\s*\{\s*name:\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)
	// A Secret declaring the basic-auth type, with its name above or below.
	secretName = regexp.MustCompile(`(?m)^\s*name:\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*$`)
)

// A basic-auth Secret must never be referenced as an imagePullSecret.
//
// `imagePullSecrets` accepts ONLY kubernetes.io/dockerconfigjson (or the
// legacy dockercfg). Given anything else the kubelet SILENTLY IGNORES it —
// there is no event, no warning — so the pull goes out anonymous and a private
// registry answers 401.
//
// The failure also hides: for as long as the pod stays on the node kpack built
// its image on, containerd already has the layers and no pull happens. It
// appears the first time the pod is rescheduled, which makes it read as a
// cluster or registry fault rather than a manifest one.
//
// Live on 2026-10-13: the scaffolded Deployment templates named `harbor-push`
// (kpack's kubernetes.io/basic-auth PUSH credential) instead of
// `harbor-dockercfg`. After node churn moved the pods:
//
//	Failed to pull image "harbor-core.adhar-system.svc.cluster.local/library/
//	my-service:latest": ... 401 Unauthorized
//
// my-service and reactapp stayed in ImagePullBackOff, their Services had zero
// ready endpoints, and every request returned Envoy's "no healthy upstream".
func TestNoBasicAuthSecretIsUsedAsAnImagePullSecret(t *testing.T) {
	root := stackPackagesDir(t)

	// Every Secret the stack declares with the basic-auth type.
	basicAuth := map[string]string{} // name -> file that declares it
	walkStackFiles(t, root, func(path, body string) {
		for _, doc := range strings.Split(body, "\n---") {
			if !strings.Contains(doc, "kubernetes.io/basic-auth") {
				continue
			}
			for _, m := range secretName.FindAllStringSubmatch(doc, -1) {
				basicAuth[m[1]] = path
			}
		}
	})
	if len(basicAuth) == 0 {
		t.Skip("the stack declares no basic-auth Secrets; nothing to confuse")
	}

	walkStackFiles(t, root, func(path, body string) {
		for _, m := range pullSecretInline.FindAllStringSubmatch(body, -1) {
			name := m[1]
			if decl, bad := basicAuth[name]; bad {
				t.Errorf("%s uses imagePullSecrets: %s, but %s declares that Secret as "+
					"kubernetes.io/basic-auth. The kubelet ignores a pull secret of the wrong "+
					"type without reporting it, so the pull is anonymous and the registry "+
					"returns 401 — visible only once the pod is rescheduled",
					rel(t, path), name, rel(t, decl))
			}
		}
	})
}

func walkStackFiles(t *testing.T, root string, fn func(path, body string)) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yaml.tmpl") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		fn(path, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}
