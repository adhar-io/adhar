package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An Organisation must never be able to adopt a namespace it does not own.
//
// The organisation composition emits its home namespace through a
// provider-kubernetes Object, and that Object carries the default
// deletionPolicy (Delete) with managementPolicies ['*']. So an organisation
// named after an existing namespace ADOPTS it, and deleting the organisation
// deletes it. For `default` that takes `service/kubernetes` with it.
//
// Live on 2026-10-13: the console's org registry came back empty after its
// database was restored, so it fell back to a synthetic placeholder
// ("Default Organization", id `default` — apps/console/app/server/
// organizations.ts) and the tenant back-fill promoted that placeholder into a
// real Organisation. The cluster then held:
//
//	Object default-4b2adbac9cdf | manages ns: default
//	  | deletionPolicy: <default=Delete> | mgmtPolicies: ['*'] | owner XR: ['default']
//
// — an XR whose deletion would have removed the Kubernetes default namespace.
func TestAnOrganisationCannotBeNamedAfterASystemNamespace(t *testing.T) {
	body := organisationComposition(t)

	if !strings.Contains(body, "$reserved") || !strings.Contains(body, "fail (printf") {
		t.Fatal("the organisation composition no longer refuses reserved names; without it an " +
			"organisation named `default` composes an Object that manages — and on deletion " +
			"removes — the Kubernetes default namespace")
	}

	// Every namespace the platform must never hand to a tenant.
	for _, ns := range []string{"default", "kube-system", "kube-public", "kube-node-lease", "adhar-system"} {
		if !strings.Contains(body, `"`+ns+`"`) {
			t.Errorf("%q is missing from the reserved organisation names", ns)
		}
	}

	// The refusal has to come BEFORE the namespace Object is rendered, or the
	// template emits the adoption it is meant to prevent.
	refusal := strings.Index(body, "$reserved")
	nsObject := strings.Index(body, "composition-resource-name: namespace")
	if refusal < 0 || nsObject < 0 || refusal > nsObject {
		t.Error("the reserved-name refusal must be evaluated before the namespace Object is rendered")
	}
}

func organisationComposition(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/organisation/local.yaml"))
	if err != nil {
		t.Fatalf("reading the organisation composition: %v", err)
	}
	return string(b)
}
