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

package dataplane

// Guards for fleet registration. Each one is a way this could silently do
// nothing, which is the state Karmada was already in: installed, running, and
// managing an empty fleet.

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
)

// memberKubeconfig is a data plane's kubeconfig as the controller publishes it:
// a ServiceAccount bearer token, which is what Karmada push mode needs.
const memberKubeconfig = `apiVersion: v1
kind: Config
clusters:
  - name: dp
    cluster:
      server: https://10.1.2.3:6443
      certificate-authority-data: dGVzdC1jYQ==
contexts:
  - name: dp
    context: {cluster: dp, user: dp}
current-context: dp
users:
  - name: dp
    user:
      token: sa-token-xyz
`

// certOnlyKubeconfig authenticates with a client certificate and therefore
// carries no bearer token.
const certOnlyKubeconfig = `apiVersion: v1
kind: Config
clusters:
  - name: dp
    cluster:
      server: https://10.1.2.3:6443
      certificate-authority-data: dGVzdC1jYQ==
contexts:
  - name: dp
    context: {cluster: dp, user: dp}
current-context: dp
users:
  - name: dp
    user:
      client-certificate-data: dGVzdC1jcnQ=
      client-key-data: dGVzdC1rZXk=
`

func testDataPlane() *v1alpha1.DataPlane {
	return &v1alpha1.DataPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "payments"},
		Spec: v1alpha1.DataPlaneSpec{
			Infrastructure: v1alpha1.DataPlaneInfrastructure{Mode: v1alpha1.InfraModeAdopt},
			Placement: v1alpha1.DataPlanePlacement{
				Labels: map[string]string{"tier": "production", "adhar.io/team": "payments"},
			},
		},
	}
}

// hostObjects are the Secrets the control plane holds: the plane's kubeconfig
// and (optionally) the fleet admin kubeconfig.
func hostObjects(memberCfg string, withKarmada bool) []client.Object {
	objs := []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "payments-kubeconfig", Namespace: controlPlaneNamespace},
			Data:       map[string][]byte{"kubeconfig": []byte(memberCfg)},
		},
	}
	if withKarmada {
		objs = append(objs, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: karmadaKubeconfigSecret, Namespace: controlPlaneNamespace},
			Data: map[string][]byte{karmadaKubeconfigKey: []byte(`apiVersion: v1
kind: Config
clusters: [{name: fleet, cluster: {server: https://karmada.adhar-system.svc:5443, insecure-skip-tls-verify: true}}]
contexts: [{name: fleet, context: {cluster: fleet, user: fleet}}]
current-context: fleet
users: [{name: fleet, user: {token: fleet-admin}}]
`)},
		})
	}
	return objs
}

// fleetScheme can hold the unstructured Karmada Cluster plus core objects.
func fleetScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// newReconciler wires a host fake client and a fake "fleet apiserver".
func newReconciler(t *testing.T, memberCfg string, withKarmada bool) (*DataPlaneReconciler, client.Client) {
	t.Helper()
	host := fake.NewClientBuilder().WithObjects(hostObjects(memberCfg, withKarmada)...).Build()
	fleet := fake.NewClientBuilder().
		WithScheme(fleetScheme(t)).
		WithRESTMapper(nil).
		Build()
	r := &DataPlaneReconciler{Client: host}
	r.FleetClientFor = func(*rest.Config) (client.Client, error) { return fleet, nil }
	return r, fleet
}

// A platform with no fleet hub is a supported shape — `karmada` is disabled in
// the local profile. Registration must then be a no-op, NOT an error that
// stalls every data plane's reconcile at phase 2b.
func TestFleetRegistrationIsSkippedWhenKarmadaIsNotInstalled(t *testing.T) {
	r, _ := newReconciler(t, memberKubeconfig, false)
	name, ready, err := r.ensureKarmadaRegistration(context.Background(), testDataPlane())
	if err != nil {
		t.Fatalf("expected no error without Karmada, got %v", err)
	}
	if name != "" || ready {
		t.Errorf("expected no fleet membership without Karmada, got name=%q ready=%v", name, ready)
	}
}

// The whole point of registering: a PropagationPolicy selects members by label,
// so the member must carry the SAME labels the ArgoCD cluster secret does. A
// member registered without them is invisible to every policy ever written —
// which looks identical to Karmada working.
func TestFleetMemberCarriesThePlacementLabels(t *testing.T) {
	r, fleet := newReconciler(t, memberKubeconfig, true)
	dp := testDataPlane()

	name, _, err := r.ensureKarmadaRegistration(context.Background(), dp)
	if err != nil {
		t.Fatalf("registering: %v", err)
	}
	if name != "payments" {
		t.Fatalf("member name = %q, want payments", name)
	}

	member := &unstructured.Unstructured{}
	member.SetGroupVersionKind(karmadaClusterGVK)
	if err := fleet.Get(context.Background(), types.NamespacedName{Name: "payments"}, member); err != nil {
		t.Fatalf("the fleet has no member cluster: %v", err)
	}

	labels := member.GetLabels()
	for key, want := range map[string]string{
		"tier":                "production",
		"adhar.io/team":       "payments",
		dataPlaneLabelKey:     "payments",
		clusterLabelKey:       "payments",
		dataPlaneModeLabelKey: string(v1alpha1.InfraModeAdopt),
	} {
		if labels[key] != want {
			t.Errorf("member label %s = %q, want %q — a policy selecting on it would never match this plane",
				key, labels[key], want)
		}
	}
}

// Push mode, the endpoint, and a secretRef that resolves. Any one of these
// wrong and the hub registers a member it can never reach.
func TestFleetMemberIsRegisteredInPushMode(t *testing.T) {
	r, fleet := newReconciler(t, memberKubeconfig, true)
	if _, _, err := r.ensureKarmadaRegistration(context.Background(), testDataPlane()); err != nil {
		t.Fatalf("registering: %v", err)
	}

	member := &unstructured.Unstructured{}
	member.SetGroupVersionKind(karmadaClusterGVK)
	if err := fleet.Get(context.Background(), types.NamespacedName{Name: "payments"}, member); err != nil {
		t.Fatalf("getting the member: %v", err)
	}
	spec, _, _ := unstructured.NestedMap(member.Object, "spec")
	if spec["syncMode"] != karmadaSyncModePush {
		t.Errorf("syncMode = %v, want Push — pull mode would put a fleet-API credential on the data plane",
			spec["syncMode"])
	}
	if spec["apiEndpoint"] != "https://10.1.2.3:6443" {
		t.Errorf("apiEndpoint = %v, want the plane's API server", spec["apiEndpoint"])
	}
	ref, _ := spec["secretRef"].(map[string]any)
	if ref["namespace"] != karmadaClusterNamespace || ref["name"] != "payments" {
		t.Errorf("secretRef = %v, want %s/payments", ref, karmadaClusterNamespace)
	}

	// And the credential it points at must actually be there, with the two keys
	// Karmada reads.
	credential := &corev1.Secret{}
	if err := fleet.Get(context.Background(),
		types.NamespacedName{Name: "payments", Namespace: karmadaClusterNamespace}, credential); err != nil {
		t.Fatalf("secretRef points at a Secret that was never created: %v", err)
	}
	if got := string(credential.Data["token"]); got != "sa-token-xyz" {
		t.Errorf("credential token = %q, want the plane's ServiceAccount token", got)
	}
	if len(credential.Data["caBundle"]) == 0 {
		t.Error("credential has no caBundle; the hub would have to skip TLS verification to reach the plane")
	}
}

// A client-certificate kubeconfig has no bearer token, and Karmada push mode
// authenticates with one. Registering anyway produces a member the hub reports
// as NotReady with an authentication error on the fleet side — far from the
// cause. Fail here, where the kubeconfig is in hand.
func TestFleetRegistrationRefusesAKubeconfigWithNoToken(t *testing.T) {
	r, _ := newReconciler(t, certOnlyKubeconfig, true)
	_, _, err := r.ensureKarmadaRegistration(context.Background(), testDataPlane())
	if err == nil {
		t.Fatal("expected a refusal for a kubeconfig with no bearer token")
	}
	if !strings.Contains(err.Error(), "bearer token") {
		t.Errorf("error should name the missing bearer token, got: %v", err)
	}
}

// Finalize must not be able to wedge a DataPlane just because the fleet hub is
// gone — uninstalling Karmada would otherwise make every plane undeletable.
func TestFleetDeregistrationToleratesAMissingFleet(t *testing.T) {
	r, _ := newReconciler(t, memberKubeconfig, false)
	if err := r.deleteKarmadaRegistration(context.Background(), testDataPlane()); err != nil {
		t.Errorf("deregistering without a fleet hub must be a no-op, got %v", err)
	}
}
