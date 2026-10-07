package adharplatform

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The platform has ONE environment model, and every layer must speak it.
//
// `adhar up` creates namespaces dev/test/prod from the config, and every
// application's own Kargo pipeline promotes dev -> test -> prod with prod held
// for a person (adhar-supply-chain/90-app-environments.yaml). But the platform
// pipeline shipped staging -> production and the environments GitOps repo held
// development/staging/testing/production — so the Kargo UI and the console
// showed environments that existed nowhere else, and the operator who had
// declared three environments could not find them (2026-10-04).
var environmentModel = []string{"dev", "test", "prod"}

// The environments repo has exactly the model's directories, plus local.
func TestEnvironmentsRepoMatchesTheModel(t *testing.T) {
	dir := filepath.Join(stackPackagesDir(t), "..", "environments")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		if e.IsDir() {
			got = append(got, e.Name())
		}
	}
	sort.Strings(got)
	want := append([]string{"local"}, environmentModel...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("environments repo directories are %v, want %v — a directory the pipeline does not "+
			"promote into is an environment nobody can reach, and one it promotes into that does "+
			"not exist fails the first promotion", got, want)
	}
	// Each directory must declare itself by the same name, or the promotion
	// copies a file whose `environment:` contradicts where it landed.
	for _, env := range environmentModel {
		b, err := os.ReadFile(filepath.Join(dir, env, "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "environment: "+env+"\n") {
			t.Errorf("environments/%s/config.yaml does not declare `environment: %s`", env, env)
		}
	}
}

// The platform Kargo pipeline promotes through exactly the model's stages, with
// the model's policy: dev and test automatic, prod manual.
func TestPlatformKargoPipelineMatchesTheModel(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "application/kargo/manifests/promotion-pipeline.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	policies := map[string]bool{}
	var warehousePaths []string
	for _, doc := range strings.Split(string(b), "\n---") {
		var m map[string]interface{}
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil || m == nil {
			continue
		}
		switch m["kind"] {
		case "Stage":
			stages = append(stages, m["metadata"].(map[string]interface{})["name"].(string))
		case "ProjectConfig":
			for _, p := range m["spec"].(map[string]interface{})["promotionPolicies"].([]interface{}) {
				pm := p.(map[string]interface{})
				policies[pm["stageSelector"].(map[string]interface{})["name"].(string)] = pm["autoPromotionEnabled"].(bool)
			}
		case "Warehouse":
			for _, sub := range m["spec"].(map[string]interface{})["subscriptions"].([]interface{}) {
				g := sub.(map[string]interface{})["git"].(map[string]interface{})
				for _, ip := range g["includePaths"].([]interface{}) {
					warehousePaths = append(warehousePaths, ip.(string))
				}
			}
		}
	}
	if strings.Join(stages, ",") != strings.Join(environmentModel, ",") {
		t.Errorf("platform Kargo stages are %v, want %v in that order", stages, environmentModel)
	}
	want := map[string]bool{"dev": true, "test": true, "prod": false}
	for env, auto := range want {
		got, ok := policies[env]
		if !ok {
			t.Errorf("no promotion policy for %s", env)
			continue
		}
		if got != auto {
			t.Errorf("%s autoPromotionEnabled=%v, want %v — prod must wait for a person, dev and test "+
				"must not", env, got, auto)
		}
	}
	// The warehouse must watch the model's source environment.
	if len(warehousePaths) != 1 || !strings.Contains(warehousePaths[0], "dev/") {
		t.Errorf("warehouse watches %v, want the dev/ directory — a path for an environment that no "+
			"longer exists leaves it at \"No commits discovered\" forever", warehousePaths)
	}
	// Every promotion copies between directories that exist.
	src := string(b)
	for _, old := range []string{"development/", "staging/", "testing/", "production/"} {
		if strings.Contains(src, "./src/"+old) {
			t.Errorf("a promotion step still references %s, which is not in the environments repo", old)
		}
	}
}

// The platform pipeline and the per-application pipeline must agree.
//
// Since ADR-0026 the per-application pipeline is composed: the Project
// XRD's default `environments` is the environment list, and the project
// composition's ProjectConfig carries the promotion policy, keyed on the Stage's
// environment label. The paved-road Task no longer iterates a fixed list — it
// reads the project — so the agreement is checked where it is now written.
func TestPlatformAndAppPipelinesAgreeOnTheModel(t *testing.T) {
	xb, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "xrd/project.xrd.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var xrd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema map[string]interface{} `json:"openAPIV3Schema"`
				}
			}
		}
	}
	if err := yaml.Unmarshal(xb, &xrd); err != nil {
		t.Fatal(err)
	}
	envsField := xrd.Spec.Versions[0].Schema.OpenAPIV3Schema["properties"].(map[string]interface{})["spec"].(map[string]interface{})["properties"].(map[string]interface{})["parameters"].(map[string]interface{})["properties"].(map[string]interface{})["environments"].(map[string]interface{})
	defaults, _ := envsField["default"].([]interface{})
	var names []string
	auto := map[string]bool{}
	for _, d := range defaults {
		m := d.(map[string]interface{})
		names = append(names, m["name"].(string))
		auto[m["name"].(string)], _ = m["autoPromote"].(bool)
	}
	if strings.Join(names, ",") != strings.Join(environmentModel, ",") {
		t.Errorf("Project's default environments are %v, want %v in that order", names, environmentModel)
	}
	for env, want := range map[string]bool{"dev": true, "test": true, "prod": false} {
		if auto[env] != want {
			t.Errorf("default autoPromote for %s is %v, want %v — the platform pipeline promotes dev and test "+
				"automatically and never prod", env, auto[env], want)
		}
	}
	cb, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/project/local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cb), `(ne $en "prod")`) {
		t.Error("the project composition's fallback autoPromote is no longer `environment != prod`")
	}
	tb, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "application/adhar-supply-chain/manifests/90-app-environments.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tb), "for env in dev test prod; do") {
		t.Error("the paved-road Task hardcodes the environment list again; it must read the project's status.environments")
	}
	if !strings.Contains(string(tb), "kind: Application") {
		t.Error("the paved-road Task must register an application by applying a Application")
	}
}
