package adharplatform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The image ships the runtime its tests ran on.
//
// detect-toolchain reads the repository's JDK, Node, Go and Python, and the test
// stage runs on exactly those (node:<node>, golang:<go>, python:<python>,
// maven:…-temurin-<jdk>). The buildpacks did not: node-engine fell back to its
// own default when nothing told it otherwise, and go-dist and cpython read only
// their BP_* variable, never go.mod or .python-version. test-func's tests ran on
// Node 22 while its image shipped Node 24 (2026-10-25).
//
// These tests RUN the shell the Tasks run — detect-toolchain's `detect` step,
// buildpacks-build's `prepare` and `runtime` steps — against fixture
// repositories and a builder's buildpack.toml layout, with Tekton's variables
// substituted the way Tekton does, so they pin behaviour rather than spelling.

type tektonStep struct {
	Name            string         `json:"name"`
	Image           string         `json:"image"`
	Script          string         `json:"script"`
	SecurityContext map[string]any `json:"securityContext"`
}

type tektonTask struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Params []struct {
			Name    string `json:"name"`
			Default any    `json:"default"`
		} `json:"params"`
		Steps []tektonStep `json:"steps"`
	} `json:"spec"`
}

func supplyChainTask(t *testing.T, file, name string) tektonTask {
	t.Helper()
	for _, doc := range strings.Split(readStackFile(t, "application/adhar-supply-chain/manifests/"+file), "\n---") {
		var task tektonTask
		if yaml.Unmarshal([]byte(doc), &task) == nil && task.Kind == "Task" && task.Metadata.Name == name {
			return task
		}
	}
	t.Fatalf("%s has no Task %s", file, name)
	return tektonTask{}
}

func (tt tektonTask) step(t *testing.T, name string) tektonStep {
	t.Helper()
	for _, s := range tt.Spec.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("Task %s has no step %q", tt.Metadata.Name, name)
	return tektonStep{}
}

func (tt tektonTask) paramDefault(name string) string {
	for _, p := range tt.Spec.Params {
		if p.Name == name {
			return fmt.Sprint(p.Default)
		}
	}
	return ""
}

// modernBash finds a bash new enough for the Tasks' scripts (they run on
// bash:5.2); macOS's /bin/bash is 3.2.
func modernBash(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"bash", "/usr/local/bin/bash", "/opt/homebrew/bin/bash", "/bin/bash"} {
		p, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		out, err := exec.Command(p, "-c", "echo ${BASH_VERSINFO[0]}").Output()
		if major, aerr := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && aerr == nil && major >= 4 {
			return p
		}
	}
	t.Skip("no bash >= 4 on PATH; the Tasks' scripts need one")
	return ""
}

var tektonResultRef = regexp.MustCompile(`\$\(results\.([a-z0-9-]+)\.path\)`)

// substitute replaces Tekton variables (and the absolute paths the steps use)
// the way Tekton would for one TaskRun.
func substitute(script string, vars map[string]string, results, platform, cnb string) string {
	for k, v := range vars {
		script = strings.ReplaceAll(script, "$("+k+")", v)
	}
	script = tektonResultRef.ReplaceAllString(script, results+"/$1")
	script = strings.ReplaceAll(script, "/cnb/buildpacks", cnb)
	return strings.ReplaceAll(script, "/platform", platform)
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func projectTOML(env map[string]string) string {
	s := "[project]\nid = \"io.adhar.app\"\nname = \"app\"\nversion = \"0.1.0\"\n"
	for k, v := range env {
		s += fmt.Sprintf("\n[[build.env]]\nname = %q\nvalue = %q\n", k, v)
	}
	return s
}

func readResult(t *testing.T, dir, name string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return string(b)
}

// detect-toolchain reads each runtime from the buildpack's own contract
// (project.toml [[build.env]]) before the editor-level files, as it always did
// for BP_JVM_VERSION, so the tests run on what the image is built with; and a
// repository that declares no Node gets 24, node-engine's own default and the
// first Node line whose bundled npm passes the image scan.
func TestDetectToolchainReadsTheBuildpackContractFirst(t *testing.T) {
	bash := modernBash(t)
	task := supplyChainTask(t, "72-app-ci-stages.yaml", "detect-toolchain")
	if got := task.paramDefault("defaultNode"); got != "24" {
		t.Errorf("detect-toolchain's defaultNode is %q, want 24: the build ships the tested Node now, and Node 22's "+
			"bundled npm carries HIGH findings the scan gate does not excuse", got)
	}
	script := task.step(t, "detect").Script
	defaults := map[string]string{}
	for _, p := range task.Spec.Params {
		defaults["params."+p.Name] = fmt.Sprint(p.Default)
	}
	for _, c := range []struct {
		name  string
		files map[string]string
		want  map[string]string
	}{
		{"declares nothing", map[string]string{"README.md": "x"},
			map[string]string{"node": "24", "go": "1.24", "python": "3.12", "jdk": "25"}},
		{"project.toml outranks engines", map[string]string{
			"project.toml": projectTOML(map[string]string{"BP_NODE_VERSION": "24.*"}),
			"package.json": `{"name":"x","engines":{"node":"22.x"}}`},
			map[string]string{"node": "24"}},
		{"engines when project.toml is silent", map[string]string{"package.json": `{"name":"x","engines":{"node":">=20"}}`},
			map[string]string{"node": "20"}},
		{"project.toml outranks go.mod", map[string]string{
			"project.toml": projectTOML(map[string]string{"BP_GO_VERSION": "1.27.*"}),
			"go.mod":       "module x\n\ngo 1.25.0\n"},
			map[string]string{"go": "1.27"}},
		{"go.mod when project.toml is silent", map[string]string{"go.mod": "module x\n\ngo 1.25.0\n"},
			map[string]string{"go": "1.25"}},
		{"project.toml outranks .python-version", map[string]string{
			"project.toml":    projectTOML(map[string]string{"BP_CPYTHON_VERSION": "3.13.x"}),
			".python-version": "3.11\n", "requirements.txt": "flask\n"},
			map[string]string{"python": "3.13"}},
		{"project.toml JDK", map[string]string{"project.toml": projectTOML(map[string]string{"BP_JVM_VERSION": "21"}), "pom.xml": "<project/>"},
			map[string]string{"jdk": "21"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			src, results := t.TempDir(), t.TempDir()
			writeFiles(t, src, c.files)
			s := substitute(script, defaults, results, "/nonexistent-platform", "/nonexistent-cnb")
			cmd := exec.Command(bash, "-c", s)
			cmd.Dir = src
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("detect failed: %v\n%s", err, out)
			}
			for k, v := range c.want {
				if got := readResult(t, results, k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
		})
	}
}

// buildpacks-build hands the buildpacks the runtime detect-toolchain chose — as a
// DEFAULT a repository's project.toml overrides — and its `runtime` step drops a
// version the builder does not ship (go.mod's `go 1.25.0` is a minimum; go-dist
// 2.11.0 ships 1.26 and 1.27) instead of failing the build, rewriting the result
// the test stage reads so tests and image still agree.
func TestTheImageShipsTheRuntimeTheTestsRanOn(t *testing.T) {
	bash := modernBash(t)
	task := supplyChainTask(t, "60-build-pipelines.yaml", "buildpacks-build")
	var order []string
	for _, s := range task.Spec.Steps {
		order = append(order, s.Name)
	}
	if strings.Join(order, ",") != "toolchain,prepare,runtime,build" {
		t.Fatalf("buildpacks-build steps are %v, want toolchain, prepare, runtime, build: the runtime check must run "+
			"after prepare chose the versions and before the lifecycle reads them", order)
	}
	runtime := task.step(t, "runtime")
	if runtime.Image != "$(params.builder)" || fmt.Sprint(runtime.SecurityContext["runAsUser"]) != "0" {
		t.Errorf("the runtime step must run in the builder (it reads the buildpacks' buildpack.toml) as root (the "+
			"toolchain step wrote the results): image %q, securityContext %v", runtime.Image, runtime.SecurityContext)
	}
	prepare := task.step(t, "prepare").Script

	// A builder laid out like paketobuildpacks/builder-jammy-base (2026-10): the
	// packaged buildpack.toml of each runtime buildpack, indented as shipped.
	cnb := t.TempDir()
	bp := func(id, version, key, def string, versions ...string) {
		s := fmt.Sprintf("api = \"0.7\"\n\n[buildpack]\n  id = \"paketo-buildpacks/%s\"\n  version = %q\n\n[metadata]\n  [metadata.default-versions]\n    %s = %q\n", id, version, key, def)
		for _, v := range versions {
			s += fmt.Sprintf("\n  [[metadata.dependencies]]\n    id = %q\n    stacks = [\"*\"]\n    version = %q\n", key, v)
		}
		writeFiles(t, cnb, map[string]string{filepath.Join("paketo-buildpacks_"+id, version, "buildpack.toml"): s})
	}
	bp("node-engine", "8.11.0", "node", "24.*.*", "20.20.2", "22.23.4", "24.22.1", "26.12.1")
	bp("go-dist", "2.11.0", "go", "1.26.*", "1.26.10", "1.27.3")
	bp("cpython", "1.18.49", "python", "3.11.x", "3.11.17", "3.12.15", "3.13.16")

	for _, c := range []struct {
		name       string
		files      map[string]string
		toolchain  map[string]string // detect-toolchain's results, handed in as params
		wantEnv    map[string]string // /platform/env after both steps ("" = absent)
		wantResult map[string]string // the results the test stage reads
	}{
		{"node, nothing declared: the tested default", map[string]string{"package.json": `{"name":"x"}`},
			map[string]string{"node": "24"},
			map[string]string{"BP_NODE_VERSION": "24"}, map[string]string{"node": "24"}},
		{"node, engines declared: node-engine's own reading stands", map[string]string{"package.json": `{"name":"x","engines":{"node":">=20"}}`},
			map[string]string{"node": "20"},
			map[string]string{"BP_NODE_VERSION": ""}, map[string]string{"node": "20"}},
		{"node, project.toml declares: kept", map[string]string{"package.json": `{"name":"x"}`,
			"project.toml": projectTOML(map[string]string{"BP_NODE_VERSION": "26"})},
			map[string]string{"node": "26"},
			map[string]string{"BP_NODE_VERSION": "26"}, map[string]string{"node": "26"}},
		{"go, shipped minor", map[string]string{"go.mod": "module x\n\ngo 1.27.0\n"},
			map[string]string{"go": "1.27"},
			map[string]string{"BP_GO_VERSION": "1.27"}, map[string]string{"go": "1.27"}},
		{"go, minor the builder does not ship: its default, and the tests follow", map[string]string{"go.mod": "module x\n\ngo 1.25.0\n"},
			map[string]string{"go": "1.25"},
			map[string]string{"BP_GO_VERSION": ""}, map[string]string{"go": "1.26"}},
		{"python, declared", map[string]string{"requirements.txt": "flask\n", ".python-version": "3.13\n"},
			map[string]string{"python": "3.13"},
			map[string]string{"BP_CPYTHON_VERSION": "3.13"}, map[string]string{"python": "3.13"}},
		{"not a Python repository: no BP_CPYTHON_VERSION", map[string]string{"package.json": `{"name":"x"}`},
			map[string]string{"node": "24", "python": "3.12"},
			map[string]string{"BP_CPYTHON_VERSION": "", "BP_GO_VERSION": ""}, map[string]string{"python": "3.12"}},
		{"JVM without the platform's Nexus still gets the tested JDK", map[string]string{"build.gradle": ""},
			map[string]string{"jdk": "21"},
			map[string]string{"BP_JVM_VERSION": "21"}, map[string]string{"jdk": "21"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			src, results, platform := t.TempDir(), t.TempDir(), t.TempDir()
			writeFiles(t, src, c.files)
			tc := map[string]string{"jdk": "25", "maven": "3.9", "node": "24", "go": "1.24", "python": "3.12"}
			for k, v := range c.toolchain {
				tc[k] = v
			}
			for k, v := range tc {
				writeFiles(t, results, map[string]string{k: v})
			}
			vars := map[string]string{
				"workspaces.source.path": src, "params.subdirectory": "",
				"params.jdkVersion": tc["jdk"], "params.mavenVersion": tc["maven"], "params.nodeVersion": tc["node"],
				"params.goVersion": tc["go"], "params.pythonVersion": tc["python"],
				"params.defaultJvmVersion": task.paramDefault("defaultJvmVersion"),
				"params.platformNpmScopes": task.paramDefault("platformNpmScopes"),
			}
			// The steps run as root in the cluster; chown is not the subject here.
			for _, s := range []string{"chown() { :; }\n" + substitute(prepare, vars, results, platform, cnb),
				substitute(runtime.Script, vars, results, platform, cnb)} {
				if out, err := exec.Command(bash, "-c", s).CombinedOutput(); err != nil {
					t.Fatalf("step failed: %v\n%s", err, out)
				}
			}
			for k, v := range c.wantEnv {
				if got := readResult(t, filepath.Join(platform, "env"), k); got != v {
					t.Errorf("/platform/env/%s = %q, want %q", k, got, v)
				}
			}
			for k, v := range c.wantResult {
				if got := readResult(t, results, k); got != v {
					t.Errorf("result %s = %q, want %q (the test stage would run on a different runtime than the image)", k, got, v)
				}
			}
		})
	}
}
