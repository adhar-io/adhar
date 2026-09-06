package adharplatform

import (
	"strings"

	"sigs.k8s.io/yaml"
)

// dropManifestDocument removes the YAML document with the given kind and
// metadata.name from a multi-document manifest, leaving every other document
// byte-for-byte intact. Used for create-once resources (e.g. the Gitea admin
// credential Secret) that must not be re-applied over live state.
func dropManifestDocument(manifest []byte, kind, name string) []byte {
	docs := strings.Split(string(manifest), "\n---")
	kept := make([]string, 0, len(docs))
	for _, doc := range docs {
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &meta); err == nil && meta.Kind == kind && meta.Metadata.Name == name {
			continue
		}
		kept = append(kept, doc)
	}
	return []byte(strings.Join(kept, "\n---"))
}
