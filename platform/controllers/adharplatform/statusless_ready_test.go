package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Kinds that publish no Ready condition. function-auto-ready counts such a
// composed resource as unready FOR EVER, and one of them holds the whole XR
// back: the cache Database of every scaffolded service with a cache sat at
// "Unready resources: redis-sm" (its ServiceMonitor), so the application's
// Argo CD apps stayed Progressing in every environment although the service
// was running (Azure, 2026-10-18). Keyed by API group so a cloud IAM `Role`,
// which reports its own status, is not mistaken for an RBAC one.
var statuslessComposedKinds = map[string]bool{
	"monitoring.coreos.com/ServiceMonitor":  true,
	"monitoring.coreos.com/PodMonitor":      true,
	"monitoring.coreos.com/PrometheusRule":  true,
	"/ConfigMap":                            true,
	"/ServiceAccount":                       true,
	"rbac.authorization.k8s.io/Role":        true,
	"rbac.authorization.k8s.io/RoleBinding": true,
	"networking.k8s.io/NetworkPolicy":       true,
	"policy/PodDisruptionBudget":            true,
	"postgresql.cnpg.io/ScheduledBackup":    true,
}

var (
	composedDocSplit = regexp.MustCompile(`\n[ \t]*---[ \t]*\n`)
	composedAPI      = regexp.MustCompile(`(?m)^([ \t]*)apiVersion:[ \t]*(\S+)[ \t]*$`)
	composedKind     = regexp.MustCompile(`(?m)^[ \t]*kind:[ \t]*(\w+)[ \t]*$`)
)

func TestEveryStatuslessComposedResourceDeclaresItselfReady(t *testing.T) {
	root := filepath.Join("..", "..", "controlplane", "configuration", "compositions")
	checked := 0
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, doc := range composedDocSplit.Split(string(b), -1) {
			// Only resources the template composes DIRECTLY carry a
			// composition-resource-name; manifests nested in a provider-kubernetes
			// Object take the Object's readiness instead.
			if !strings.Contains(doc, "gotemplating.fn.crossplane.io/composition-resource-name") {
				continue
			}
			api := composedAPI.FindStringSubmatch(doc)
			kind := composedKind.FindStringSubmatch(doc)
			if api == nil || kind == nil {
				continue
			}
			group := ""
			if i := strings.LastIndex(api[2], "/"); i >= 0 {
				group = api[2][:i]
			}
			if !statuslessComposedKinds[group+"/"+kind[1]] {
				continue
			}
			checked++
			if !strings.Contains(doc, "gotemplating.fn.crossplane.io/ready") {
				t.Errorf("%s composes a %s with no Ready condition and no gotemplating.fn.crossplane.io/ready "+
					"annotation; function-auto-ready will hold the composite unready for ever", p, kind[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("found no statusless composed resources at all; the scan no longer matches the compositions")
	}
	t.Logf("%d statusless composed resources checked", checked)
}
