package batch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"adhar-io/adhar/tests/e2e"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// A2.10 removes the descriptor by switching its element off in the LIVE
// ApplicationSet. That object is itself a managed resource of an Argo CD
// Application with selfHeal (adhar-appset-source.yaml), which would put the
// element back within seconds — the descriptor pruned, recreated, pruned —
// and the canary would read a flapping cluster. These pin that the owner is
// the one the canary holds, that the hold really removes its automated sync
// and gives back exactly what was there, and that the canary holds it before
// it patches.

func readAppSetOwner(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e2e.RepoRoot, "platform", "stack", "adhar-appset-source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: doc}
}

func TestTheApplicationSetsOwnerIsTheOneA2_10Holds(t *testing.T) {
	owner := readAppSetOwner(t)
	if owner.GetKind() != "Application" || owner.GetName() != AppSetOwner {
		t.Fatalf("adhar-appset-source.yaml is %s %q; the canary holds Application %q", owner.GetKind(), owner.GetName(), AppSetOwner)
	}
	if include, _, _ := unstructured.NestedString(owner.Object, "spec", "source", "directory", "include"); include != "applicationset.yaml" {
		t.Errorf("%s includes %q: is it still the ApplicationSet's owner?", AppSetOwner, include)
	}
	if heal, _, _ := unstructured.NestedBool(owner.Object, "spec", "syncPolicy", "automated", "selfHeal"); !heal {
		t.Logf("%s no longer self-heals; the hold is harmless but no longer needed", AppSetOwner)
	}

	before := owner.DeepCopy().Object
	hold, release, err := SelfHealHold(owner)
	if err != nil || hold == nil || release == nil {
		t.Fatalf("SelfHealHold on the shipped owner: hold %s release %s err %v", hold, release, err)
	}
	if !applyPatch(t, owner.Object, hold) {
		t.Fatal("the hold patch failed on the shipped owner")
	}
	if _, ok, _ := unstructured.NestedFieldNoCopy(owner.Object, "spec", "syncPolicy", "automated"); ok {
		t.Error("after the hold the owner still syncs automatically")
	}
	if opts, _, _ := unstructured.NestedStringSlice(owner.Object, "spec", "syncPolicy", "syncOptions"); len(opts) == 0 {
		t.Error("the hold took the owner's syncOptions with it")
	}
	// The release carries the automated block as JSON; compare it as JSON.
	if !applyPatch(t, owner.Object, release) {
		t.Fatal("the release patch failed")
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(owner.Object)
	var x, y interface{}
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	if !reflect.DeepEqual(x, y) {
		t.Errorf("hold + release did not give the owner back as it was:\n%s\n%s", a, b)
	}

	delete(owner.Object["spec"].(map[string]interface{})["syncPolicy"].(map[string]interface{}), "automated")
	if h, r, err := SelfHealHold(owner); h != nil || r != nil || err != nil {
		t.Errorf("an owner that syncs by hand was held: %s %s %v", h, r, err)
	}
}

func TestTheCanaryHoldsTheOwnerBeforeItPatchesTheApplicationSet(t *testing.T) {
	raw, err := os.ReadFile("volcano_e2e_test.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "func (v *volcanoSuite) removeVolcano(")
	if start < 0 {
		t.Fatal("removeVolcano not found")
	}
	body := src[start:]
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end+1]
	}
	hold := strings.Index(body, "Patch(ctx, AppSetOwner, types.JSONPatchType, hold")
	off := strings.Index(body, "patch(false)")
	release := strings.Index(body, "Patch(context.Background(), AppSetOwner, types.JSONPatchType, release")
	switch {
	case hold < 0:
		t.Error("removeVolcano never holds the ApplicationSet's owner: Argo CD self-heals the element back on")
	case off < 0 || hold > off:
		t.Error("removeVolcano patches the ApplicationSet before it holds the owner")
	case release < 0:
		t.Error("removeVolcano never gives the owner its automated sync back")
	}
}
