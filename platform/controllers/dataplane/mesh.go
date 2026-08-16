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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/api/v1alpha1"
)

// ciliumCLIImage runs the clustermesh connect. Kept as a var so a pinned
// digest can replace it once mesh wiring is finalised (follow-up).
var ciliumCLIImage = "quay.io/cilium/cilium-cli:latest"

// ensureMesh runs the Cilium clustermesh connect as a Job on the control plane,
// joining the data plane to the mesh. Idempotent SSA; returns true once the Job
// is applied. Tolerant — the real `clustermesh status` pre-check and SPIFFE
// trust-domain registration are follow-ups.
func (r *DataPlaneReconciler) ensureMesh(ctx context.Context, dp *v1alpha1.DataPlane, _ client.Client) (bool, error) {
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "clustermesh-connect-" + dp.Name,
			Namespace: controlPlaneNamespace,
			Labels: map[string]string{
				dataPlaneLabelKey:    dp.Name,
				"adhar.io/component": "clustermesh-connect",
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{dataPlaneLabelKey: dp.Name},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyOnFailure,
					ServiceAccountName: "cilium-cli",
					Containers: []corev1.Container{{
						Name:  "clustermesh-connect",
						Image: ciliumCLIImage,
						Command: []string{"cilium", "clustermesh", "connect",
							"--context", "mgmt",
							"--destination-context", dp.Name},
					}},
				},
			},
		},
	}

	if err := ssaApply(ctx, r.Client, job); err != nil {
		return false, fmt.Errorf("applying clustermesh connect job: %w", err)
	}
	return true, nil
}
