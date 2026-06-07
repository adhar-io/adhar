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

package scale

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// resolveNamespace returns the namespace flag value, defaulting to the Adhar
// system namespace when not set.
func resolveNamespace() string {
	if namespace != "" {
		return namespace
	}
	return globals.AdharSystemNamespace
}

// parseTimeout parses the --timeout flag, falling back to 5m on error.
func parseTimeout() time.Duration {
	if timeout == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(timeout)
	if err != nil {
		return 5 * time.Minute
	}
	return d
}

// getClientset builds a clientset using the shared platform helper and prints a
// friendly message when the cluster cannot be reached.
func getClientset() (*kubernetes.Clientset, error) {
	clientset, err := k8s.GetClientset()
	if err != nil {
		fmt.Println(helpers.ErrorStyle.Render("❌ Could not connect to the cluster"))
		fmt.Println(helpers.CreateMuted("   " + err.Error()))
		fmt.Println(helpers.CreateMuted("   Is the cluster running? Try `adhar up` or check your kubeconfig context."))
		return nil, fmt.Errorf("failed to get Kubernetes client: %w", err)
	}
	return clientset, nil
}

// scaleKind identifies whether the target workload is a Deployment or a
// StatefulSet. It returns an error when neither exists.
type scaleKind string

const (
	kindDeployment  scaleKind = "Deployment"
	kindStatefulSet scaleKind = "StatefulSet"
)

// detectWorkload determines whether name refers to a Deployment or StatefulSet
// in the namespace. When resourceType is set ("deployment"/"statefulset") the
// lookup is constrained to that kind.
func detectWorkload(ctx context.Context, clientset *kubernetes.Clientset, ns, name string) (scaleKind, error) {
	wantDeploy := resourceType == "" || resourceType == "deployment" || resourceType == "deploy"
	wantSts := resourceType == "" || resourceType == "statefulset" || resourceType == "sts"

	if wantDeploy {
		if _, err := clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return kindDeployment, nil
		} else if !k8serrors.IsNotFound(err) {
			return "", err
		}
	}
	if wantSts {
		if _, err := clientset.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return kindStatefulSet, nil
		} else if !k8serrors.IsNotFound(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("no Deployment or StatefulSet named %q found in namespace %q", name, ns)
}

// applyReplicas sets the replica count on the resolved workload via the Scale
// subresource and returns the kind that was scaled.
func applyReplicas(ctx context.Context, clientset *kubernetes.Clientset, ns, name string, target int32) (scaleKind, error) {
	kind, err := detectWorkload(ctx, clientset, ns, name)
	if err != nil {
		return "", err
	}

	switch kind {
	case kindDeployment:
		sc, err := clientset.AppsV1().Deployments(ns).GetScale(ctx, name, metav1.GetOptions{})
		if err != nil {
			return kind, fmt.Errorf("getting scale for deployment %s/%s: %w", ns, name, err)
		}
		sc.Spec.Replicas = target
		if _, err := clientset.AppsV1().Deployments(ns).UpdateScale(ctx, name, sc, metav1.UpdateOptions{}); err != nil {
			return kind, fmt.Errorf("scaling deployment %s/%s: %w", ns, name, err)
		}
	case kindStatefulSet:
		sc, err := clientset.AppsV1().StatefulSets(ns).GetScale(ctx, name, metav1.GetOptions{})
		if err != nil {
			return kind, fmt.Errorf("getting scale for statefulset %s/%s: %w", ns, name, err)
		}
		sc.Spec.Replicas = target
		if _, err := clientset.AppsV1().StatefulSets(ns).UpdateScale(ctx, name, sc, metav1.UpdateOptions{}); err != nil {
			return kind, fmt.Errorf("scaling statefulset %s/%s: %w", ns, name, err)
		}
	}
	return kind, nil
}

// ensureHPA creates or updates a HorizontalPodAutoscaler targeting the named
// workload with the given min/max replicas and CPU utilization target.
func ensureHPA(ctx context.Context, clientset *kubernetes.Clientset, ns, name string, kind scaleKind, minR, maxR, cpu int32) (string, error) {
	hpaClient := clientset.AutoscalingV2().HorizontalPodAutoscalers(ns)

	desired := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"adhar.io/managed-by": "adhar-scale"},
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       string(kind),
				Name:       name,
			},
			MinReplicas: &minR,
			MaxReplicas: maxR,
			Metrics: []autoscalingv2.MetricSpec{
				{
					Type: autoscalingv2.ResourceMetricSourceType,
					Resource: &autoscalingv2.ResourceMetricSource{
						Name: "cpu",
						Target: autoscalingv2.MetricTarget{
							Type:               autoscalingv2.UtilizationMetricType,
							AverageUtilization: &cpu,
						},
					},
				},
			},
		},
	}

	existing, err := hpaClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			if _, err := hpaClient.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
				return "", fmt.Errorf("creating HPA %s/%s: %w", ns, name, err)
			}
			return "created", nil
		}
		return "", fmt.Errorf("getting HPA %s/%s: %w", ns, name, err)
	}

	existing.Spec = desired.Spec
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels["adhar.io/managed-by"] = "adhar-scale"
	if _, err := hpaClient.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("updating HPA %s/%s: %w", ns, name, err)
	}
	return "updated", nil
}
