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
// plane so its Alloy agent knows the hub identity and ingest endpoints. Uses the
// data-plane client; tolerates a nil client (infra not yet reachable).
func (r *DataPlaneReconciler) ensureObservability(ctx context.Context, dp *v1alpha1.DataPlane, kube client.Client) error {
	if kube == nil {
		return nil
	}

	hub := dp.Spec.Observability.Hub
	if hub == "" {
		hub = defaultObservabilityHub
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
			"hub":       hub,
			"cluster":   dp.Name,
			"mimirURL":  fmt.Sprintf("https://mimir.%s/api/v1/push", hub),
			"lokiURL":   fmt.Sprintf("https://loki.%s/loki/api/v1/push", hub),
			"tempoURL":  fmt.Sprintf("https://tempo.%s/otlp", hub),
			"profile":   string(dp.Spec.Profile),
			"generated": "adhar-dataplane-controller",
		},
	}

	if err := ssaApply(ctx, kube, cm); err != nil {
		return fmt.Errorf("applying observability-hub ConfigMap on data plane: %w", err)
	}
	return nil
}
