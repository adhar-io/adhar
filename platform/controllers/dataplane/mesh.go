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
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"adhar-io/adhar/api/v1alpha1"
)

// ciliumCLIImage runs the clustermesh connect.
var ciliumCLIImage = "quay.io/cilium/cilium-cli:v0.18.6"

const (
	meshServiceAccount = "clustermesh-connect"
	meshContextMgmt    = "mgmt"
	meshKubeconfigPath = "/kube/config"
)

// ensureMesh joins the data plane to the Cilium Cluster Mesh by running
// `cilium clustermesh connect` as a Job on the control plane with a kubeconfig
// that carries both sides (context "mgmt" = in-cluster ServiceAccount, context
// "<dp>" = the plane's kubeconfig). MeshJoined is reported True only once the
// Job has succeeded — a failed Job surfaces as an error with the Job's own
// failure — so the condition is a statement about the mesh, not about the
// Job object existing.
//
// Prerequisites the Job checks for and reports (it does not paper over them):
// both clusters must run Cilium with distinct cluster IDs sharing the same CA,
// and each must have its clustermesh-apiserver exposed (the management cluster
// ships one in resources/cilium/clustermesh.yaml). A vcluster plane shares the
// host's CNI and has no mesh of its own; mesh.enabled is meaningless there.
func (r *DataPlaneReconciler) ensureMesh(ctx context.Context, dp *v1alpha1.DataPlane, _ client.Client) (bool, error) {
	if err := r.ensureMeshRBAC(ctx, dp); err != nil {
		return false, err
	}
	if err := r.ensureMeshKubeconfig(ctx, dp); err != nil {
		return false, err
	}

	jobName := "clustermesh-connect-" + dp.Name
	existing := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: controlPlaneNamespace}, existing)
	switch {
	case err == nil:
		for _, c := range existing.Status.Conditions {
			if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
				return true, nil
			}
			if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
				return false, fmt.Errorf("clustermesh connect failed: %s (inspect job/%s)", c.Message, jobName)
			}
		}
		return false, nil // still running
	case !apierrors.IsNotFound(err):
		return false, fmt.Errorf("getting clustermesh job: %w", err)
	}

	backoff := int32(3)
	script := fmt.Sprintf(`set -eu
cilium clustermesh status --context %[1]s || { echo "management cluster has no clustermesh-apiserver: apply resources/cilium/clustermesh.yaml first"; exit 1; }
cilium clustermesh connect --context %[1]s --destination-context %[2]s
cilium clustermesh status --context %[1]s --wait --wait-duration 5m`, meshContextMgmt, dp.Name)
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: controlPlaneNamespace,
			Labels: map[string]string{
				dataPlaneLabelKey:    dp.Name,
				"adhar.io/component": "clustermesh-connect",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{dataPlaneLabelKey: dp.Name},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: meshServiceAccount,
					Containers: []corev1.Container{{
						Name:    "clustermesh-connect",
						Image:   ciliumCLIImage,
						Command: []string{"sh", "-c", script},
						Env:     []corev1.EnvVar{{Name: "KUBECONFIG", Value: meshKubeconfigPath}},
						VolumeMounts: []corev1.VolumeMount{{
							Name: "kubeconfig", MountPath: "/kube", ReadOnly: true,
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "kubeconfig",
						VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName: meshKubeconfigSecretName(dp),
							Items:      []corev1.KeyToPath{{Key: "config", Path: "config"}},
						}},
					}},
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(dp, job, r.Scheme); err != nil {
		return false, err
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("creating clustermesh connect job: %w", err)
	}
	return false, nil
}

func meshKubeconfigSecretName(dp *v1alpha1.DataPlane) string {
	return "clustermesh-kubeconfig-" + dp.Name
}

// ensureMeshRBAC creates the ServiceAccount the connect Job runs as, bound to
// cluster-admin on the control plane (the cilium CLI reads Cilium's secrets and
// writes the clustermesh ones).
func (r *DataPlaneReconciler) ensureMeshRBAC(ctx context.Context, dp *v1alpha1.DataPlane) error {
	sa := &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: metav1.ObjectMeta{Name: meshServiceAccount, Namespace: controlPlaneNamespace},
	}
	if err := ssaApply(ctx, r.Client, sa); err != nil {
		return fmt.Errorf("applying clustermesh ServiceAccount: %w", err)
	}
	crb := &rbacv1.ClusterRoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: "adhar-" + meshServiceAccount},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: meshServiceAccount, Namespace: controlPlaneNamespace}},
	}
	if err := ssaApply(ctx, r.Client, crb); err != nil {
		return fmt.Errorf("applying clustermesh ClusterRoleBinding: %w", err)
	}
	_ = dp
	return nil
}

// ensureMeshKubeconfig writes a two-context kubeconfig Secret: "mgmt" uses the
// Job's projected ServiceAccount token against the in-cluster API server, and
// "<dp>" is the data plane's own kubeconfig re-keyed under the plane's name.
func (r *DataPlaneReconciler) ensureMeshKubeconfig(ctx context.Context, dp *v1alpha1.DataPlane) error {
	name, ns := r.resolveKubeconfigRef(dp)
	src := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, src); err != nil {
		return fmt.Errorf("reading data-plane kubeconfig %s/%s: %w", ns, name, err)
	}
	raw := kubeconfigBytes(src)
	if len(raw) == 0 {
		return fmt.Errorf("data-plane kubeconfig %s/%s has no usable data", ns, name)
	}
	dpCfg, err := clientcmd.Load(raw)
	if err != nil {
		return fmt.Errorf("parsing data-plane kubeconfig: %w", err)
	}
	cur := dpCfg.Contexts[dpCfg.CurrentContext]
	if cur == nil {
		return fmt.Errorf("data-plane kubeconfig has no current context")
	}

	merged := clientcmdapi.NewConfig()
	merged.Clusters[meshContextMgmt] = &clientcmdapi.Cluster{
		Server:               "https://kubernetes.default.svc",
		CertificateAuthority: "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
	}
	merged.AuthInfos[meshContextMgmt] = &clientcmdapi.AuthInfo{TokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token"}
	merged.Contexts[meshContextMgmt] = &clientcmdapi.Context{Cluster: meshContextMgmt, AuthInfo: meshContextMgmt}
	merged.Clusters[dp.Name] = dpCfg.Clusters[cur.Cluster]
	merged.AuthInfos[dp.Name] = dpCfg.AuthInfos[cur.AuthInfo]
	merged.Contexts[dp.Name] = &clientcmdapi.Context{Cluster: dp.Name, AuthInfo: dp.Name}
	merged.CurrentContext = meshContextMgmt
	out, err := clientcmd.Write(*merged)
	if err != nil {
		return fmt.Errorf("writing merged kubeconfig: %w", err)
	}

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      meshKubeconfigSecretName(dp),
			Namespace: controlPlaneNamespace,
			Labels:    map[string]string{dataPlaneLabelKey: dp.Name, "adhar.io/component": "clustermesh-connect"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"config": out},
	}
	if err := controllerutil.SetControllerReference(dp, secret, r.Scheme); err != nil {
		return err
	}
	if err := ssaApply(ctx, r.Client, secret); err != nil {
		return fmt.Errorf("applying clustermesh kubeconfig secret: %w", err)
	}
	return nil
}
