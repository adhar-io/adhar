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
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/knowledge"
)

// reconcileKnowledgeBundle publishes the platform knowledge bundle — this
// release's docs, examples, changelog and CLI reference — as the
// `adhar-ai-knowledge-<n>` ConfigMaps the AI runtime mounts.
//
// It replaces the Gitea mirror of github.com/adhar-io/adhar that bootstrap
// kept until 2026-10-23: that needed egress, tracked upstream main rather than
// the release the cluster runs, and put a repository nobody deploys from into
// the platform org. Whichever controller is running publishes ITS bundle, so an
// upgraded controller re-publishes on its first pass and the AI then reads the
// docs of the version it is actually answering about.
//
// Cheap when nothing changed: one cached List, and a write only when the set
// of parts or their digest differs from what is published. Best effort — the
// caller logs a failure and the next pass retries.
func (r *AdharPlatformReconciler) reconcileKnowledgeBundle(ctx context.Context) error {
	bundle := r.KnowledgeBundle
	if bundle == nil {
		b, err := knowledge.Current()
		if err != nil {
			return fmt.Errorf("building the knowledge bundle: %w", err)
		}
		bundle = b
	}
	desired, err := knowledge.ConfigMaps(bundle, globals.AdharSystemNamespace)
	if err != nil {
		return err
	}

	existing := &corev1.ConfigMapList{}
	if err := r.List(ctx, existing, client.InNamespace(globals.AdharSystemNamespace),
		client.MatchingLabels{knowledge.LabelBundle: "true"}); err != nil {
		return fmt.Errorf("listing knowledge ConfigMaps: %w", err)
	}
	published := map[string]*corev1.ConfigMap{}
	for i := range existing.Items {
		published[existing.Items[i].Name] = &existing.Items[i]
	}

	current := len(published) == len(desired)
	for _, cm := range desired {
		have, ok := published[cm.Name]
		if !ok || have.Annotations[knowledge.AnnotationDigest] != bundle.Manifest.Digest {
			current = false
			break
		}
	}
	if current {
		return nil
	}

	// Parts after the first go out first and part 0 — which carries the
	// manifest — last, so a reader that sees the new manifest finds its parts
	// already written. The reader verifies every part's digest regardless.
	order := append(append([]*corev1.ConfigMap{}, desired[1:]...), desired[:1]...)
	for _, cm := range order {
		if have, ok := published[cm.Name]; ok {
			have.Labels, have.Annotations = cm.Labels, cm.Annotations
			have.Data, have.BinaryData = cm.Data, cm.BinaryData
			if err := r.Update(ctx, have); err != nil {
				return fmt.Errorf("updating %s: %w", cm.Name, err)
			}
			continue
		}
		if err := r.Create(ctx, cm); err != nil && !k8serrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating %s: %w", cm.Name, err)
		}
	}

	// A smaller bundle leaves higher-numbered parts behind.
	wanted := map[string]bool{}
	for _, cm := range desired {
		wanted[cm.Name] = true
	}
	for name, cm := range published {
		if wanted[name] {
			continue
		}
		if err := r.Delete(ctx, cm); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("deleting stale %s: %w", name, err)
		}
	}

	log.FromContext(ctx).Info("Published the platform knowledge bundle for the AI knowledge base",
		"version", bundle.Manifest.Version, "digest", bundle.Manifest.Digest,
		"files", len(bundle.Manifest.Files), "bytes", bundle.Manifest.Size, "parts", len(desired))
	return nil
}
