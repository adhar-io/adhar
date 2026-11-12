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
	"maps"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/globals"
)

// reconcileCIStorageClass publishes globals.CIStorageClass (see its comment):
// a copy of the network block class when the cluster has one (on an SSD tier,
// see ciStorageParameters), otherwise of the default class. StorageClass
// parameters are immutable, so a class created before the block class existed,
// or on a slower tier, is replaced once nothing uses it.
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
	want := ciStorageClassFrom(source)
	if current.Provisioner == want.Provisioner && maps.Equal(current.Parameters, want.Parameters) {
		return nil
	}
	inUse, err := r.storageClassInUse(ctx, globals.CIStorageClass)
	if err != nil || inUse {
		return err
	}
	if err := r.Delete(ctx, current); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("replacing StorageClass %s: %w", globals.CIStorageClass, err)
	}
	log.FromContext(ctx).Info("Rebuilt the CI StorageClass on the network block class",
		"from", current.Provisioner, "to", want.Provisioner, "parameters", want.Parameters)
	return r.Create(ctx, want)
}

// ciStorageParameters are the source class's parameters with an HDD disk tier
// raised to SSD. A CI workspace is a few GiB that live for one run, and what
// it needs is IOPS, not capacity: every forked test JVM loads its jars from
// the workspace's .m2. On GCP the block class follows the NODE disk type and
// can be pd-standard (0.75 read IOPS per GB — three IOPS for the 4 GiB claim),
// on which adhar-kit's suite ran 100-1000× slower than on a laptop and hit the
// one-hour TaskRun timeout at module 18 of 30 (2026-11-11). pd-balanced has a
// 3,000-IOPS baseline at any size, and the few GiB it costs against
// SSD_TOTAL_GB are nothing next to the node disks that quota exists to protect.
func ciStorageParameters(source *storagev1.StorageClass) map[string]string {
	params := maps.Clone(source.Parameters)
	if source.Provisioner == "pd.csi.storage.gke.io" && params["type"] == "pd-standard" {
		params["type"] = "pd-balanced"
	}
	return params
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
		Parameters:           ciStorageParameters(source),
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
