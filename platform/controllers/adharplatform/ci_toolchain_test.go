package adharplatform

import (
	"regexp"
	"strings"
	"testing"
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
	if strings.Count(pipeline, `value: "$(tasks.toolchain.results.jdk)"`) < 2 {
		t.Error("the detected JDK reaches only one of build/test; both must follow the repository")
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
	buildStep := strings.Index(build, "command: [\"/cnb/lifecycle/creator\"]")
	if prepare < 0 || buildStep < 0 || prepare > buildStep {
		t.Fatal("buildpacks-build no longer has a prepare step before the lifecycle; update this guard")
	}
	if !strings.Contains(build[prepare:buildStep], `rm -rf "$(workspaces.source.path)/$(params.subdirectory)/node_modules"`) {
		t.Error("prepare does not remove node_modules, so a retried build fails on `mkdir node_modules: file exists` instead of its real cause")
	}
}
