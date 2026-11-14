package adharplatform

// Volcano's root queue and scheduler descriptor are rendered at seed time from the
// cluster (design 0025 D7, D17; user decision DEC-3, 2026-11-14). `adhar up`
// resolves globalSettings.batch against the cluster's workers
// (config.ResolveBatchScheduler) and carries the result on the platform's
// BuildCustomizationSpec; stageStack renders core/volcano's *.yaml.tmpl with it.
// These guards hold the chain together from the spec to the staged files, and the
// fallback a platform created before the fields existed renders. Each was
// mutation-tested.

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	sigsyaml "sigs.k8s.io/yaml"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/utils/files"
)

// renderVolcanoSeed renders one of core/volcano's seed-time templates exactly as
// stageStack does (same engine, same data type) and returns its one object.
func renderVolcanoSeed(t *testing.T, name string, spec v1alpha1.BuildCustomizationSpec) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stackPackagesDir(t), volcanoDir, "manifests", name))
	if err != nil {
		t.Fatal(err)
	}
	out, err := files.ApplyTemplate(raw, stackTemplateData{BuildCustomizationSpec: spec})
	if err != nil {
		t.Fatalf("rendering %s: %v", name, err)
	}
	docs := manifestDocs(string(out))
	if len(docs) != 1 {
		t.Fatalf("%s renders %d objects; want 1", name, len(docs))
	}
	return docs[0]
}

// volcanoRootQueue is the root Queue as a platform with this spec is seeded.
func volcanoRootQueue(t *testing.T, spec v1alpha1.BuildCustomizationSpec) map[string]any {
	t.Helper()
	return renderVolcanoSeed(t, "root-queue.yaml.tmpl", spec)
}

// volcanoDescriptor is the adhar-batch-scheduler ConfigMap as seeded.
func volcanoDescriptor(t *testing.T, spec v1alpha1.BuildCustomizationSpec) map[string]any {
	t.Helper()
	return renderVolcanoSeed(t, "scheduler-descriptor.yaml.tmpl", spec)
}

func batchSpec(s config.BatchScheduler) v1alpha1.BuildCustomizationSpec {
	return v1alpha1.BuildCustomizationSpec{
		BatchCeilingCPU: s.CeilingCPU, BatchCeilingMemory: s.CeilingMemory,
		BatchProdDeservedCPU: s.ProdDeservedCPU, BatchProdDeservedMemory: s.ProdDeservedMemory,
	}
}

func quantityAt(t *testing.T, doc map[string]any, path ...string) resource.Quantity {
	t.Helper()
	s := toString(dig(doc, path...))
	q, err := resource.ParseQuantity(s)
	if err != nil {
		t.Fatalf("%v = %q: %v", path, s, err)
	}
	return q
}

func TestVolcanoRootQueueIsRenderedFromTheCluster(t *testing.T) {
	// The reference cluster's sizing (design §2.2: 10 workers at 7500m / 12.5Gi).
	measured, _, err := config.ResolveBatchScheduler(nil, 10, 7500, 25<<30/2)
	if err != nil {
		t.Fatal(err)
	}
	// What `adhar up` falls back to when it cannot measure the cluster, and what a
	// platform created before the spec carried the values must render too.
	fallback, _, err := config.ResolveBatchScheduler(nil, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		spec v1alpha1.BuildCustomizationSpec
		want config.BatchScheduler
	}{
		{"a platform sized from its cluster", batchSpec(measured), measured},
		{"a platform created before the seed carried batch sizing", v1alpha1.BuildCustomizationSpec{}, fallback},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := volcanoRootQueue(t, c.spec)
			for _, field := range []string{"capability", "deserved"} {
				for res, want := range map[string]string{"cpu": c.want.CeilingCPU, "memory": c.want.CeilingMemory} {
					got := quantityAt(t, q, "spec", field, res)
					if got.Cmp(resource.MustParse(want)) != 0 {
						t.Errorf("root queue spec.%s.%s = %s, want %s", field, res, got.String(), want)
					}
				}
			}
			d := volcanoDescriptor(t, c.spec)
			for key, want := range map[string]string{"prodDeservedCPU": c.want.ProdDeservedCPU, "prodDeservedMemory": c.want.ProdDeservedMemory} {
				got := quantityAt(t, d, "data", key)
				if got.Cmp(resource.MustParse(want)) != 0 {
					t.Errorf("descriptor %s = %s, want %s", key, got.String(), want)
				}
			}
			// The descriptor's data is a ConfigMap's: every value a string.
			data, _ := d["data"].(map[string]any)
			for k, v := range data {
				if _, ok := v.(string); !ok {
					t.Errorf("descriptor data.%s is %T, not a string; the API server refuses it", k, v)
				}
			}
		})
	}
	if measured == fallback {
		t.Fatal("the measured and the fallback sizing are the same; this test cannot tell them apart")
	}
}

// The seed path end to end: StageStack (what `adhar up` and `adhar upgrade` push
// to Gitea) renders the two templates into plain manifests with the spec's values.
func TestStagedStackCarriesTheBatchSizing(t *testing.T) {
	want, _, err := config.ResolveBatchScheduler(&config.BatchSettings{ProdDeserved: &config.BatchResources{CPU: "6", Memory: "9Gi"}}, 4, 15500, 60<<30)
	if err != nil {
		t.Fatal(err)
	}
	staged, cleanup, err := StageStack(stackRoot(t), batchSpec(want))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	dir := filepath.Join(staged, "packages", "core", "volcano", "manifests")
	for _, gone := range []string{"root-queue.yaml.tmpl", "scheduler-descriptor.yaml.tmpl"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("the staged stack still holds the template %s", gone)
		}
	}
	read := func(name string) map[string]any {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := sigsyaml.Unmarshal(b, &doc); err != nil {
			t.Fatalf("staged %s: %v", name, err)
		}
		return doc
	}
	q, d := read("root-queue.yaml"), read("scheduler-descriptor.yaml")
	if got := toString(dig(q, "spec", "capability", "cpu")); got != want.CeilingCPU {
		t.Errorf("staged root queue capability.cpu = %q, want %q", got, want.CeilingCPU)
	}
	if got := toString(dig(q, "spec", "deserved", "memory")); got != want.CeilingMemory {
		t.Errorf("staged root queue deserved.memory = %q, want %q", got, want.CeilingMemory)
	}
	if got := toString(dig(d, "data", "prodDeservedCPU")); got != "6" {
		t.Errorf("staged descriptor prodDeservedCPU = %q, want the configured 6", got)
	}
	if got := toString(dig(d, "data", "prodDeservedMemory")); got != "9Gi" {
		t.Errorf("staged descriptor prodDeservedMemory = %q, want the configured 9Gi", got)
	}
}
