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
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/globals"
)

// reconcileCIStorageClass publishes globals.CIStorageClass (see its comment):
// a copy of the network block class when the cluster has one, otherwise of the
// default class. StorageClass parameters are immutable, so a class created
// before the block class existed is replaced once nothing uses it.
func (r *AdharPlatformReconciler) reconcileCIStorageClass(ctx context.Context) error {
	source, err := r.ciStorageSource(ctx)
	if err != nil || source == nil {
		return err
	}
	current := &storagev1.StorageClass{}
	err = r.Get(ctx, client.ObjectKey{Name: globals.CIStorageClass}, current)
	switch {
	case k8serrors.IsNotFound(err):
		return r.Create(ctx, ciStorageClassFrom(source))
	case err != nil:
		return fmt.Errorf("reading StorageClass %s: %w", globals.CIStorageClass, err)
	}
	if current.Provisioner == source.Provisioner {
		return nil
	}
	inUse, err := r.storageClassInUse(ctx, globals.CIStorageClass)
	if err != nil || inUse {
		return err
	}
	if err := r.Delete(ctx, current); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("replacing StorageClass %s: %w", globals.CIStorageClass, err)
	}
	log.FromContext(ctx).Info("Rebuilt the CI StorageClass on the network block class", "from", current.Provisioner, "to", source.Provisioner)
	return r.Create(ctx, ciStorageClassFrom(source))
}

// ciStorageSource is the class to copy: the block class, else the default one.
func (r *AdharPlatformReconciler) ciStorageSource(ctx context.Context) (*storagev1.StorageClass, error) {
	classes := &storagev1.StorageClassList{}
	if err := r.List(ctx, classes); err != nil {
		return nil, fmt.Errorf("listing StorageClasses: %w", err)
	}
	var fallback *storagev1.StorageClass
	for i := range classes.Items {
		sc := &classes.Items[i]
		switch {
		case sc.Name == globals.BlockStorageClass:
			return sc, nil
		case sc.Annotations[defaultStorageClassAnnotation] == "true":
			fallback = sc
		case sc.Name == globals.DefaultStorageClass && fallback == nil:
			fallback = sc
		}
	}
	return fallback, nil
}

func ciStorageClassFrom(source *storagev1.StorageClass) *storagev1.StorageClass {
	reclaim := corev1.PersistentVolumeReclaimDelete
	binding := storagev1.VolumeBindingWaitForFirstConsumer
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: globals.CIStorageClass,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "adhar",
				"adhar.io/storage-source":      source.Name,
			},
		},
		Provisioner:          source.Provisioner,
		Parameters:           source.Parameters,
		MountOptions:         source.MountOptions,
		AllowVolumeExpansion: source.AllowVolumeExpansion,
		ReclaimPolicy:        &reclaim,
		VolumeBindingMode:    &binding,
	}
}

func (r *AdharPlatformReconciler) storageClassInUse(ctx context.Context, name string) (bool, error) {
	claims := &corev1.PersistentVolumeClaimList{}
	if err := r.List(ctx, claims); err != nil {
		return false, fmt.Errorf("listing PersistentVolumeClaims: %w", err)
	}
	for _, c := range claims.Items {
		if c.Spec.StorageClassName != nil && *c.Spec.StorageClassName == name {
			return true, nil
		}
	}
	return false, nil
}
