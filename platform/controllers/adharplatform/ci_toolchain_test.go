package adharplatform

import (
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The CI pipeline must not decide a project's toolchain.
//
// Every stage used to carry a literal — `maven:3.9-eclipse-temurin-25`,
// `eclipse-temurin:25-jdk`, `node:22`, `golang:1.24`, BP_JVM_VERSION=25 — so a
// repository on Java 21 was unit-tested on 25 and one on Node 24 on 22, and a
// project could not pick its own Maven at all. Now `detect-toolchain` reads
// what the repository declares (project.toml, .java-version, .sdkmanrc,
// .tool-versions, the POM's compiler release, the Maven wrapper, .nvmrc,
// package.json engines, go.mod) and the build and test stages take their
// images and buildpack settings from its results. The only versions the
// pipeline knows are the last-resort defaults for a repository that declares
// nothing anywhere.
func TestTheCiToolchainComesFromTheRepository(t *testing.T) {
	stages := readStackFile(t, "application/adhar-supply-chain/manifests/72-app-ci-stages.yaml")
	byType := readStackFile(t, "application/adhar-supply-chain/manifests/65-build-by-type.yaml")
	build := readStackFile(t, "application/adhar-supply-chain/manifests/60-build-pipelines.yaml")
	pipeline := readStackFile(t, "application/adhar-supply-chain/manifests/70-app-ci.yaml")

	// No step image pins a language runtime version.
	literal := regexp.MustCompile(`(?m)^\s+image:\s*\S*(eclipse-temurin[-:]\d|maven:\d|node:\d|golang:\d|python:\d)`)
	for name, body := range map[string]string{"72-app-ci-stages.yaml": stages, "65-build-by-type.yaml": byType} {
		if m := literal.FindString(body); m != "" {
			t.Errorf("%s pins a toolchain in a step image (%q); the project's own declaration must decide", name, strings.TrimSpace(m))
		}
	}
	for _, want := range []string{
		"maven:$(params.mavenVersion)-eclipse-temurin-$(params.jdkVersion)",
		"eclipse-temurin:$(params.jdkVersion)-jdk",
		"node:$(params.nodeVersion)-bookworm",
		"golang:$(params.goVersion)-bookworm",
		"python:$(params.pythonVersion)-slim",
	} {
		if !strings.Contains(stages, want) {
			t.Errorf("test-auto no longer builds a step image from %q", want)
		}
	}

	// The detection exists and reads the places each ecosystem declares its version.
	if !strings.Contains(stages, "name: detect-toolchain") {
		t.Fatal("the detect-toolchain Task is gone")
	}
	for _, src := range []string{"BP_JVM_VERSION", ".java-version", ".sdkmanrc", ".tool-versions", `maven\.compiler\.release`, "maven-wrapper.properties", ".python-version", ".nvmrc", `"node"`, "go.mod"} {
		if !strings.Contains(stages, src) {
			t.Errorf("detect-toolchain does not read %s", src)
		}
	}

	// The pipeline wires the results into build AND test.
	for _, want := range []string{
		"taskRef: {name: detect-toolchain}",
		`{name: jdkVersion, value: "$(tasks.toolchain.results.jdk)"}`,
		`{name: mavenVersion, value: "$(tasks.toolchain.results.maven)"}`,
		`{name: nodeVersion, value: "$(tasks.toolchain.results.node)"}`,
		`{name: goVersion, value: "$(tasks.toolchain.results.go)"}`,
	} {
		if !strings.Contains(pipeline, want) {
			t.Errorf("app-ci does not carry %s", want)
		}
	}
	// The build reads the detected JDK from toolchain; the test reads it from the
	// build it judges (a pass-through result), so both follow the repository
	// while the pipeline graph stays a line.
	if !strings.Contains(pipeline, `{name: jdkVersion, value: "$(tasks.toolchain.results.jdk)"}`) ||
		!strings.Contains(pipeline, `{name: jdkVersion, value: "$(tasks.build.results.jdk)"}`) {
		t.Error("the detected JDK does not reach both build (from toolchain) and test (from build); both must follow the repository")
	}

	// The buildpack build honours the detected JDK and a wrapper-pinned Maven
	// before falling back to its own default.
	for _, want := range []string{
		`elif [ -n "$(params.jdkVersion)" ]; then`,
		`> /platform/env/BP_MAVEN_VERSION`,
	} {
		if !strings.Contains(build, want) {
			t.Errorf("buildpacks-build does not apply %s", want)
		}
	}
}

// A retried build must start from the checkout.
//
// Tekton retries re-run the Task on the SAME workspace. The first attempt's
// `npm install` leaves node_modules behind and the npm-install buildpack then
// refuses with `mkdir /workspace/source/node_modules: file exists`, so every
// retry fails for a reason unrelated to the first failure — and that is the
// error a person reads (ngapp, 2026-10-15; the real cause, a missing source
// file, was only in attempt 0's log).
func TestARetriedBuildStartsFromTheCheckout(t *testing.T) {
	build := readStackFile(t, "application/adhar-supply-chain/manifests/60-build-pipelines.yaml")
	prepare := strings.Index(build, "- name: prepare")
	buildStep := strings.Index(build, "exec /cnb/lifecycle/creator")
	if prepare < 0 || buildStep < 0 || prepare > buildStep {
		t.Fatal("buildpacks-build no longer has a prepare step before the lifecycle; update this guard")
	}
	if !strings.Contains(build[prepare:buildStep], `rm -rf "$(workspaces.source.path)/$(params.subdirectory)/node_modules"`) {
		t.Error("prepare does not remove node_modules, so a retried build fails on `mkdir node_modules: file exists` instead of its real cause")
	}
}

// The pipeline graph is a line: only the build reads detect-toolchain's results;
// the test takes the versions from the build it judges. A direct reference from
// test to toolchain drew a second edge (the "connection line") in the console.
func TestOnlyTheBuildReadsTheToolchainDirectly(t *testing.T) {
	ci := readStackFile(t, "application/adhar-supply-chain/manifests/70-app-ci.yaml")
	i := strings.Index(ci, "    - name: test\n")
	j := strings.Index(ci[i:], "    - name: secrets\n")
	if i < 0 || j < 0 {
		t.Fatal("the app-ci pipeline no longer has test followed by secrets; update this guard")
	}
	testTask := ci[i : i+j]
	if strings.Contains(testTask, "tasks.toolchain.") {
		t.Error("the test task reads detect-toolchain's results directly; it must read them from the build (tasks.build.results.*)")
	}
	for _, want := range []string{"tasks.build.results.jdk", "tasks.build.results.node", "tasks.build.results.python"} {
		if !strings.Contains(testTask, want) {
			t.Errorf("the test task lacks %s", want)
		}
	}
	tasks := readStackFile(t, "application/adhar-supply-chain/manifests/60-build-pipelines.yaml")
	for _, want := range []string{"{name: jdk, description", "{name: maven, description", "{name: node, description", "{name: go, description", "{name: python, description", `> "$(results.python.path)"`} {
		if !strings.Contains(tasks, want) {
			t.Errorf("buildpacks-build does not pass the toolchain through as a result (%s)", want)
		}
	}
}

// Every build's image carries the time it was BUILT: the lifecycle otherwise
// stamps a fixed 1980 timestamp, and the Warehouse orders builds by it, so a
// second build of an app never became freight.
func TestBuildsCarryTheirOwnCreationTime(t *testing.T) {
	build := readStackFile(t, "application/adhar-supply-chain/manifests/60-build-pipelines.yaml")
	if !strings.Contains(build, `SOURCE_DATE_EPOCH="$(date +%s)" exec /cnb/lifecycle/creator`) {
		t.Error("the buildpacks lifecycle runs without SOURCE_DATE_EPOCH; every image is dated 1980 and Kargo cannot order builds")
	}
	comp := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	if !strings.Contains(comp, "imageSelectionStrategy: {{ $strategy }}") || !strings.Contains(comp, `default "NewestBuild"`) {
		t.Error("the Warehouse no longer selects by build time; the SOURCE_DATE_EPOCH reasoning above needs revisiting")
	}
}

// A Tekton Task or Pipeline whose results or params carry a key Tekton does not
// know is rejected by its webhook, and Argo CD then cannot even COMPARE the
// supply-chain app — every change in it stops reaching the cluster. An unquoted
// comma inside a flow mapping (`description: a, b`) did exactly that.
func TestCiTasksDecodeWithoutUnknownFields(t *testing.T) {
	for _, f := range []string{"60-build-pipelines.yaml", "65-build-by-type.yaml", "70-app-ci.yaml", "72-app-ci-stages.yaml"} {
		docs := strings.Split(readStackFile(t, "application/adhar-supply-chain/manifests/"+f), "\n---")
		for _, doc := range docs {
			var obj struct {
				Kind     string `json:"kind"`
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Params  []map[string]interface{} `json:"params"`
					Results []map[string]interface{} `json:"results"`
				} `json:"spec"`
			}
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || (obj.Kind != "Task" && obj.Kind != "Pipeline") {
				continue
			}
			allowed := map[string]bool{"name": true, "description": true, "type": true, "default": true, "properties": true, "enum": true, "value": true}
			for _, fields := range append(obj.Spec.Params, obj.Spec.Results...) {
				for k := range fields {
					if !allowed[k] {
						t.Errorf("%s %s/%s has a param/result with unknown field %q; Tekton's webhook rejects it and Argo CD stops syncing the package", f, obj.Kind, obj.Metadata.Name, k)
					}
				}
			}
		}
	}
}
