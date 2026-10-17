/*
Copyright 2026 The Adhar Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package get

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/dynamic"

	"adhar-io/adhar/globals"
)

// `adhar get apps` lists CUSTOM applications: what teams build and promote, not
// the ~80 platform packages (all in adhar-system) and not the databases, caches
// and Kafka pools the control plane composes beside an app in its environment
// namespaces. The source of truth is the control plane: every Application XR
// composes one Argo CD Application per environment, labelled
// adhar.io/application + adhar.io/environment, deploying an Argo Rollout of the
// same name. Listing Deployments showed every platform service and none of the
// apps, which are Rollouts (2026-10-17).

var (
	argoApplicationGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	rolloutGVR         = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	appXRGVR           = schema.GroupVersionResource{Group: "platform.adhar.io", Version: "v1alpha1", Resource: "applications"}
)

const (
	applicationLabel = "adhar.io/application"
	environmentLabel = "adhar.io/environment"
)

// listCustomApplications reads the control plane's view of every custom app.
func listCustomApplications(ctx context.Context, dc dynamic.Interface, names []string, namespace string) ([]ApplicationInfo, error) {
	argoApps, err := dc.Resource(argoApplicationGVR).Namespace(globals.AdharSystemNamespace).List(ctx, metav1.ListOptions{LabelSelector: applicationLabel})
	if err != nil {
		return nil, fmt.Errorf("listing Argo CD applications: %w", err)
	}
	var rollouts []unstructured.Unstructured
	if list, err := dc.Resource(rolloutGVR).List(ctx, metav1.ListOptions{}); err == nil {
		rollouts = list.Items
	}
	var xrs []unstructured.Unstructured
	if list, err := dc.Resource(appXRGVR).Namespace(globals.AdharSystemNamespace).List(ctx, metav1.ListOptions{}); err == nil {
		xrs = list.Items
	}
	return customAppRows(argoApps.Items, rollouts, xrs, names, namespace, time.Now()), nil
}

// customAppRows is the pure half: one row per application per environment,
// plus a Pending row for an Application XR that has not composed its Argo CD
// apps yet, so a newly created app is never invisible.
func customAppRows(argoApps, rollouts, xrs []unstructured.Unstructured, names []string, namespace string, now time.Time) []ApplicationInfo {
	rolloutAt := map[string]unstructured.Unstructured{}
	for _, r := range rollouts {
		rolloutAt[r.GetNamespace()+"/"+r.GetName()] = r
	}
	want := func(app, ns string) bool {
		if len(names) > 0 && !contains(names, app) {
			return false
		}
		return namespace == "" || namespace == ns
	}
	var rows []ApplicationInfo
	seen := map[string]bool{}
	for _, a := range argoApps {
		app := a.GetLabels()[applicationLabel]
		env := a.GetLabels()[environmentLabel]
		ns, _, _ := unstructured.NestedString(a.Object, "spec", "destination", "namespace")
		if app == "" || !want(app, ns) {
			continue
		}
		seen[app] = true
		health, _, _ := unstructured.NestedString(a.Object, "status", "health", "status")
		syncStatus, _, _ := unstructured.NestedString(a.Object, "status", "sync", "status")
		if health == "" || health == "Missing" {
			health = "Pending"
		}
		row := ApplicationInfo{
			Name:         app,
			Namespace:    ns,
			Type:         "Rollout",
			Status:       health,
			CreationTime: a.GetCreationTimestamp().Time,
			LastUpdated:  now,
			Age:          duration.HumanDuration(now.Sub(a.GetCreationTimestamp().Time)),
			Labels:       map[string]string{applicationLabel: app, environmentLabel: env, "argocd.argoproj.io/sync": syncStatus},
		}
		if r, ok := rolloutAt[ns+"/"+app]; ok {
			ready, _, _ := unstructured.NestedInt64(r.Object, "status", "readyReplicas")
			total, _, _ := unstructured.NestedInt64(r.Object, "status", "replicas")
			avail, _, _ := unstructured.NestedInt64(r.Object, "status", "availableReplicas")
			row.Replicas = ReplicaInfo{Ready: int32(ready), Total: int32(total), Available: int32(avail), Unavailable: int32(total - avail)}
			if containers, _, _ := unstructured.NestedSlice(r.Object, "spec", "template", "spec", "containers"); len(containers) > 0 {
				if c, ok := containers[0].(map[string]interface{}); ok {
					if img, ok := c["image"].(string); ok {
						row.Images = []string{img}
					}
				}
			}
		}
		rows = append(rows, row)
	}
	for _, x := range xrs {
		app := x.GetName()
		if seen[app] || !want(app, "") || namespace != "" {
			continue
		}
		rows = append(rows, ApplicationInfo{
			Name:         app,
			Namespace:    "-",
			Type:         "Application",
			Status:       "Pending",
			CreationTime: x.GetCreationTimestamp().Time,
			LastUpdated:  now,
			Age:          duration.HumanDuration(now.Sub(x.GetCreationTimestamp().Time)),
		})
	}
	order := map[string]int{}
	for i, e := range globals.PlatformEnvironments() {
		order[e] = i
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return order[rows[i].Namespace] < order[rows[j].Namespace]
	})
	return rows
}
