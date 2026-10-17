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

package adharplatform

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	provider "adhar-io/adhar/platform/providers"
)

// The default StorageClass on a MANAGED cluster whose cloud volumes are quota'd.
//
// On every self-managed cloud the platform makes node-local storage the default
// (provider.StepNodeLocalStorageClass): a cloud block volume costs an attach
// slot, and the enabled packages ask for ~90 PersistentVolumeClaims. Civo's
// managed k3s has the same wall in a different place — an ACCOUNT quota of 40
// volumes — and the step could not run there because it works over SSH and a
// managed node has none. So a Civo managed bring-up kept `civo-volume` as the
// default and would have stalled with Pending claims after the first wave of
// applications (mum1, 2026-10-17). This applies the same manifest through the
// API, on every pass (idempotent), and demotes the cloud's class to opt-in:
// anything that needs a real block device names it explicitly.

const defaultStorageClassAnnotation = "storageclass.kubernetes.io/is-default-class"

// managedNeedsNodeLocalStorage reports whether this managed cluster's cloud
// volumes are quota'd tightly enough that node-local storage must be the
// default. Today: Civo.
func managedNeedsNodeLocalStorage(providerName, clusterMode string) bool {
	return providerName == civoProviderName && clusterMode == "managed"
}

func (r *AdharPlatformReconciler) reconcileManagedNodeLocalStorage(ctx context.Context, resource *v1alpha1.AdharPlatform) error {
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKey{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace}, cm); err != nil {
		return nil // no spec: nothing is known about the cloud
	}
	if !managedNeedsNodeLocalStorage(cm.Data["provider"], cm.Data[globals.ClusterSpecClusterModeKey]) {
		return nil
	}
	if err := r.applyManifest(ctx, []byte(provider.NodeLocalStorageManifest(globals.DefaultStorageClass)), resource, "node-local storage"); err != nil {
		return fmt.Errorf("applying the node-local StorageClass: %w", err)
	}
	classes := &storagev1.StorageClassList{}
	if err := r.List(ctx, classes); err != nil {
		return fmt.Errorf("listing StorageClasses: %w", err)
	}
	for i := range classes.Items {
		sc := &classes.Items[i]
		if sc.Name == globals.DefaultStorageClass || sc.Annotations[defaultStorageClassAnnotation] != "true" {
			continue
		}
		patch := client.MergeFrom(sc.DeepCopy())
		sc.Annotations[defaultStorageClassAnnotation] = "false"
		if err := r.Patch(ctx, sc, patch); err != nil {
			return fmt.Errorf("demoting StorageClass %s from default: %w", sc.Name, err)
		}
		log.FromContext(ctx).Info("Node-local storage is the default; the cloud volume class is opt-in", "default", globals.DefaultStorageClass, "demoted", sc.Name)
	}
	return nil
}
