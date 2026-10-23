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

package knowledge

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// How the bundle is delivered: as ConfigMaps in the platform namespace, which
// the AI runtime and its reindex CronJob mount through ONE projected volume.
// A ConfigMap is capped at 1 MiB and binaryData is base64 in the stored
// object, so a part holds at most PartBytes of the compressed bundle; MaxParts
// is the number of ConfigMaps the projected volume names (all optional).
// The ai/adhar-ai manifests must project exactly ConfigMapName(0..MaxParts-1);
// a test in the controller package holds the two together.
const (
	ConfigMapPrefix = "adhar-ai-knowledge"
	ManifestKey     = "manifest.json"
	PartBytes       = 600 * 1024
	MaxParts        = 4

	// MountPath is where the AI runtime mounts the projected volume and what
	// ADHAR_AI_KNOWLEDGE_PATH points at.
	MountPath = "/var/lib/adhar-ai/knowledge"

	LabelBundle      = "adhar.io/knowledge-bundle"
	AnnotationDigest = "adhar.io/knowledge-digest"
	AnnotationVer    = "adhar.io/knowledge-version"
	AnnotationPart   = "adhar.io/knowledge-part"
)

// ConfigMapName is the ConfigMap holding part i.
func ConfigMapName(i int) string { return fmt.Sprintf("%s-%d", ConfigMapPrefix, i) }

// PartKey is the binaryData key (and therefore file name) of part i.
func PartKey(i int) string { return fmt.Sprintf("part-%04d", i) }

// ConfigMaps renders the bundle as ConfigMaps. The manifest rides in part 0.
func ConfigMaps(b *Bundle, namespace string) ([]*corev1.ConfigMap, error) {
	manifest, err := b.JSON()
	if err != nil {
		return nil, err
	}
	out := make([]*corev1.ConfigMap, 0, len(b.Parts))
	for i, part := range b.Parts {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ConfigMapName(i),
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/name":       ConfigMapPrefix,
					"app.kubernetes.io/part-of":    "adhar-ai",
					"app.kubernetes.io/managed-by": "adhar-controller",
					"adhar.io/component":           "ai",
					LabelBundle:                    "true",
				},
				Annotations: map[string]string{
					AnnotationDigest: b.Manifest.Digest,
					AnnotationVer:    b.Manifest.Version,
					AnnotationPart:   strconv.Itoa(i+1) + "/" + strconv.Itoa(len(b.Parts)),
				},
			},
			BinaryData: map[string][]byte{PartKey(i): part},
		}
		if i == 0 {
			cm.Data = map[string]string{ManifestKey: string(manifest)}
		}
		out = append(out, cm)
	}
	return out, nil
}
