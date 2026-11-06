package apps

import (
	"strings"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// A paved-road Application that an Argo CD Application tracks.
func trackedXR(name, trackingID string) *unstructured.Unstructured {
	xr := pavedRoadXR(name, "Healthy", "")
	xr.SetAnnotations(map[string]string{argoCDTrackingAnnotation: trackingID})
	return xr
}

// The Argo CD Application the adhar-services ApplicationSet generates for a
// repository carrying .adhar/app.yaml.
func bootstrapApp(namespace, name string, labels map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": name, "namespace": namespace, "labels": labels},
		"spec": map[string]interface{}{
			"syncPolicy": map[string]interface{}{"automated": map[string]interface{}{"prune": true, "selfHeal": true}},
		},
	}}
}

// An Application that Argo CD keeps from Git — an SPA scaffold's
// .adhar/app.yaml, adopted with self-heal — is not deleted: the delete took
// every environment down and was undone at the next sync. It is refused before
// anyone is asked, saying what retires it. A tracking id that is stale (its
// Argo CD Application is gone) or written for another object keeps nothing.
func TestAnApplicationArgoCDKeepsFromGitIsNotDeleted(t *testing.T) {
	adopted := "storefront-bootstrap:platform.adhar.io/Application:adhar-system/storefront"
	bootstrap := bootstrapApp("adhar-system", "storefront-bootstrap",
		map[string]interface{}{"adhar.io/type": "service-bootstrap", "adhar.io/service": "storefront"})
	for _, tc := range []struct {
		name    string
		xr      *unstructured.Unstructured
		argo    *unstructured.Unstructured
		refused []string // what the refusal must say; nil = deleted
	}{
		{"adopted from the repository's .adhar/app.yaml", trackedXR("storefront", adopted), bootstrap,
			[]string{"adhar-system/storefront is kept from Git by Argo CD application storefront-bootstrap", "delete .adhar/app.yaml from the repository adhar/storefront"}},
		{"kept by an Argo CD Application in another namespace", trackedXR("storefront", "team-a_storefront-gitops:platform.adhar.io/Application:adhar-system/storefront"),
			bootstrapApp("team-a", "storefront-gitops", nil),
			[]string{"kept from Git by Argo CD application team-a/storefront-gitops", "remove it from the Git source team-a/storefront-gitops syncs"}},
		{"its Argo CD Application is gone", trackedXR("storefront", adopted), nil, nil},
		{"a tracking id written for another object", trackedXR("storefront", "storefront-bootstrap:platform.adhar.io/Application:adhar-system/storefront-old"), bootstrap, nil},
		{"a tracking id of another group", trackedXR("storefront", "storefront-bootstrap:argoproj.io/Application:adhar-system/storefront"), bootstrap, nil},
		{"a tracking id of another kind", trackedXR("storefront", "storefront-bootstrap:platform.adhar.io/Project:adhar-system/storefront"), bootstrap, nil},
	} {
		objs := []runtime.Object{tc.xr}
		if tc.argo != nil {
			objs = append(objs, tc.argo)
		}
		dc := fakeCluster(objs...)
		asked := false
		err := deleteApplication(t.Context(), dc, applicationNamespaces(false, "dev"), "storefront", func(string) bool {
			asked = true
			return true
		})
		_, gerr := dc.Resource(applicationGVR).Namespace("adhar-system").Get(t.Context(), "storefront", metav1.GetOptions{})
		gone := k8serrors.IsNotFound(gerr)
		if tc.refused == nil {
			if err != nil || !asked || !gone {
				t.Errorf("%s: err %v, asked %v, deleted %v — want it deleted", tc.name, err, asked, gone)
			}
			continue
		}
		if err == nil || asked || gone {
			t.Errorf("%s: err %v, asked %v, deleted %v — want it refused before asking", tc.name, err, asked, gone)
			continue
		}
		for _, want := range tc.refused {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: refusal %q does not say %q", tc.name, err, want)
			}
		}
	}
}
