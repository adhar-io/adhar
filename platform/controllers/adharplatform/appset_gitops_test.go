package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"adhar-io/adhar/api/v1alpha1"
)

// The Marketplace enables and disables packages by committing to the
// ApplicationSet. Until 2026-10-24 that file was never in Gitea — the CLI
// applied it from its checkout — so every toggle failed with "Could not locate
// the ApplicationSet YAML". These tests hold the three halves together: seeding
// commits it, an Argo CD Application applies it, the console edits it.

func TestSeedingCommitsTheProfilesApplicationSet(t *testing.T) {
	stack := filepath.Join("..", "..", "stack")
	for provider, file := range map[v1alpha1.EnvironmentProvider]string{
		v1alpha1.ProviderKind: "adhar-appset-local.yaml",
		"aws":                 "adhar-appset-production.yaml",
	} {
		staged := t.TempDir()
		if err := InjectPlatformAppSet(staged, stack, provider); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(staged, PlatformAppSetRepoFile))
		if err != nil {
			t.Fatalf("%s: %s was not staged: %v", provider, PlatformAppSetRepoFile, err)
		}
		want, _ := os.ReadFile(filepath.Join(stack, file))
		// On a fresh repo the staged file is the profile file plus the merge
		// base annotation (SeededEnabledAnnotation) and nothing else.
		var kept []string
		for _, line := range strings.Split(string(got), "\n") {
			if line == "  annotations:" || strings.Contains(line, SeededEnabledAnnotation+":") {
				continue
			}
			kept = append(kept, line)
		}
		if strings.Join(kept, "\n") != string(want) {
			t.Errorf("%s: the staged ApplicationSet differs from %s beyond the %s annotation", provider, file, SeededEnabledAnnotation)
		}
	}
}

func TestPlatformApplicationSetSourceCannotCascadeADelete(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "stack", "adhar-appset-source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var app struct {
		Metadata struct{ Finalizers []string } `json:"metadata"`
		Spec     struct {
			Source struct {
				RepoURL   string `json:"repoURL"`
				Directory struct {
					Recurse bool   `json:"recurse"`
					Include string `json:"include"`
				} `json:"directory"`
			} `json:"source"`
			SyncPolicy struct {
				Automated struct{ Prune, SelfHeal bool } `json:"automated"`
			} `json:"syncPolicy"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &app); err != nil {
		t.Fatal(err)
	}
	if len(app.Metadata.Finalizers) > 0 {
		t.Error("the source Application carries a finalizer: deleting it would cascade into the ApplicationSet and every platform app")
	}
	if app.Spec.SyncPolicy.Automated.Prune {
		t.Error("prune must stay off: an empty or broken applicationset.yaml would delete the ApplicationSet")
	}
	if !app.Spec.SyncPolicy.Automated.SelfHeal {
		t.Error("selfHeal must be on, or a Marketplace commit is not applied until someone syncs by hand")
	}
	if app.Spec.Source.Directory.Include != PlatformAppSetRepoFile || app.Spec.Source.Directory.Recurse {
		t.Errorf("the source must apply exactly %s at the repo root, got include=%q recurse=%v",
			PlatformAppSetRepoFile, app.Spec.Source.Directory.Include, app.Spec.Source.Directory.Recurse)
	}
	prod, _ := os.ReadFile(filepath.Join("..", "..", "stack", "adhar-appset-production.yaml"))
	if !strings.Contains(string(prod), "repoURL: "+app.Spec.Source.RepoURL+"\n") {
		t.Errorf("the source reads %s, which is not the packages repo the ApplicationSet itself uses", app.Spec.Source.RepoURL)
	}
}

// The console tells platform packages from the apps teams build by the
// `adhar.io/category` label alone, and every ApplicationSet-generated package
// carries one. The source Application is created outside the ApplicationSet,
// and without the label it was listed in the Delivery Flow as a team's app.
func TestThePlatformApplicationSetSourceIsFiledAsPlatform(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "stack", "adhar-appset-source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var app struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := yaml.Unmarshal(raw, &app); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(app.Metadata.Labels["adhar.io/category"]) == "" {
		t.Error("adhar-platform-applicationset has no adhar.io/category label; the console files it as a team's app")
	}
}

func TestTheConsoleCommitsToTheSeededApplicationSet(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "stack", "packages", "core", "adhar-console", "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, want := range []string{
		"- name: ADHAR_APPSET_REPO\n          value: adhar/packages\n",
		"- name: ADHAR_APPSET_FILE\n          value: " + PlatformAppSetRepoFile + "\n",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the console is not pointed at the seeded ApplicationSet; missing %q", want)
		}
	}
}

func TestTheStackApplyInstallsTheApplicationSetSource(t *testing.T) {
	src, err := os.ReadFile("controller.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, `filepath.Join(r.StackDir, "adhar-appset-source.yaml")`) {
		t.Error("applyPlatformStack no longer applies adhar-appset-source.yaml; Marketplace commits would never reach the cluster")
	}
	if !strings.Contains(s, "kept, err := InjectMergedPlatformAppSet(stagedSrc, r.StackDir, provider, head, fallbackBase)") {
		t.Error("seeding no longer commits the ApplicationSet, merged with Gitea's copy, into the packages repo")
	}
}

// Once the ApplicationSet exists, the packages repo is its source. Re-applying
// the CLI's file on every pass force-set each `enabled` flag back to the
// checkout and re-created packages the Marketplace had disabled.
func TestTheStackApplyOnlyCreatesTheApplicationSet(t *testing.T) {
	src, err := os.ReadFile("controller.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "exists, err := r.platformAppSetExists(ctx, appSetBytes)") ||
		!strings.Contains(s, "if !exists {\n\t\tif err := r.applyManifestNoOwner(ctx, appSetBytes, resource, \"Platform stack ApplicationSet\")") {
		t.Error("the platform ApplicationSet is applied over an existing one again; Marketplace toggles would be reverted on every reconcile")
	}
}
