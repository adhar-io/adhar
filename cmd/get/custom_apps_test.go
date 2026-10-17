package get

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func argoApp(name, app, env string, health string) unstructured.Unstructured {
	u := unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": name, "namespace": "adhar-system", "creationTimestamp": "2026-10-17T00:00:00Z"},
		"spec":     map[string]interface{}{"destination": map[string]interface{}{"namespace": env}},
		"status":   map[string]interface{}{"health": map[string]interface{}{"status": health}, "sync": map[string]interface{}{"status": "Synced"}},
	}}
	if app != "" {
		u.SetLabels(map[string]string{applicationLabel: app, environmentLabel: env})
	}
	return u
}

// `adhar get apps` lists custom applications, one row per environment, and
// nothing else: platform packages carry no adhar.io/application label.
func TestGetAppsListsCustomApplicationsOnly(t *testing.T) {
	apps := []unstructured.Unstructured{
		argoApp("adhar-ngapp-dev", "ngapp", "dev", "Healthy"),
		argoApp("adhar-ngapp-prod", "ngapp", "prod", "Progressing"),
		argoApp("keycloak", "", "adhar-system", "Healthy"),
	}
	rollout := unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "ngapp", "namespace": "dev"},
		"spec":     map[string]interface{}{"template": map[string]interface{}{"spec": map[string]interface{}{"containers": []interface{}{map[string]interface{}{"image": "harbor/ngapp@sha256:abc"}}}}},
		"status":   map[string]interface{}{"readyReplicas": int64(1), "replicas": int64(1), "availableReplicas": int64(1)},
	}}
	xr := func(name string) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": name, "creationTimestamp": "2026-10-17T00:00:00Z"}}}
	}
	rows := customAppRows(apps, []unstructured.Unstructured{rollout}, []unstructured.Unstructured{xr("ngapp"), xr("ledger")}, nil, "", time.Now())
	if len(rows) != 3 {
		t.Fatalf("want ngapp/dev, ngapp/prod and the not-yet-deployed ledger; got %d rows: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Name == "keycloak" {
			t.Fatal("a platform package was listed as an application")
		}
	}
	if rows[0].Name != "ledger" || rows[0].Status != "Pending" {
		t.Errorf("an Application XR with no Argo CD apps yet must still be listed as Pending: %+v", rows[0])
	}
	if rows[1].Namespace != "dev" || rows[1].Replicas.Ready != 1 || len(rows[1].Images) != 1 {
		t.Errorf("the dev row lacks its Rollout's replicas and image: %+v", rows[1])
	}
	if got := customAppRows(apps, nil, nil, []string{"ngapp"}, "prod", time.Now()); len(got) != 1 || got[0].Namespace != "prod" {
		t.Errorf("name and namespace filters were not applied: %+v", got)
	}
}
