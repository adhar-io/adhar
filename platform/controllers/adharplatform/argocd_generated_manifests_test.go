package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"
)

// argoCDMinor is the Argo CD minor release the platform runs. Moving to another
// minor is a reviewed upgrade, not a version bump: read the upstream
// docs/operator-manual/upgrading/<from>-<to>.md, adapt hack/argocd/values*.yaml,
// regenerate, rehearse — and only then change this constant. Patch releases
// within the minor need no change here.
const argoCDMinor = "v3.6"

var (
	// generate-manifests.sh pins the CHART; its comment names the app it packages.
	generatorPinRe = regexp.MustCompile(`(?m)^ARGOCD_VERSION="(\d+\.\d+\.\d+)"\s*#.*packages Argo CD app (v\d+\.\d+\.\d+)`)
	chartLabelRe   = regexp.MustCompile(`(?m)^\s*helm\.sh/chart:\s*"?([^"\s]+)"?\s*$`)
	appVersionRe   = regexp.MustCompile(`(?m)^\s*app\.kubernetes\.io/version:\s*"?([^"\s]+)"?\s*$`)
	argoCDImageRe  = regexp.MustCompile(`(?m)^\s*(?:-\s+)?image:\s*"?quay\.io/argoproj/argocd(?::([^"\s]+))?"?\s*$`)
	yamlDocSepRe   = regexp.MustCompile(`(?m)^---[ \t]*$`)
)

// The Argo CD version must be the same everywhere it is stated: the chart the
// generator pins, the app version that chart packages, every object both
// generated manifests install, the values files the manifests come from, and
// the two documents that tell an operator which version they are running.
//
// The two renderings are pre-generated files embedded in the controller, and a
// cluster gets whichever one enableHAMode selects. A version stated in one place
// and not another is either a manifest nobody regenerated (the values say one
// thing, the cluster runs another — the drift TestArgoCDReconciliationMatches
// ItsGeneratorValues was written for), or an HA toggle that silently up- or
// downgrades Argo CD.
func TestArgoCDVersionIsPinnedConsistently(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		t.Helper()
		b, rerr := os.ReadFile(filepath.Join(root, rel))
		if rerr != nil {
			t.Fatalf("reading %s: %v", rel, rerr)
		}
		return string(b)
	}

	m := generatorPinRe.FindStringSubmatch(read("hack/argocd/generate-manifests.sh"))
	if m == nil {
		t.Fatal("hack/argocd/generate-manifests.sh no longer pins ARGOCD_VERSION=\"<chart>\" with a " +
			"`# ... packages Argo CD app v<x.y.z>` comment; this guard reads both")
	}
	chart, app := m[1], m[2]

	if !strings.HasPrefix(app, argoCDMinor+".") {
		t.Errorf("the generator packages Argo CD %s, but the platform is pinned to %s.x. Changing the "+
			"minor is an upgrade: follow the upstream upgrading notes, adapt the values, regenerate "+
			"both manifests, rehearse, then update argoCDMinor", app, argoCDMinor)
	}

	for _, rel := range []string{
		"platform/controllers/adharplatform/resources/argocd/install.yaml",
		"platform/controllers/adharplatform/resources/argocd/install-ha.yaml",
	} {
		body := read(rel)
		charts := 0
		for _, c := range chartLabelRe.FindAllStringSubmatch(body, -1) {
			charts++
			if c[1] != "argo-cd-"+chart {
				t.Errorf("%s carries helm.sh/chart %q, but the generator pins argo-cd chart %s: the "+
					"manifest was not regenerated from the pinned chart", rel, c[1], chart)
				break
			}
		}
		if charts == 0 {
			t.Errorf("%s has no helm.sh/chart label; it is no longer the generator's output", rel)
		}
		versions := 0
		for _, v := range appVersionRe.FindAllStringSubmatch(body, -1) {
			versions++
			if v[1] != app {
				t.Errorf("%s labels an object app.kubernetes.io/version %q, but chart %s packages %s",
					rel, v[1], chart, app)
				break
			}
		}
		if versions == 0 {
			t.Errorf("%s has no app.kubernetes.io/version label", rel)
		}
		images := 0
		for _, img := range argoCDImageRe.FindAllStringSubmatch(body, -1) {
			images++
			if img[1] != app {
				t.Errorf("%s runs quay.io/argoproj/argocd:%s, want %s — every Argo CD component "+
					"must run the version the chart packages", rel, img[1], app)
			}
		}
		if images == 0 {
			t.Errorf("%s runs no quay.io/argoproj/argocd image; this guard is checking nothing", rel)
		}
	}

	// The values must leave the image tag to the chart (its appVersion), or
	// pin exactly the version the generator comment states.
	for _, rel := range []string{"hack/argocd/values.yaml", "hack/argocd/values-ha.yaml"} {
		var values struct {
			Global struct {
				Image struct {
					Tag string `json:"tag"`
				} `json:"image"`
			} `json:"global"`
		}
		if err := sigsyaml.Unmarshal([]byte(read(rel)), &values); err != nil {
			t.Fatalf("parsing %s: %v", rel, err)
		}
		if tag := values.Global.Image.Tag; tag != "" && tag != app {
			t.Errorf("%s pins global.image.tag %q while the generator packages %s; the values "+
				"would override the chart's own version", rel, tag, app)
		}
	}

	// And what the operator reads.
	readme := read("hack/argocd/README.md")
	heading := regexp.MustCompile(`(?m)^##\s.*Version:\s*Argo ?CD (v\S+) \(argo-cd chart (\S+)\)`).FindStringSubmatch(readme)
	switch {
	case heading == nil:
		t.Error("hack/argocd/README.md has no `## ... Version: Argo CD v<x.y.z> (argo-cd chart <x.y.z>)` heading")
	case heading[1] != app || heading[2] != chart:
		t.Errorf("hack/argocd/README.md says Argo CD %s (chart %s); the generator pins chart %s = %s",
			heading[1], heading[2], chart, app)
	}
	arch := regexp.MustCompile("(?m)^\\|\\s*Argo ?CD\\s*\\|\\s*`(v[^`]+)`\\s*\\(chart ([^)]+)\\)").FindStringSubmatch(read("docs/ARCHITECTURE.md"))
	switch {
	case arch == nil:
		t.Error("docs/ARCHITECTURE.md has no `| ArgoCD | `v<x.y.z>` (chart <x.y.z>) |` row")
	case arch[1] != app || arch[2] != chart:
		t.Errorf("docs/ARCHITECTURE.md lists Argo CD %s (chart %s); the generator pins chart %s = %s",
			arch[1], arch[2], chart, app)
	}
}

// A generated Argo CD manifest must not state any key twice in one mapping.
//
// Both renderings were hand-edited for months, and a hand-edit that ADDS a key
// the chart already renders does not fail anywhere: the controller's decoder
// keeps the LAST occurrence without a word. That is how the 2026-10-15
// application-controller memory request (a second `resources:` above the
// chart's own) never reached a cluster, and how argocd-cm carried two different
// ScheduledBackup health checks of which only one could ever run. Regenerating
// removes them; this keeps them out, because the fix for a missing setting is
// the values file, never the output.
func TestTheGeneratedArgoCDManifestsHaveNoDuplicateKeys(t *testing.T) {
	for _, file := range []string{"install.yaml", "install-ha.yaml"} {
		raw, err := argoCDFS.ReadFile("resources/argocd/" + file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		docs := yamlDocSepRe.Split(string(raw), -1)
		checked := 0
		for i, doc := range docs {
			if strings.TrimSpace(doc) == "" {
				continue
			}
			checked++
			if _, err := sigsyaml.YAMLToJSONStrict([]byte(doc)); err != nil {
				t.Errorf("%s document %d (%s): %v — a key appears twice and the controller would "+
					"silently apply only the last one; put the setting in hack/argocd/values*.yaml "+
					"and regenerate instead of editing the output", file, i, docIdentity(doc), err)
			}
		}
		if checked < 10 {
			t.Fatalf("%s split into %d documents; the separator handling is broken and this guard "+
				"is checking nothing", file, checked)
		}
	}
}

// docIdentity names a YAML document by kind and metadata.name for messages.
func docIdentity(doc string) string {
	kind := regexp.MustCompile(`(?m)^kind:\s*(\S+)`).FindStringSubmatch(doc)
	name := regexp.MustCompile(`(?m)^  name:\s*(\S+)`).FindStringSubmatch(doc)
	id := "unknown"
	if kind != nil {
		id = kind[1]
	}
	if name != nil {
		id += "/" + name[1]
	}
	return id + ", " + strconv.Itoa(strings.Count(doc, "\n")) + " lines"
}
