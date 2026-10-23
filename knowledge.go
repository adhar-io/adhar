// Package adhar carries the platform's own documentation inside the binary.
//
// It exists for one reason: the in-cluster AI knowledge base must be able to
// read the docs, the examples and the changelog of the release the cluster is
// actually running, without network egress and without a Gitea mirror of this
// repository (which bootstrap kept until 2026-10-23 for exactly this). The
// controller turns this file system plus the live Cobra command tree into a
// versioned bundle (platform/knowledge) and publishes it into the cluster.
//
// go:embed cannot reach outside the directory of the file that declares it,
// and README.md, CHANGELOG.md, docs/ and examples/ all live at the repository
// root — hence a package at the module root. Only text is embedded: the images
// under docs/ are of no use to a text index and would only grow the binary.
package adhar

import "embed"

// KnowledgeFS is the documentation shipped with this release. A new directory
// of Markdown under docs/ must be added to the pattern list; the test beside
// this file fails until it is.
//
//go:embed README.md CHANGELOG.md docs/*.md docs/adr/*.md docs/design/*.md docs/images/branding/*.md examples/*.md examples/*.yaml
var KnowledgeFS embed.FS
