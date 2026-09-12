/*
Copyright 2025.

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

package adharplatform

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
)

// This file used to hold the untouched KubeBuilder Ginkgo scaffold: a
// Describe("AdharPlatform Controller") block whose only assertion was
// `err == nil` on an empty CR, next to a `// TODO: Add more specific
// assertions`. Worse, it never ran at all -- RunSpecs lives in
// platform/controllers/suite_test.go, which is `package controllers`, so specs
// registered in `package adharplatform` were never executed by any test binary.
// It produced the appearance of controller coverage and none of the substance.
//
// What follows tests the decision the scaffold gestured at and the bootstrap
// actually turns on.

// applicationSetGVK matches what isPlatformAlreadyDeployed lists.
var applicationSetGVK = schema.GroupVersionKind{
	Group:   "argoproj.io",
	Version: "v1alpha1",
	Kind:    "ApplicationSet",
}

func readyDeployment(name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: globals.AdharSystemNamespace},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:     1,
			AvailableReplicas: 1,
		},
	}
}

func pendingDeployment(name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: globals.AdharSystemNamespace},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0, AvailableReplicas: 0},
	}
}

func applicationSet(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(applicationSetGVK)
	u.SetName(name)
	u.SetNamespace(globals.AdharSystemNamespace)
	return u
}

func deployedTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	if err := appsv1.AddToScheme(sch); err != nil {
		t.Fatalf("registering appsv1: %v", err)
	}
	// The ApplicationSet CRD is not compiled in; register the list kind the
	// controller queries so the fake client can serve it unstructured.
	sch.AddKnownTypeWithName(applicationSetGVK, &unstructured.Unstructured{})
	sch.AddKnownTypeWithName(applicationSetGVK.GroupVersion().WithKind("ApplicationSetList"),
		&unstructured.UnstructuredList{})
	return sch
}

// isPlatformAlreadyDeployed is the gate that decides whether `adhar up` may
// declare the bootstrap finished and shut the controller down. Every false
// negative here costs a pointless wait; every false POSITIVE ships a
// half-installed platform and reports success, which is the expensive one. Each
// case below removes exactly one required signal.
func TestIsPlatformAlreadyDeployed(t *testing.T) {
	all := func() []client.Object {
		return []client.Object{
			readyDeployment("gitea"),
			readyDeployment("argo-cd-argocd-server"),
			readyDeployment("crossplane"),
			applicationSet("adhar-appset-local"),
		}
	}

	tests := []struct {
		name    string
		objects []client.Object
		want    bool
	}{
		{
			name:    "everything present and ready",
			objects: all(),
			want:    true,
		},
		{
			name:    "nothing deployed at all",
			objects: nil,
			want:    false,
		},
		{
			name: "gitea missing",
			objects: []client.Object{
				readyDeployment("argo-cd-argocd-server"),
				readyDeployment("crossplane"),
				applicationSet("adhar-appset-local"),
			},
			want: false,
		},
		{
			name: "argocd missing",
			objects: []client.Object{
				readyDeployment("gitea"),
				readyDeployment("crossplane"),
				applicationSet("adhar-appset-local"),
			},
			want: false,
		},
		{
			name: "crossplane missing",
			objects: []client.Object{
				readyDeployment("gitea"),
				readyDeployment("argo-cd-argocd-server"),
				applicationSet("adhar-appset-local"),
			},
			want: false,
		},
		{
			name: "a core deployment exists but has no ready replica",
			objects: []client.Object{
				readyDeployment("gitea"),
				pendingDeployment("argo-cd-argocd-server"),
				readyDeployment("crossplane"),
				applicationSet("adhar-appset-local"),
			},
			want: false,
		},
		{
			// The regression that matters most: ArgoCD and Gitea can be up and
			// perfectly healthy while the ApplicationSet was never applied, which
			// is an EMPTY ArgoCD. Declaring the platform deployed there reports
			// success on a cluster with no packages on it.
			name: "core is up but no ApplicationSet was applied",
			objects: []client.Object{
				readyDeployment("gitea"),
				readyDeployment("argo-cd-argocd-server"),
				readyDeployment("crossplane"),
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().
				WithScheme(deployedTestScheme(t)).
				WithObjects(tc.objects...).
				Build()

			r := &AdharPlatformReconciler{Client: cl, Scheme: cl.Scheme()}
			if got := r.isPlatformAlreadyDeployed(context.Background()); got != tc.want {
				t.Fatalf("isPlatformAlreadyDeployed() = %v, want %v", got, tc.want)
			}
		})
	}
}
