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
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/api/v1alpha1"
)

// argoTLSClientConfig mirrors ArgoCD's cluster-secret TLS block. Byte slices
// marshal to base64 strings, which is exactly the format ArgoCD expects.
type argoTLSClientConfig struct {
	Insecure bool   `json:"insecure"`
	CAData   []byte `json:"caData,omitempty"`
	CertData []byte `json:"certData,omitempty"`
	KeyData  []byte `json:"keyData,omitempty"`
}

// argoClusterConfig mirrors ArgoCD's cluster-secret `config` JSON.
type argoClusterConfig struct {
	BearerToken     string              `json:"bearerToken,omitempty"`
	TLSClientConfig argoTLSClientConfig `json:"tlsClientConfig"`
}

// ensureArgoRegistration creates/patches the ArgoCD cluster Secret for this
// data plane (label `argocd.argoproj.io/secret-type: cluster`), stamping the
// placement labels so ApplicationSet generators and Sveltos can select it.
// Idempotent server-side apply with the `adhar` field manager. Returns the
// ArgoCD cluster name.
func (r *DataPlaneReconciler) ensureArgoRegistration(ctx context.Context, dp *v1alpha1.DataPlane, _ client.Client) (string, error) {
	name, ns := r.resolveKubeconfigRef(dp)
	server, cfgJSON, err := r.argoClusterConfigFromSecret(ctx, name, ns)
	if err != nil {
		return "", err
	}

	labels := map[string]string{
		argoClusterSecretTypeLabel: argoClusterSecretTypeValue,
		dataPlaneLabelKey:          dp.Name,
	}
	for k, v := range dp.Spec.Placement.Labels {
		labels[k] = v
	}

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      argoClusterSecretName(dp),
			Namespace: controlPlaneNamespace,
			Labels:    labels,
		},
		Data: map[string][]byte{
			keyName:  []byte(dp.Name),
			"server": []byte(server),
			"config": cfgJSON,
		},
	}

	if err := ssaApply(ctx, r.Client, secret); err != nil {
		return "", fmt.Errorf("applying ArgoCD cluster secret: %w", err)
	}
	return dp.Name, nil
}

// deleteArgoRegistration removes the ArgoCD cluster Secret during finalize.
// Tolerant of an already-deleted secret.
func (r *DataPlaneReconciler) deleteArgoRegistration(ctx context.Context, dp *v1alpha1.DataPlane) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: argoClusterSecretName(dp), Namespace: controlPlaneNamespace},
	}
	if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// argoClusterConfigFromSecret reads the data-plane kubeconfig secret and derives
// the ArgoCD cluster server URL and `config` JSON (CA/token/client cert).
func (r *DataPlaneReconciler) argoClusterConfigFromSecret(ctx context.Context, name, ns string) (string, []byte, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, secret); err != nil {
		return "", nil, fmt.Errorf("getting kubeconfig secret %s/%s: %w", ns, name, err)
	}
	raw := kubeconfigBytes(secret)
	if len(raw) == 0 {
		return "", nil, fmt.Errorf("kubeconfig secret %s/%s has no usable data", ns, name)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return "", nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}

	cfg := argoClusterConfig{
		BearerToken: restCfg.BearerToken,
		TLSClientConfig: argoTLSClientConfig{
			Insecure: restCfg.Insecure,
			CAData:   restCfg.CAData,
			CertData: restCfg.CertData,
			KeyData:  restCfg.KeyData,
		},
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("marshaling ArgoCD cluster config: %w", err)
	}
	return restCfg.Host, cfgJSON, nil
}

// resolveKubeconfigRef returns the name/namespace of the kubeconfig secret for a
// data plane — the referenced secret for adopt, else the controller-published
// `<dp>-kubeconfig` in the control-plane namespace.
func (r *DataPlaneReconciler) resolveKubeconfigRef(dp *v1alpha1.DataPlane) (string, string) {
	if dp.Spec.Infrastructure.Mode == v1alpha1.InfraModeAdopt && dp.Spec.Infrastructure.KubeconfigSecretRef != nil {
		ref := dp.Spec.Infrastructure.KubeconfigSecretRef
		ns := ref.Namespace
		if ns == "" {
			ns = controlPlaneNamespace
		}
		return ref.Name, ns
	}
	return dp.Name + "-kubeconfig", controlPlaneNamespace
}

// argoClusterSecretName is the deterministic name of the ArgoCD cluster secret.
func argoClusterSecretName(dp *v1alpha1.DataPlane) string {
	return "dp-" + dp.Name + "-cluster"
}
