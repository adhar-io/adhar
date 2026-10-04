package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	cnpgAPI       = regexp.MustCompile(`(?m)^apiVersion: postgresql\.cnpg\.io/`)
	kindCluster   = regexp.MustCompile(`(?m)^kind: Cluster$`)
	kindAnything  = regexp.MustCompile(`(?m)^kind: (\S+)$`)
	resourceName  = regexp.MustCompile(`(?m)^  name: (\S+)`)
	nodeBoundName = "adhar-node-bound"
)

// Every CNPG database must be able to claim room on the node its volume pins it
// to.
//
// The default StorageClass is node-local, so a local-path PV carries
// `kubernetes.io/hostname` node affinity and the pod has exactly ONE legal
// placement. CNPG's `initdb` Job consumes the PVC first — that is what binds the
// volume — so the node is chosen before the instance pod exists. If it fills in
// between, the instance is unschedulable forever and adding nodes cannot help:
//
//	0/5 nodes are available: 1 node(s) didn't match PersistentVolume's node
//	affinity, ... No preemption victims found for incoming pod.
//
// At the default priority of 0 there are no lower-priority victims. Measured on
// Azure 2026-10-04: keycloak-db sat Pending for 70 minutes asking for 100m while
// 52 pods that could have run anywhere held the node — and Keycloak gates the
// entire SSO estate, so the platform could not converge.
func TestEveryCNPGDatabaseIsNodeBoundPriority(t *testing.T) {
	root := stackPackagesDir(t)
	var checked int
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".tmpl") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, doc := range strings.Split(string(b), "\n---") {
			if !cnpgAPI.MatchString(doc) || !kindCluster.MatchString(doc) {
				continue
			}
			checked++
			if !strings.Contains(doc, "priorityClassName: "+nodeBoundName) {
				name := "?"
				if m := resourceName.FindStringSubmatch(doc); len(m) == 2 {
					name = m[1]
				}
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s: CNPG Cluster %q has no `priorityClassName: %s`. Its volume pins it "+
					"to one node, so at the default priority of 0 it can be stranded there "+
					"permanently — see data/cnpg/manifests/priorityclass.yaml", rel, name, nodeBoundName)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the stack: %v", err)
	}
	if checked == 0 {
		t.Fatal("no CNPG Clusters found — this guard is not looking at anything, so a database " +
			"added without the priority class would pass silently")
	}
	t.Logf("checked %d CNPG Cluster definitions", checked)
}

// The PriorityClass must be declared, or every pod naming it is REJECTED by
// Kubernetes — a worse failure than the one it fixes.
func TestNodeBoundPriorityClassIsDeclared(t *testing.T) {
	path := filepath.Join(stackPackagesDir(t), "data/cnpg/manifests/priorityclass.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the %s PriorityClass must be declared in the cnpg package (nothing can create a "+
			"CNPG Cluster before that package installs the CRD): %v", nodeBoundName, err)
	}
	src := string(b)
	for _, want := range []string{
		"kind: PriorityClass",
		"name: " + nodeBoundName,
		// Ahead of everything else in its package, so it exists before any pod
		// names it.
		`sync-wave: '-10'`,
		// It must be able to evict, or it changes nothing.
		"preemptionPolicy: PreemptLowerPriority",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("priorityclass.yaml must contain %q", want)
		}
	}
	if strings.Contains(src, "globalDefault: true") {
		t.Error("it must NOT be the global default — the justification is a pod having no " +
			"alternative placement, not importance, and making everything preemptive-high " +
			"removes the victims that make preemption work")
	}
}

// It must not outrank the control plane, and must outrank work that can wait.
func TestNodeBoundPrioritySitsBetweenBuildsAndTheControlPlane(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "data/cnpg/manifests/priorityclass.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^value: (\d+)`).FindStringSubmatch(string(b))
	if len(m) != 2 {
		t.Fatal("priorityclass.yaml has no value")
	}
	var v int
	for _, c := range m[1] {
		v = v*10 + int(c-'0')
	}
	// kpack's highest build class is 10000: a container build can wait.
	if v <= 10000 {
		t.Errorf("value %d does not outrank kpack's build classes, so a build can still strand a "+
			"database", v)
	}
	// system-cluster-critical is 2000000000 and must stay above.
	if v >= 2000000000 {
		t.Errorf("value %d is at or above system-cluster-critical; a database must never be able "+
			"to starve the control plane", v)
	}
}

// stackPackagesDir locates platform/stack/packages from the test's directory.
func stackPackagesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../stack/packages")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("stack packages not found at %s: %v", dir, err)
	}
	return dir
}
