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

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/api/v1alpha1"
)

// defaultObservabilityHub is the control-plane mesh identity that stores
// telemetry when a DataPlane does not override it.
const defaultObservabilityHub = "adhar-mgmt"

// ensureObservability applies the `observability-hub` ConfigMap onto the data
// plane so its Alloy agent ships telemetry to the management hub. The keys are
// the contract Alloy's config.alloy reads through envFrom
// (platform/stack/packages/observability/alloy/manifests/hub-endpoints.yaml):
// HUB_MIMIR_URL / HUB_LOKI_URL / HUB_TEMPO_URL / HUB_CLUSTER_NAME. The URLs are
// the hub's external Gateway routes, derived from the control plane's
// AdharPlatform host so nothing here hardcodes a domain. The workload
// ApplicationSet marks fields owned by this controller's field manager as
// ignored (RespectIgnoreDifferences), so ArgoCD's copy of the ConfigMap and
// this one do not fight. Uses the data-plane client; tolerates a nil client
// (infra not yet reachable).
func (r *DataPlaneReconciler) ensureObservability(ctx context.Context, dp *v1alpha1.DataPlane, kube client.Client) error {
	if kube == nil {
		return nil
	}

	hub := dp.Spec.Observability.Hub
	if hub == "" {
		hub = defaultObservabilityHub
	}
	base := r.hubBaseURL(ctx)

	// A freshly provisioned plane has no platform namespace yet (the thin
	// profile's Applications create it later, but this phase may run first).
	// Create it here, labelled as a data plane per ADR-0023.
	ns := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   controlPlaneNamespace,
			Labels: map[string]string{"adhar.io/plane": "data", dataPlaneLabelKey: dp.Name},
		},
	}
	if err := ssaApply(ctx, kube, ns); err != nil {
		return fmt.Errorf("ensuring platform namespace on data plane: %w", err)
	}

	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "observability-hub",
			Namespace: controlPlaneNamespace,
			Labels: map[string]string{
				dataPlaneLabelKey:    dp.Name,
				"adhar.io/component": "observability-hub",
			},
		},
		Data: map[string]string{
			"HUB_MIMIR_URL":    base("mimir") + "/api/v1/push",
			"HUB_LOKI_URL":     base("loki") + "/loki/api/v1/push",
			"HUB_TEMPO_URL":    base("tempo"),
			"HUB_CLUSTER_NAME": dp.Name,
			"hub":              hub,
			"profile":          string(dp.Spec.Profile),
			"generated":        "adhar-dataplane-controller",
		},
	}

	if err := ssaApply(ctx, kube, cm); err != nil {
		return fmt.Errorf("applying observability-hub ConfigMap on data plane: %w", err)
	}
	return nil
}

// hubBaseURL returns a function building https://<service>.<host>[:port] from
// the control plane's AdharPlatform spec (host + port), i.e. the hub's public
// Gateway routes. Falls back to the mesh hub identity when no platform object
// is visible (envtest).
func (r *DataPlaneReconciler) hubBaseURL(ctx context.Context) func(service string) string {
	host, port := "", ""
	platforms := &v1alpha1.AdharPlatformList{}
	if err := r.List(ctx, platforms, client.InNamespace(controlPlaneNamespace)); err == nil && len(platforms.Items) > 0 {
		bc := platforms.Items[0].Spec.BuildCustomization
		host, port = bc.Host, bc.Port
	}
	if host == "" {
		host = defaultObservabilityHub
	}
	suffix := ""
	if port != "" && port != "443" {
		suffix = ":" + port
	}
	return func(service string) string {
		return fmt.Sprintf("https://%s.%s%s", service, host, suffix)
	}
}
