package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Pipeline stability invariants for the paved-road CI.
//
// These exist because the Tekton pipelines shipped with NO stability primitives
// at all — `retries` appeared zero times across 12 pipelines and 56 tasks, and
// no Trivy invocation carried a `--timeout`. The result, measured on a live GCP
// cluster on 2026-10-03:
//
//   - `release-adhar-ui` failed twice, once at `scan` with
//     "semaphore acquire: context deadline exceeded" — Trivy had spent 4m20s of
//     its 5-minute DEFAULT timeout downloading the 120 MiB vulnerability DB,
//     leaving ~40s to walk the tree — and once at `build` on a single dropped
//     TCP read while fetching an npm package. A rerun of the same revision
//     passed, which is the signature of flakiness rather than a broken build.
//   - `app-ci-angular-app` then failed with a 404 for `@adhar-ui/angular`,
//     because the release that publishes it had never got as far as publishing.
//     One dropped read took out an unrelated application pipeline.
//
// A fix that is only applied once rots. These assert the policy instead.

// pipelineDirs are the packages that own the paved-road pipelines.
func pipelineDirs(t *testing.T) []string {
	t.Helper()
	root := filepath.Join(stackRoot(t), "packages", "application")
	return []string{
		filepath.Join(root, "adhar-supply-chain", "manifests"),
		filepath.Join(root, "adhar-libraries", "manifests"),
	}
}

func yamlFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return out
}

// Every Trivy invocation must bound itself explicitly.
//
// Trivy's default is 5 MINUTES TOTAL, and that budget has to cover downloading
// the vulnerability database before any scanning starts. Whether a run passes
// therefore depends on how fast the DB downloads, which is not a property of
// the code under test. `--timeout` is the difference between a scan that is slow
// and a pipeline that is unreliable.
func TestEveryTrivyScanSetsAnExplicitTimeout(t *testing.T) {
	// `trivy convert` reads a report off disk — no network, no DB, nothing to
	// bound. Every other subcommand walks a tree or pulls an image.
	invocation := regexp.MustCompile(`\btrivy\s+(fs|image|repo|config|rootfs|filesystem)\b`)

	for _, dir := range pipelineDirs(t) {
		for _, path := range yamlFilesIn(t, dir) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			lines := strings.Split(string(b), "\n")
			for i, l := range lines {
				if strings.HasPrefix(strings.TrimSpace(l), "#") || !invocation.MatchString(l) {
					continue
				}
				// A shell invocation may continue over several lines with `\`.
				cmd := l
				for j := i; j < len(lines)-1 && strings.HasSuffix(strings.TrimRight(cmd, " "), "\\"); j++ {
					cmd += lines[j+1]
				}
				if !strings.Contains(cmd, "--timeout") && !strings.Contains(cmd, "TRIVY_TIMEOUT") {
					t.Errorf("%s:%d: trivy invocation without an explicit --timeout; the 5m default "+
						"is consumed by the vulnerability-DB download and the scan then dies with "+
						"'context deadline exceeded'\n\t%s",
						filepath.Base(path), i+1, strings.TrimSpace(l))
				}
			}
		}
	}
}

// retryPolicy is the line between infrastructure and judgement.
//
// A task that fails because a TCP read dropped, a registry 503'd or a 120 MiB
// database came down slowly should be retried: nothing about the code changed.
// A task that renders a VERDICT about the code must fail on its first attempt —
// retrying tests until they pass is how a flaky test becomes permanent, and it
// is the one "fix" that makes a pipeline less trustworthy rather than more.
var (
	retryableTasks = map[string]bool{
		"clone": true, "build": true, "scan": true, "source-scan": true,
		"sbom": true, "sign": true, "attest": true, "verify": true,
		"publish": true, "deploy": true, "scaffold": true, "assemble": true,
	}
	// scan-gate renders the scan verdict and may hold a reviewer's answer;
	// a retry would re-open a decided review.
	verdictTasks = map[string]bool{"test": true, "lint": true, "secrets": true, "scan-gate": true}
)

// Network-bound pipeline tasks carry `retries`; verdict tasks never do.
//
// Parsed by hand rather than with a YAML decoder on purpose: these files are
// heavily commented and the comments are load-bearing documentation, so the
// fixtures are read as text everywhere else in this suite too.
func TestPipelineTasksFollowTheRetryPolicy(t *testing.T) {
	taskName := regexp.MustCompile(`^    - name: (\S+)\s*$`)

	for _, dir := range pipelineDirs(t) {
		for _, path := range yamlFilesIn(t, dir) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			lines := strings.Split(string(b), "\n")
			inTasks := false
			for i, l := range lines {
				if l == "  tasks:" {
					inTasks = true
					continue
				}
				// Any other key at indent 2 closes the block — `finally:`,
				// `workspaces:`, `params:`. Without this the `- name:` entries
				// under those sections look exactly like pipeline tasks.
				if inTasks && strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") &&
					strings.HasSuffix(strings.TrimRight(l, " "), ":") {
					inTasks = false
				}
				if !inTasks {
					continue
				}
				m := taskName.FindStringSubmatch(l)
				if m == nil {
					continue
				}
				name := m[1]
				// `retries` sits among the task's own keys, which start on the
				// next line at indent 6 and end at the next task or block.
				hasRetries := false
				for j := i + 1; j < len(lines); j++ {
					if strings.HasPrefix(lines[j], "    - ") || !strings.HasPrefix(lines[j], "      ") {
						break
					}
					if strings.HasPrefix(strings.TrimSpace(lines[j]), "retries:") {
						hasRetries = true
						break
					}
				}
				switch {
				case verdictTasks[name] && hasRetries:
					t.Errorf("%s:%d: task %q renders a verdict about the code and must NOT be retried — "+
						"retrying a failing test until it passes hides the defect",
						filepath.Base(path), i+1, name)
				case retryableTasks[name] && !hasRetries:
					t.Errorf("%s:%d: task %q is network-bound and needs `retries` — a dropped read, a "+
						"registry 503 or a slow vulnerability-DB download should not fail a release",
						filepath.Base(path), i+1, name)
				}
			}
		}
	}
}

// The dependency fetch in the Deno release is retried in-step.
//
// `release-adhar-ui` died on `Failed caching npm package '@esbuild/linux-x64'
// … error reading a body from connection`. The Maven release never had that
// class of failure because it already passed
// `-Daether.connector.http.retryHandler.count=5`; the Deno path had no
// equivalent, so the two library pipelines had visibly different reliability on
// the same cluster. The in-step retry recovers in seconds where a Tekton task
// retry restarts the whole stage.
func TestDenoDependencyFetchIsRetried(t *testing.T) {
	path := filepath.Join(stackRoot(t), "packages", "application", "adhar-libraries", "manifests", "npm-release.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	s := string(b)
	if !strings.Contains(s, "until deno install; do") {
		t.Error("npm-release.yaml must retry `deno install`: one dropped read during dependency " +
			"resolution otherwise fails the whole release and then 404s every app that depends on it")
	}
	// The BUILD must not be swept into the same retry: a compile error is a
	// verdict, and retrying it three times only delays the report.
	if strings.Contains(s, "until deno $(params.command)") {
		t.Error("the build command must not be retried — only the dependency fetch is transient")
	}
}

// Platform npm scopes resolve from the platform's registry, not from whatever an
// application's own .npmrc happens to name.
//
// `app-ci` for a scaffolded Angular app failed with a 404 for
// `@adhar-ui/angular` against Gitea's package registry, while the library
// release pipeline publishes to Nexus (`repository/npm-hosted/`) — where the
// package demonstrably was. The app's .npmrc even asserted in a comment that the
// pipeline published to Gitea, which was never true, and every app scaffolded
// from that template inherited the same dead pointer. A registry hostname is a
// property of the platform, so the build now sets it.
func TestBuildpacksBuildRoutesPlatformScopesToTheClusterRegistry(t *testing.T) {
	path := filepath.Join(stackRoot(t), "packages", "application", "adhar-supply-chain",
		"manifests", "60-build-pipelines.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	s := string(b)

	for _, want := range []string{
		// The scopes are configurable, not hardcoded in the script.
		"name: platformNpmScopes",
		// Sourced from the same Secret the release pipeline publishes with, so
		// the two cannot point at different registries.
		"key: npm-hosted",
		// Existing mappings are deleted before ours are appended, so the result
		// does not depend on npm's last-key-wins parsing.
		`sed -i "\|^${scope}:registry=|d" "$NPMRC"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("buildpacks-build is missing the platform npm registry injection (%q)", want)
		}
	}

	// A cluster without the nexus package must still build: every secret ref is
	// optional and the step skips when the URL is unset.
	if strings.Count(s, "optional: true") < 3 {
		t.Error("the nexus-credentials refs must be optional — a profile without nexus must still build")
	}
	if !strings.Contains(s, `if [ -z "${NEXUS_NPM:-}" ]; then`) {
		t.Error("the injection must no-op when nexus-credentials is absent rather than fail the build")
	}
	// always-auth would send Nexus credentials to npmjs for every public package.
	// Matched as an ASSIGNMENT, not as the bare word: the script carries a comment
	// explaining why the option is absent, and a substring check on "always-auth"
	// fails on that comment — a test that cannot pass however correct the code is.
	if strings.Contains(s, "always-auth=") {
		t.Error("always-auth must not be set: it would leak the registry credential to npmjs")
	}
}

// The Maven half of the same dead pointer, which fails for two reasons at once.
//
// `app-ci` for a scaffolded Java service failed with
//
//	[FATAL] Non-resolvable parent POM ... com.adhar.kit:adhar-kit-parent:pom:0.1.0
//	(absent): Could not transfer artifact from/to maven-default-http-blocker
//	(http://0.0.0.0/): Blocked mirror for repositories: [gitea-adhar (http://...)]
//
// and neither cause was the artifact being missing:
//
//   - the template's settings.xml resolves from GITEA's Maven registry, which
//     holds nothing (an empty package list, a 404 for the POM), while the
//     release pipeline deploys to NEXUS, where that exact POM answers 200;
//   - Maven 3.8 ships a global mirror, `maven-default-http-blocker`, matching
//     `external:http:*`. Every adhar-controlplane registry is plain HTTP on a cluster-DNS
//     name, so Maven refuses to contact it no matter where the URL points.
//
// Both have to be handled, and the settings path has to be overridden AFTER the
// project.toml translation or a template naming its own settings.xml wins and
// the build is back on the dead pointer.
func TestBuildpacksBuildRoutesMavenAtTheClusterRegistry(t *testing.T) {
	path := filepath.Join(stackRoot(t), "packages", "application", "adhar-supply-chain",
		"manifests", "60-build-pipelines.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	s := string(b)

	// Sourced from the same Secret the release pipeline publishes with, so the
	// two cannot drift apart.
	if !strings.Contains(s, "key: maven-public") {
		t.Error("the build does not read the Nexus maven-public URL from nexus-credentials; without it " +
			"a Java app keeps resolving from whatever its template hardcoded")
	}
	// Maven's default HTTP blocker must be lifted by redeclaring its id.
	if !strings.Contains(s, "<id>maven-default-http-blocker</id>") {
		t.Error("the generated Maven settings do not redeclare maven-default-http-blocker; Maven 3.8+ " +
			"blocks every plain-HTTP repository, which is every adhar-controlplane registry, so the build " +
			"cannot reach Nexus however correct the URL is")
	}
	if !strings.Contains(s, "<mirrorOf>dummy</mirrorOf>") {
		t.Error("the blocker override must use a mirrorOf that matches no repository; anything else " +
			"either leaves the blocker active or redirects real traffic to http://0.0.0.0/")
	}
	// And the platform mirror must actually be a mirror of everything.
	if !strings.Contains(s, "<id>nexus-public</id><mirrorOf>*</mirrorOf>") {
		t.Error("the platform Maven mirror is not a mirrorOf *; the app's own repository declarations " +
			"would then still be consulted, and the one the templates declare is the empty Gitea registry")
	}
	// A cluster with no nexus package must still build.
	if !strings.Contains(s, `if [ -z "${NEXUS_MAVEN_PUBLIC:-}" ]; then`) {
		t.Error("the Maven injection must no-op when nexus-credentials is absent rather than fail the build")
	}
	// It must not clobber an application's own settings.xml.
	if strings.Contains(s, `> "${APPDIR_MVN}/settings.xml"`) {
		t.Error("the platform writes over the application's settings.xml; write a separate file and " +
			"point the buildpack at it, so nothing the application wrote is destroyed")
	}

	// ORDERING: the settings-path override has to come after the project.toml
	// translation, or a BP_MAVEN_SETTINGS_PATH the repository declared wins and
	// Maven reads the file with the dead pointer in it.
	translate := strings.Index(s, "translating $PROJ into /platform/env")
	override := strings.Index(s, "/platform/env/BP_MAVEN_SETTINGS_PATH")
	if translate < 0 || override < 0 {
		t.Fatalf("cannot locate both the project.toml translation (%d) and the settings-path override (%d); "+
			"this guard is no longer checking their order", translate, override)
	}
	if override < translate {
		t.Error("BP_MAVEN_SETTINGS_PATH is written BEFORE project.toml is translated, so a template that " +
			"names its own settings.xml overwrites the platform's pointer and the build resolves from " +
			"the empty Gitea registry again")
	}
}

// Resolving the parent POM is only half of it: the platform's Java libraries
// require a newer JDK than the buildpack's default.
//
// adhar-kit's parent POM enforces RequireJavaVersion [25,), and the buildpack
// provisions 21, so once the registry was fixed the very next failure was
//
//	Rule 1: RequireJavaVersion failed with message: Detected JDK version
//	21.0.12 (JAVA_HOME=/layers/paketo-buildpacks_bellsoft-liberica/jdk) is not
//	in the allowed range [25,).
//
// The library release pipeline already builds on temurin-25 for this reason; an
// application consuming those libraries needs the same floor.
//
// Unlike the registry, the JVM version IS legitimately the application's
// choice, so this must be a DEFAULT and not an override — a repository that
// declared BP_JVM_VERSION keeps it.
func TestBuildpacksBuildDefaultsTheJvmForMavenProjects(t *testing.T) {
	path := filepath.Join(stackRoot(t), "packages", "application", "adhar-supply-chain",
		"manifests", "60-build-pipelines.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	s := string(b)

	if !strings.Contains(s, "name: defaultJvmVersion") {
		t.Fatal("buildpacks-build has no defaultJvmVersion parameter; a Maven build then gets the " +
			"buildpack's JDK 21 and fails maven-enforcer against adhar-kit's [25,) floor")
	}
	if !strings.Contains(s, "/platform/env/BP_JVM_VERSION") {
		t.Error("the build never writes BP_JVM_VERSION, so the buildpack default stands and every " +
			"service built on the platform's Java libraries fails in maven-enforcer")
	}
	// It must only fill in when the application said nothing.
	if !strings.Contains(s, "if [ -s /platform/env/BP_JVM_VERSION ]; then") {
		t.Error("BP_JVM_VERSION is written unconditionally; the JVM version is the application's choice, " +
			"so a project.toml that declares one must win")
	}
	// And it must be decided after the translation, or it cannot see what the
	// application declared.
	translate := strings.Index(s, "translating $PROJ into /platform/env")
	jvm := strings.Index(s, "/platform/env/BP_JVM_VERSION")
	if translate < 0 || jvm < 0 {
		t.Fatalf("cannot locate both the translation (%d) and the JVM default (%d)", translate, jvm)
	}
	if jvm < translate {
		t.Error("BP_JVM_VERSION is decided BEFORE project.toml is translated, so the check for an " +
			"application-declared value always sees nothing and the platform default always wins")
	}
}
