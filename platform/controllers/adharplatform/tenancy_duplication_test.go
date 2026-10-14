package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Project must not become a second manager of its Organisation's namespace.
//
// Both compositions name their namespace after themselves, so a project named
// after its organisation produces the SAME namespace — and the same quota and
// limit range. Two provider-kubernetes Objects managing one target do not
// merge: each reconcile re-applies its own copy, so the quota flips between
// the organisation's numbers and the project's for as long as both exist, and
// every resource page lists the composed object twice.
//
// Live on 2026-10-14, where the platform's own org and project are both
// `adhar`:
//
//	Namespace/adhar            owners=[Organisation/adhar] and [Project/adhar]
//	ResourceQuota/adhar-quota  likewise
//	LimitRange/adhar-limits    likewise
//
// The organisation is the authority for its own namespace, so the project
// defers when the names coincide and keeps its own namespace otherwise.
func TestAProjectDoesNotDuplicateItsOrganisationsNamespace(t *testing.T) {
	body := projectComposition(t)

	guard := strings.Index(body, "{{- if ne $name $org }}")
	if guard < 0 {
		t.Fatal("the project composition composes its namespace unconditionally; when a project " +
			"is named after its organisation that is a SECOND manager of the same Namespace, " +
			"ResourceQuota and LimitRange")
	}

	// The guard has to cover all three colliding resources and nothing else.
	closing := strings.Index(body[guard:], "\n            {{- end }}")
	if closing < 0 {
		t.Fatal("the collision guard is never closed")
	}
	guarded := body[guard : guard+closing]

	for _, want := range []string{
		"composition-resource-name: namespace",
		"composition-resource-name: quota",
		"composition-resource-name: limits",
	} {
		if !strings.Contains(guarded, want) {
			t.Errorf("%q is composed OUTSIDE the collision guard, so it is still duplicated when "+
				"the project and organisation share a name", want)
		}
	}
	// Things the organisation does NOT compose must stay unconditional — a
	// project still needs its RBAC, environments and Argo CD project.
	for _, mustNotBeGuarded := range []string{
		"composition-resource-name: team-admin-rbac",
		"composition-resource-name: appproject",
	} {
		if strings.Contains(guarded, mustNotBeGuarded) {
			t.Errorf("%q is inside the collision guard, so a project sharing its organisation's "+
				"name would silently lose it", mustNotBeGuarded)
		}
	}
}

func projectComposition(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/project/local.yaml"))
	if err != nil {
		t.Fatalf("reading the project composition: %v", err)
	}
	return string(b)
}
