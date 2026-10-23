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
	"io/fs"
	"sync"

	"github.com/spf13/cobra"

	adhar "adhar-io/adhar"
	"adhar-io/adhar/globals"
)

var (
	treeMu      sync.Mutex
	commandTree *cobra.Command

	currentOnce   sync.Once
	currentBundle *Bundle
	currentErr    error
)

// SetCommandTree registers the binary's root command so the bundle can carry
// its reference. cmd/ calls it; the controller, which cannot import cmd/
// without a cycle, only reads it.
func SetCommandTree(root *cobra.Command) {
	treeMu.Lock()
	defer treeMu.Unlock()
	commandTree = root
}

func registeredTree() *cobra.Command {
	treeMu.Lock()
	defer treeMu.Unlock()
	return commandTree
}

// Assemble is the corpus for a release: the embedded documentation plus the
// CLI reference rendered from root (nil leaves the reference out).
func Assemble(docs fs.FS, root *cobra.Command, rel Release) (*Bundle, error) {
	files, err := FromFS(docs)
	if err != nil {
		return nil, err
	}
	files = append(files, CommandReference(root)...)
	files = append(files, File{Path: ReleaseFile, Data: []byte(releaseDocument(rel))})
	return Pack(files, rel, PartBytes, MaxParts)
}

// ReleaseFile names the document that says which release the bundle is. It is
// a document and not only manifest metadata for two reasons: the AI can then
// answer "which Adhar version is this platform running", and it makes the
// bundle's digest change on every release even when no doc did, so an
// upgraded controller always re-publishes.
const ReleaseFile = "release.md"

func releaseDocument(rel Release) string {
	return fmt.Sprintf(`# Adhar release %[1]s

This platform runs Adhar %[1]s (commit %[2]s, built %[3]s). The platform
documentation, examples, changelog and `+"`adhar`"+` CLI reference in the
knowledge base are the ones shipped with this release, published into the
cluster by the platform controller of this version.
`, rel.Version, rel.Commit, rel.BuildDate)
}

// Current is this binary's bundle, built once: neither the embedded docs nor
// the command tree change while the process runs.
func Current() (*Bundle, error) {
	currentOnce.Do(func() {
		currentBundle, currentErr = Assemble(adhar.KnowledgeFS, registeredTree(), Release{
			Version:   globals.Version,
			Commit:    globals.GitCommit,
			BuildDate: globals.BuildDate,
		})
	})
	return currentBundle, currentErr
}
