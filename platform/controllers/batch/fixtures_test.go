package batch

import (
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// Fixtures: a run and its pods as the API server holds them, built with the
// labels and env the composition renders (compositions/batchjob/kubernetes.yaml).

var t0 = time.Date(2026, 11, 3, 17, 30, 0, 0, time.UTC)

const okSummary = `{"v":1,"job":"importJob","batchJob":"importer","instanceId":42,"executionId":97,"status":"COMPLETED",` +
	`"exitCode":0,"outcome":"completed","trigger":"manual","businessDate":"2026-11-03",` +
	`"read":120000,"written":119990,"skipped":10,"deadLettered":10,"start":"2026-11-03T17:30:04Z","end":"2026-11-03T17:43:36Z"}`

func runJob(name, uid, trigger string, params map[string]string) *unstructured.Unstructured {
	encoded, _ := json.Marshal(params)
	if params == nil {
		encoded = []byte("{}")
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]interface{}{
			"name": name, "namespace": "prod", "uid": uid,
			"creationTimestamp": t0.Format(time.RFC3339),
			"labels": map[string]interface{}{
				labelBatchJob: "importer", labelWorkload: "batch", labelTrigger: trigger,
				"adhar.io/application": "importer",
			},
			"annotations": map[string]interface{}{annotationActor: "alice@example.com"},
		},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{"labels": map[string]interface{}{labelBatchJob: "importer", labelTrigger: trigger}},
				"spec": map[string]interface{}{
					"restartPolicy": "Never",
					"containers": []interface{}{map[string]interface{}{
						"name": "batch", "image": "harbor/importer@sha256:abc",
						"env": []interface{}{
							map[string]interface{}{"name": envParams, "value": string(encoded)},
							map[string]interface{}{"name": envTrigger, "value": trigger},
							map[string]interface{}{"name": envTimeZone, "value": "Asia/Singapore"},
						},
					}},
				},
			},
		},
	}}
	return u
}

func setCond(run *unstructured.Unstructured, typ, reason, at string) {
	conds, _, _ := unstructured.NestedSlice(run.Object, "status", "conditions")
	conds = append(conds, map[string]interface{}{"type": typ, "status": "True", "reason": reason, "lastTransitionTime": at})
	_ = unstructured.SetNestedSlice(run.Object, conds, "status", "conditions")
}

func setStatus(run *unstructured.Unstructured, field string, v interface{}) {
	_ = unstructured.SetNestedField(run.Object, v, "status", field)
}

type podState struct {
	phase    string
	running  bool
	exitCode *int64
	reason   string
	message  string
}

func runPod(name string, run *unstructured.Unstructured, created time.Time, st podState) unstructured.Unstructured {
	p := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]interface{}{
			"name": name, "namespace": run.GetNamespace(), "uid": "pod-" + name,
			"creationTimestamp": created.Format(time.RFC3339),
			"labels": map[string]interface{}{
				labelBatchJob: "importer", labelWorkload: "batch", labelNativeJob: run.GetName(),
			},
		},
		"status": map[string]interface{}{"phase": st.phase},
	}}
	p.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: run.GetName(), UID: types.UID(run.GetUID())}})
	cs := map[string]interface{}{"name": "batch"}
	switch {
	case st.exitCode != nil:
		cs["state"] = map[string]interface{}{"terminated": map[string]interface{}{
			"exitCode": *st.exitCode, "reason": st.reason, "message": st.message,
		}}
	case st.running:
		cs["state"] = map[string]interface{}{"running": map[string]interface{}{}}
	}
	_ = unstructured.SetNestedSlice(p.Object, []interface{}{cs}, "status", "containerStatuses")
	return p
}

func code(n int64) *int64 { return &n }

func types0(ts []transition) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Type)
	}
	return out
}
