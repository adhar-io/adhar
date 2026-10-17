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
	"bytes"
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/globals"
)

// Cilium's Envoy does not read the Gateway's TLS Secret; it reads a COPY that
// Cilium's secret-sync keeps for it (`cilium-sync-secret-<hash>`, labelled
// with the owning Secret's name and namespace). With the sync namespace set to
// the platform namespace — the same namespace the Gateway and its Secret live
// in, ADR-0011 — the sync makes the copy once and never updates it: when
// cert-manager replaced the self-signed bootstrap certificate with the Let's
// Encrypt one, every URL kept serving the old certificate, and restarting
// Envoy changed nothing because Envoy was faithfully serving the copy (Azure,
// 2026-10-17; the same symptom was fixed by hand on the first Azure cluster).
// Deleting the stale copy makes the operator recreate it from the source within
// seconds, which is what this does, on every pass. Without it the first
// renewal would have let the certificate expire in production.

const (
	secretSyncOwnerNameLabel      = "secretsync.cilium.io/owning-secret-name"
	secretSyncOwnerNamespaceLabel = "secretsync.cilium.io/owning-secret-namespace"
)

// reconcileGatewaySecretSync deletes every Cilium-synced Secret copy in the
// platform namespace whose TLS data no longer matches its source Secret.
func (r *AdharPlatformReconciler) reconcileGatewaySecretSync(ctx context.Context) error {
	copies := &corev1.SecretList{}
	if err := r.List(ctx, copies, client.InNamespace(globals.AdharSystemNamespace), client.HasLabels{secretSyncOwnerNameLabel}); err != nil {
		return fmt.Errorf("listing Cilium-synced secrets: %w", err)
	}
	for i := range copies.Items {
		cp := &copies.Items[i]
		srcName := cp.Labels[secretSyncOwnerNameLabel]
		srcNS := cp.Labels[secretSyncOwnerNamespaceLabel]
		if srcNS == "" {
			srcNS = globals.AdharSystemNamespace
		}
		if srcName == "" || (srcName == cp.Name && srcNS == cp.Namespace) {
			continue
		}
		src := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Name: srcName, Namespace: srcNS}, src); err != nil {
			continue // no source: not ours to judge
		}
		if syncedCopyMatches(src, cp) {
			continue
		}
		if err := r.Delete(ctx, cp); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("refreshing stale Cilium-synced secret %s: %w", cp.Name, err)
		}
		log.FromContext(ctx).Info("Refreshed Cilium's synced copy of a Gateway secret that had gone stale", "copy", cp.Name, "source", srcNS+"/"+srcName)
	}
	return nil
}

func syncedCopyMatches(src, cp *corev1.Secret) bool {
	for _, key := range []string{corev1.TLSCertKey, corev1.TLSPrivateKeyKey} {
		if !bytes.Equal(src.Data[key], cp.Data[key]) {
			return false
		}
	}
	return true
}
