package adharplatform

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/knowledge"
)

func testBundle(t *testing.T, version string, files map[string]string, partBytes int) *knowledge.Bundle {
	t.Helper()
	var fs []knowledge.File
	for p, d := range files {
		fs = append(fs, knowledge.File{Path: p, Data: []byte(d)})
	}
	b, err := knowledge.Pack(fs, knowledge.Release{Version: version}, partBytes, knowledge.MaxParts)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func incompressible(n int) string {
	var b strings.Builder
	x := uint64(88172645463325252)
	for b.Len() < n {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b.WriteByte(byte(x))
	}
	return b.String()
}

// The controller publishes this release's knowledge bundle, writes nothing on
// a pass where it is already published, re-publishes when the release changes,
// and removes parts a smaller bundle no longer uses.
func TestTheControllerPublishesTheKnowledgeBundleOncePerRelease(t *testing.T) {
	ctx := context.Background()
	writes := 0
	c := fake.NewClientBuilder().WithScheme(storageScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			writes++
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			writes++
			return c.Update(ctx, obj, opts...)
		},
	}).Build()

	big := testBundle(t, "v1", map[string]string{"docs/a.md": incompressible(3000), "cli/adhar.md": "# adhar\n"}, 1024)
	if len(big.Parts) < 3 {
		t.Fatalf("test bundle should span several parts, has %d", len(big.Parts))
	}
	r := &AdharPlatformReconciler{Client: c, KnowledgeBundle: big}
	if err := r.reconcileKnowledgeBundle(ctx); err != nil {
		t.Fatal(err)
	}
	list := &corev1.ConfigMapList{}
	if err := c.List(ctx, list, client.InNamespace(globals.AdharSystemNamespace)); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != len(big.Parts) {
		t.Fatalf("published %d ConfigMaps for %d parts", len(list.Items), len(big.Parts))
	}
	cm0 := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: globals.AdharSystemNamespace, Name: knowledge.ConfigMapName(0)}, cm0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cm0.Data[knowledge.ManifestKey], big.Manifest.Digest) {
		t.Fatal("part 0 does not carry the manifest")
	}

	before := writes
	if err := r.reconcileKnowledgeBundle(ctx); err != nil {
		t.Fatal(err)
	}
	if writes != before {
		t.Fatalf("an already-published bundle was written again (%d writes); every 30 s reconcile would push megabytes to the API server", writes-before)
	}

	small := testBundle(t, "v2", map[string]string{"cli/adhar.md": "# adhar v2\n"}, 1024)
	r.KnowledgeBundle = small
	if err := r.reconcileKnowledgeBundle(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.List(ctx, list, client.InNamespace(globals.AdharSystemNamespace)); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("a one-part bundle left %d ConfigMaps behind", len(list.Items))
	}
	if got := list.Items[0].Annotations[knowledge.AnnotationVer]; got != "v2" {
		t.Fatalf("upgrade did not re-publish: version %q", got)
	}
}

// Without an override the controller publishes the binary's own bundle, which
// carries the embedded docs.
func TestTheDefaultBundleIsThisBinarysDocs(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(storageScheme(t)).Build()
	r := &AdharPlatformReconciler{Client: c}
	if err := r.reconcileKnowledgeBundle(context.Background()); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: globals.AdharSystemNamespace, Name: knowledge.ConfigMapName(0)}, cm); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cm.Data[knowledge.ManifestKey], "docs/ARCHITECTURE.md") {
		t.Fatal("the published bundle does not list the platform docs")
	}
}

// The AI runtime and its reindex CronJob must mount exactly the ConfigMaps the
// controller can write, each optional, where ADHAR_AI_KNOWLEDGE_PATH points.
// A fifth part the manifests do not project would be silently dropped; the
// reader then sees a digest mismatch and indexes no docs at all.
func TestTheAIRuntimeMountsEveryKnowledgePart(t *testing.T) {
	var want []string
	for i := 0; i < knowledge.MaxParts; i++ {
		want = append(want, knowledge.ConfigMapName(i))
	}
	for _, file := range []string{"agent-runtime.yaml", "knowledge-reindex.yaml"} {
		body := readStackFile(t, "ai/adhar-ai/manifests/"+file)
		found := false
		for _, doc := range strings.Split(body, "\n---") {
			var obj map[string]any
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
				continue
			}
			spec := podSpecOf(obj)
			if spec == nil {
				continue
			}
			for _, v := range asList(spec["volumes"]) {
				vol := v.(map[string]any)
				if vol["name"] != "knowledge" {
					continue
				}
				found = true
				var got []string
				for _, s := range asList(vol["projected"].(map[string]any)["sources"]) {
					cm := s.(map[string]any)["configMap"].(map[string]any)
					if cm["optional"] != true {
						t.Errorf("%s: %v must be optional — parts exist only as the bundle needs them", file, cm["name"])
					}
					got = append(got, cm["name"].(string))
				}
				if strings.Join(got, ",") != strings.Join(want, ",") {
					t.Errorf("%s projects %v, the controller writes %v", file, got, want)
				}
			}
			for _, ct := range asList(spec["containers"]) {
				ctr := ct.(map[string]any)
				mounted, env := false, ""
				for _, m := range asList(ctr["volumeMounts"]) {
					mm := m.(map[string]any)
					if mm["name"] == "knowledge" && mm["mountPath"] == knowledge.MountPath {
						mounted = true
					}
				}
				for _, e := range asList(ctr["env"]) {
					ee := e.(map[string]any)
					if ee["name"] == "ADHAR_AI_KNOWLEDGE_PATH" {
						env, _ = ee["value"].(string)
					}
				}
				if !mounted || env != knowledge.MountPath {
					t.Errorf("%s container %v: knowledge mounted=%v, ADHAR_AI_KNOWLEDGE_PATH=%q (want %s)", file, ctr["name"], mounted, env, knowledge.MountPath)
				}
			}
		}
		if !found {
			t.Errorf("%s has no knowledge volume", file)
		}
	}
}

// Bootstrap mirrors exactly one upstream repository — the templates — and no
// longer the platform's own source: the AI reads the knowledge bundle.
func TestBootstrapNoLongerMirrorsThePlatformSource(t *testing.T) {
	src, err := os.ReadFile("controller.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := regexp.MustCompile(`r\.mirrorGiteaRepository\(ctx, ([^,]+),`).FindAllStringSubmatch(string(src), -1)
	if len(calls) != 1 || calls[0][1] != "globals.GitOpsRepoTemplates" {
		t.Fatalf("bootstrap mirrors %v; only the templates repository should be mirrored", calls)
	}
	if strings.Contains(string(src), "github.com/adhar-io/adhar.git") {
		t.Fatal("controller still references the adhar source repository")
	}
}

func podSpecOf(obj map[string]any) map[string]any {
	spec, _ := obj["spec"].(map[string]any)
	switch obj["kind"] {
	case "Deployment":
		tpl, _ := spec["template"].(map[string]any)
		s, _ := tpl["spec"].(map[string]any)
		return s
	case "CronJob":
		jt, _ := spec["jobTemplate"].(map[string]any)
		js, _ := jt["spec"].(map[string]any)
		tpl, _ := js["template"].(map[string]any)
		s, _ := tpl["spec"].(map[string]any)
		return s
	}
	return nil
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
