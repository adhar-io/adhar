package batch

import (
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// The run template as compositions/batchjob/kubernetes.yaml writes it for the
// importer of §6.3: template.yaml is the unnamed manual run, parameters.json
// the declarations with every default written out. Trimmed to the fields the
// CLI touches plus a sidecar, which must NOT receive the client variables.
const importerTemplateYAML = `apiVersion: batch/v1
kind: Job
metadata:
  namespace: prod
  labels:
    adhar.io/batch-job: importer
    adhar.io/batch-trigger: manual
    adhar.io/application: importer
    adhar.io/environment: prod
    adhar.io/workload: batch
  annotations:
    adhar.io/batch-actor: ""
spec:
  backoffLimit: 2
  activeDeadlineSeconds: 21600
  template:
    metadata:
      labels:
        adhar.io/batch-job: importer
        adhar.io/batch-trigger: manual
        adhar.io/batch-role: single
    spec:
      restartPolicy: Never
      serviceAccountName: importer-batch
      containers:
        - name: batch
          image: harbor.adhar.localtest.me/adhar/importer@sha256:aaaa
          env:
            - name: ADHAR_BATCH_JOB
              value: importJob
            - name: ADHAR_BATCH_TRIGGER
              value: manual
            - name: ADHAR_BATCH_PARAMS
              value: "{}"
            - name: ADHAR_BATCH_TIME_ZONE
              value: Asia/Singapore
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  apiVersion: v1
                  fieldPath: metadata.name
        - name: sidecar
          image: busybox
          env:
            - name: OTHER
              value: x
`

func templateConfigMap(params string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": "importer-run", "namespace": "prod"},
		"data": map[string]interface{}{
			"template.yaml":   importerTemplateYAML,
			"parameters.json": params,
			"kind":            "Job",
			"naming":          "v1",
		},
	}}
}

func batchJobXR(name, ns, cron string, held string) *unstructured.Unstructured {
	params := map[string]interface{}{
		"job": "importJob", "image": "harbor.adhar.localtest.me/adhar/importer@sha256:aaaa",
		"database": map[string]interface{}{"secretRef": "importer-db-app", "schema": "batch"},
		"timeouts": map[string]interface{}{"activeDeadlineSeconds": int64(3600), "terminationGracePeriodSeconds": int64(120)},
		"parameters": []interface{}{
			map[string]interface{}{"name": "businessDate", "type": "date", "source": "schedule"},
			map[string]interface{}{"name": "region", "default": "sg", "allowed": []interface{}{"sg", "uk"}},
		},
	}
	if cron != "" {
		params["schedule"] = map[string]interface{}{"cron": cron, "timeZone": "Asia/Singapore"}
	}
	meta := map[string]interface{}{"name": name, "namespace": ns,
		"creationTimestamp": testNow.Add(-48 * time.Hour).Format(time.RFC3339),
		"labels":            map[string]interface{}{"adhar.io/application": name, "adhar.io/environment": ns}}
	if held != "" {
		meta["annotations"] = map[string]interface{}{annotationHold: held}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1", "kind": "BatchJob",
		"metadata": meta,
		"spec":     map[string]interface{}{"parameters": params},
		"status": map[string]interface{}{
			"scheduler": "kubernetes", "runTemplate": name + "-run",
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True", "reason": "Available"},
				map[string]interface{}{"type": "TemplateReady", "status": "True", "reason": "Published"},
			},
		},
	}}
}

// jobObject is a run as the Job API reports it, in one of the phases a person reads.
func jobObject(name, ns, batchJob, phase string, created time.Time) *unstructured.Unstructured {
	status := map[string]interface{}{}
	spec := map[string]interface{}{
		"template": map[string]interface{}{"spec": map[string]interface{}{"containers": []interface{}{
			map[string]interface{}{"name": "batch", "image": "img", "env": []interface{}{
				map[string]interface{}{"name": "ADHAR_BATCH_PARAMS", "value": `{"businessDate":"2026-11-03","region":"sg"}`},
				map[string]interface{}{"name": "ADHAR_BATCH_TIME_ZONE", "value": "Asia/Singapore"},
			}},
		}}},
	}
	switch phase {
	case "succeeded":
		status["succeeded"] = int64(1)
		status["startTime"] = created.Format(time.RFC3339)
		status["completionTime"] = created.Add(10 * time.Minute).Format(time.RFC3339)
		status["conditions"] = []interface{}{map[string]interface{}{"type": "Complete", "status": "True"}}
	case "failed":
		status["failed"] = int64(1)
		status["startTime"] = created.Format(time.RFC3339)
		status["conditions"] = []interface{}{map[string]interface{}{"type": "Failed", "status": "True", "reason": "PodFailurePolicy", "message": "Container batch for pod importer-x failed with exit code 3 matching FailJob rule", "lastTransitionTime": created.Add(5 * time.Minute).Format(time.RFC3339)}}
	case "running":
		status["active"] = int64(1)
		status["ready"] = int64(1)
		status["startTime"] = created.Format(time.RFC3339)
	case "pending":
		status["active"] = int64(1)
		status["ready"] = int64(0)
	case "stopped":
		spec["suspend"] = true
		status["conditions"] = []interface{}{map[string]interface{}{"type": "Suspended", "status": "True", "reason": "JobSuspended", "message": "Job suspended", "lastTransitionTime": created.Add(3 * time.Minute).Format(time.RFC3339)}}
	case "stopping":
		spec["suspend"] = true
		status["active"] = int64(1)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]interface{}{
			"name": name, "namespace": ns, "uid": "uid-" + name,
			"creationTimestamp": created.Format(time.RFC3339),
			"labels":            map[string]interface{}{labelBatchJob: batchJob, labelTrigger: "manual", "adhar.io/application": batchJob},
			"annotations":       map[string]interface{}{annotationActor: "alice"},
		},
		"spec":   spec,
		"status": status,
	}}
}

// podObject is a run's pod whose batch container ended with a summary.
func podObject(name, ns, job string, exitCode int, summary map[string]interface{}, created time.Time) *unstructured.Unstructured {
	msg := ""
	if summary != nil {
		b, _ := json.Marshal(summary)
		msg = string(b)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]interface{}{
			"name": name, "namespace": ns, "creationTimestamp": created.Format(time.RFC3339),
			"labels": map[string]interface{}{labelNativeJob: job, labelBatchJob: "importer", labelRole: "single"},
		},
		"status": map[string]interface{}{
			"phase": "Succeeded",
			"containerStatuses": []interface{}{map[string]interface{}{
				"name": "batch",
				"state": map[string]interface{}{"terminated": map[string]interface{}{
					"exitCode": int64(exitCode), "reason": "Completed", "message": msg,
				}},
			}},
		},
	}}
}

func projectXR(name string, envs map[string]string) *unstructured.Unstructured {
	var list []interface{}
	for _, e := range []string{"dev", "test", "prod"} {
		if ns, ok := envs[e]; ok {
			list = append(list, map[string]interface{}{"name": e, "namespace": ns, "tier": e})
		}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Project",
		"metadata": map[string]interface{}{"name": name, "namespace": "adhar-system"},
		"status":   map[string]interface{}{"environments": list},
	}}
}

func fakeCluster(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			batchJobGVR:   "BatchJobList",
			projectGVR:    "ProjectList",
			jobGVR:        "JobList",
			volcanoJobGVR: "JobList",
			commandGVR:    "CommandList",
			podGroupGVR:   "PodGroupList",
			queueGVR:      "QueueList",
			podGVR:        "PodList",
			configMapGVR:  "ConfigMapList",
		}, objs...)
}

func fixedClock(t *testing.T) {
	t.Helper()
	prev := now
	now = func() time.Time { return testNow }
	t.Cleanup(func() { now = prev })
}

func mustParseTemplate(t *testing.T, params string) *runTemplate {
	t.Helper()
	tpl, err := parseRunTemplate(templateConfigMap(params))
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

var _ = metav1.Now
