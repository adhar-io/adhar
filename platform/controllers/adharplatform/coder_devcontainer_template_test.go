package adharplatform

// The `devcontainer` Coder template (application/coder/manifests/
// devcontainer-template.yaml) is the platform's half of the development
// container standard the templates repository pins (its tests/
// devcontainer_test.ts): a workspace opened on a repository is built from that
// repository's .devcontainer, gets the extensions in .vscode/extensions.json
// and the IntelliJ plugins in .idea/externalDependencies.xml, and runs the
// devcontainer's postCreateCommand. These guards hold the seams that
// nothing else checks before a cluster does:
//
//   - the seed names `data "coder_external_auth" "gitea"`, which Coder refuses
//     to push until oidc-reload (wave 13) restarts it with the provider — so
//     the seed runs in a LATER wave and still carries the no-provider fallback;
//   - the Console creates a repository's workspace from the template named
//     `devcontainer` with `git_repo` set and opens /home/coder/<repo>;
//   - the builder image is pinned to a release, never `latest`.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const coderDevcontainerManifest = "application/coder/manifests/devcontainer-template.yaml"

// coderDevcontainerTemplate returns main.tf and the seed's run.sh from the
// tracked ConfigMaps, plus the seed Job's PostSync wave.
func coderDevcontainerTemplate(t *testing.T) (mainTF, runSH string, wave int) {
	t.Helper()
	wave = -1
	for _, d := range manifestDocs(readStackFile(t, coderDevcontainerManifest)) {
		switch {
		case d["kind"] == "ConfigMap" && toString(metaName(d)) == "coder-devcontainer-template":
			mainTF = toString(nestedMap(d, "data")["main.tf"])
		case d["kind"] == "ConfigMap" && toString(metaName(d)) == "coder-devcontainer-template-seed":
			runSH = toString(nestedMap(d, "data")["run.sh"])
		case d["kind"] == "Job" && toString(metaName(d)) == "coder-devcontainer-template-seed":
			ann := nestedMap(d, "metadata", "annotations")
			if ann["argocd.argoproj.io/hook"] != "PostSync" {
				t.Errorf("%s: the seed Job must be a PostSync hook, as the kubernetes template's seed is", coderDevcontainerManifest)
			}
			w, err := strconv.Atoi(toString(ann["argocd.argoproj.io/sync-wave"]))
			if err != nil {
				t.Fatalf("%s: the seed Job carries no sync-wave", coderDevcontainerManifest)
			}
			wave = w
		}
	}
	if mainTF == "" || runSH == "" || wave < 0 {
		t.Fatalf("%s: expected ConfigMaps coder-devcontainer-template (main.tf) and coder-devcontainer-template-seed (run.sh) and the seed Job", coderDevcontainerManifest)
	}
	return mainTF, runSH, wave
}

func hookWave(t *testing.T, manifest, job string) int {
	t.Helper()
	for _, d := range manifestDocs(readStackFile(t, manifest)) {
		if d["kind"] == "Job" && toString(metaName(d)) == job {
			w, err := strconv.Atoi(toString(nestedMap(d, "metadata", "annotations")["argocd.argoproj.io/sync-wave"]))
			if err != nil {
				t.Fatalf("%s: Job %s carries no sync-wave", manifest, job)
			}
			return w
		}
	}
	t.Fatalf("%s: Job %s not found", manifest, job)
	return 0
}

// Coder refuses `templates push` for a template naming an external auth
// provider it has not loaded, and it loads the Gitea provider only when
// oidc-reload restarts it (templates-seed.yaml learned this the hard way on
// 2026-10-28). The devcontainer template needs the provider — the clone
// happens in an init container, before any agent's GIT_ASKPASS exists — so its
// seed must run after that restart AND cope with a platform where the provider
// never arrives.
func TestDevcontainerSeedRunsAfterCoderLoadsItsGiteaProvider(t *testing.T) {
	mainTF, runSH, wave := coderDevcontainerTemplate(t)
	reload := hookWave(t, coderReloadManifest, "coder-oidc-reload")
	if wave <= reload {
		t.Errorf("coder-devcontainer-template-seed runs at wave %d, coder-oidc-reload at %d: the seed names the gitea provider, so it must run after the reload loads it", wave, reload)
	}

	tf := withoutComments(mainTF)
	if !strings.Contains(tf, `data "coder_external_auth" "gitea"`) {
		t.Fatal("main.tf declares no coder_external_auth gitea data source; the init container's clone has no token for private repositories")
	}
	if !regexp.MustCompile(`data "coder_external_auth" "gitea" \{[^}]*optional\s*=\s*true`).MatchString(tf) {
		t.Error("the gitea external auth must be optional = true, or every workspace build fails until its owner has linked Gitea")
	}
	if !strings.Contains(tf, "data.coder_external_auth.gitea.access_token") {
		t.Error("the token is never handed to the init container (GIT_TOKEN)")
	}
	// The fallback the seed applies with sed must find its markers.
	for _, marker := range []string{"# adhar:gitea-auth:begin", "# adhar:gitea-auth:end"} {
		if !strings.Contains(mainTF, marker) {
			t.Errorf("main.tf lacks the %q marker the seed's no-provider fallback deletes between", marker)
		}
	}
	sh := withoutComments(runSH)
	if !strings.Contains(sh, "not configured") || !strings.Contains(sh, "adhar:gitea-auth:begin") || !strings.Contains(sh, "-no-gitea-auth") {
		t.Error("run.sh must push a provider-less copy (stamped -no-gitea-auth) when Coder answers that the gitea provider is not configured")
	}
}

// The Console (modules/develop/src/data/coder.ts, ensureRepoWorkspace)
// creates a repository's workspace from the template named `devcontainer`,
// passing the clone URL as `git_repo`, and opens /home/coder/<repo> in both
// IDEs (repoFolder; coder-ide.ts accepts only that folder shape).
func TestDevcontainerTemplateKeepsTheConsoleContract(t *testing.T) {
	mainTF, _, _ := coderDevcontainerTemplate(t)
	tf := withoutComments(mainTF)
	for _, d := range manifestDocs(readStackFile(t, coderDevcontainerManifest)) {
		if d["kind"] != "Job" {
			continue
		}
		for _, c := range sliceOfMaps(nestedMap(d, "spec", "template", "spec")["containers"]) {
			for _, e := range sliceOfMaps(c["env"]) {
				if e["name"] == "TEMPLATE_NAME" && e["value"] != "devcontainer" {
					t.Errorf("the seed pushes the template as %q; the Console looks for `devcontainer`", e["value"])
				}
			}
		}
	}
	if !regexp.MustCompile(`data "coder_parameter" "git_repo" \{[^}]*name\s*=\s*"git_repo"`).MatchString(tf) {
		t.Error("main.tf has no `git_repo` parameter; the Console passes the clone URL under that name")
	}
	if !strings.Contains(tf, `"/home/coder/${local.repo_name}"`) {
		t.Error("the checkout must be /home/coder/<repo>: the Console opens exactly that folder")
	}
	for _, slug := range []string{`slug         = "code-server"`, `slug         = "jetbrains-gateway"`, `slug         = "vscode-desktop"`} {
		if !strings.Contains(tf, slug) {
			t.Errorf("main.tf lacks the app %s the Console's editorOf() resolves", slug)
		}
	}
}

// What a workspace built from this template actually installs, and from where.
func TestDevcontainerTemplateInstallsWhatTheRepositoryDeclares(t *testing.T) {
	mainTF, _, _ := coderDevcontainerTemplate(t)
	tf := withoutComments(mainTF)

	img := regexp.MustCompile(`envbuilder_image\s*=\s*"([^"]+)"`).FindStringSubmatch(tf)
	if img == nil {
		t.Fatal("main.tf pins no envbuilder image")
	}
	if !regexp.MustCompile(`^ghcr\.io/coder/envbuilder:v\d+\.\d+\.\d+$`).MatchString(img[1]) {
		t.Errorf("envbuilder image %q is not pinned to a release tag (never latest)", img[1])
	}
	if !regexp.MustCompile(`prepare_image\s*=\s*"[^"]+:\d[^"]*"`).MatchString(tf) {
		t.Error("the init container image must carry a version tag")
	}

	for _, want := range []struct{ needle, why string }{
		{"ENVBUILDER_DEVCONTAINER_JSON_PATH", "envbuilder must build from the init container's copy of devcontainer.json"},
		{"ENVBUILDER_FALLBACK_IMAGE", "a repository without a devcontainer, or a failed build, runs on the fallback image"},
		{"ENVBUILDER_LAYER_CACHE_DIR", "a restart must replay the build from the home volume, not rebuild"},
		{"export HOME=/home/coder", "the agent's HOME is the volume whichever user the image runs as"},
		{".vscode/extensions.json", "code-server installs the extensions the repository recommends"},
		{"--install-extension", "code-server installs them with --install-extension"},
		{".idea/externalDependencies.xml", "IntelliJ gets the plugins the repository requires"},
		{"installPlugins", "the plugins are installed into the JetBrains backend with remote-dev-server.sh installPlugins"},
		{".postCreateCommand", "the devcontainer's postCreateCommand runs"},
		{"DEVCONTAINER-FAILED.md", "a failed build must be visible in the folder, not only in a log"},
		{"enable_service_links = false", "a developer's shell must not inherit WEBHOOK_PORT=tcp://… (packages/CONFLICTS.md)"},
	} {
		if !strings.Contains(tf, want.needle) {
			t.Errorf("main.tf lacks %q: %s", want.needle, want.why)
		}
	}
	if strings.Contains(tf, ":latest") {
		t.Error("main.tf references a :latest image")
	}
	// The build copy drops the lifecycle commands (envbuilder would otherwise
	// run post-create on every start with an ephemeral HOME) and records the
	// real user for the setup script.
	del := regexp.MustCompile(`del\(([^)]*)\)`).FindStringSubmatch(tf)
	if del == nil {
		t.Fatal("the init container's jq never deletes the lifecycle commands from the build copy")
	}
	for _, key := range []string{".initializeCommand", ".onCreateCommand", ".updateContentCommand", ".postCreateCommand", ".postStartCommand", ".postAttachCommand"} {
		if !strings.Contains(del[1], key) {
			t.Errorf("the build copy must delete %s (envbuilder would run it on every start with an ephemeral HOME)", key)
		}
	}
	if !strings.Contains(tf, `.remoteUser = "root"`) || !strings.Contains(tf, "TARGET_USER=") {
		t.Error("the build copy must run envbuilder as root and the setup script must switch to the recorded user (TARGET_USER)")
	}
}

// Every manifest in the file parses and every container declares a request:
// the seed Job and the workspace pod are both in adhar-system.
func TestDevcontainerSeedJobDeclaresItsResources(t *testing.T) {
	var docs []map[string]any
	for _, chunk := range strings.Split(readStackFile(t, coderDevcontainerManifest), "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(chunk), &m); err != nil {
			t.Fatalf("%s: %v", coderDevcontainerManifest, err)
		}
		if m != nil {
			docs = append(docs, m)
		}
	}
	if len(docs) != 3 {
		t.Fatalf("expected 3 documents, got %d", len(docs))
	}
	for _, d := range docs {
		if d["kind"] != "Job" {
			continue
		}
		for _, c := range sliceOfMaps(nestedMap(d, "spec", "template", "spec")["containers"]) {
			if nestedMap(c, "resources", "requests")["cpu"] == nil || nestedMap(c, "resources", "requests")["memory"] == nil {
				t.Errorf("container %v of the seed Job declares no requests", c["name"])
			}
			if !strings.HasPrefix(toString(c["image"]), "ghcr.io/coder/coder:v") {
				t.Errorf("the seed runs %v; it must be the pinned Coder server image (the CLI)", c["image"])
			}
		}
	}
}
