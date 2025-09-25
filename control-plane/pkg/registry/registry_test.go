package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndValidate(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}

	controlPlaneRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	registryPath := filepath.Join(controlPlaneRoot, "features", "registry.yaml")

	r, err := Load(registryPath)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}

	if err := r.Validate(controlPlaneRoot); err != nil {
		t.Fatalf("validate registry: %v", err)
	}

	cluster, ok := r.Command("cluster")
	if !ok {
		t.Fatalf("expected cluster command in registry")
	}

	expectedCompositions := map[string]string{
		"aws-eks":           "configuration/compositions/cluster/aws-eks.yaml",
		"gcp-gke":           "configuration/compositions/cluster/gcp-gke.yaml",
		"azure-aks":         "configuration/compositions/cluster/azure-aks.yaml",
		"digitalocean-doks": "configuration/compositions/cluster/digitalocean-doks.yaml",
		"civo-k3s":          "configuration/compositions/cluster/civo-k3s.yaml",
	}

	if len(cluster.Compositions) != len(expectedCompositions) {
		t.Fatalf("cluster compositions mismatch: got %d want %d", len(cluster.Compositions), len(expectedCompositions))
	}

	for _, comp := range cluster.Compositions {
		expectedFile, ok := expectedCompositions[comp.Name]
		if !ok {
			t.Fatalf("unexpected cluster composition %q", comp.Name)
		}
		if comp.File != expectedFile {
			t.Fatalf("composition %s file mismatch: got %s want %s", comp.Name, comp.File, expectedFile)
		}
	}

	apps, ok := r.Command("apps")
	if !ok {
		t.Fatalf("expected apps command in registry")
	}
	if len(apps.Compositions) != 1 {
		t.Fatalf("apps compositions mismatch: got %d want 1", len(apps.Compositions))
	}
	if apps.Compositions[0].Name != "argocd-application" {
		t.Fatalf("unexpected apps composition name %q", apps.Compositions[0].Name)
	}

	gitops, ok := r.Command("gitops")
	if !ok {
		t.Fatalf("expected gitops command in registry")
	}
	if len(gitops.Compositions) != 1 {
		t.Fatalf("gitops compositions mismatch: got %d want 1", len(gitops.Compositions))
	}
	if gitops.Compositions[0].Name != "argocd-project" {
		t.Fatalf("unexpected gitops composition name %q", gitops.Compositions[0].Name)
	}
}
