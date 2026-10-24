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

// Package knowledge builds the platform knowledge bundle: the documentation,
// examples, changelog and CLI reference of THIS release, packed for the
// adhar-controlplane AI knowledge base.
//
// Until 2026-10-23 bootstrap mirrored github.com/adhar-io/adhar into the
// cluster's Gitea so the indexer could read docs/ and parse cmd/. That needed
// egress from the cluster, put a repository nobody deploys from into the
// platform org (where app-ci and the console's catalogue then had to be taught
// to ignore it), and — the real defect — tracked upstream `main`, not the
// release the cluster runs: an agent on a v0.1.50 cluster read v0.1.53's
// commands. The bundle instead comes out of the binary that runs the
// controller, so it is exactly that release, and needs no network at all.
//
// The CLI reference is rendered from the live Cobra tree rather than parsed out
// of Go source with regular expressions, so it carries flags and the full
// command paths the parser used to guess.
package knowledge

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Schema is the bundle format version. A reader that sees a schema it does not
// know must ignore the bundle rather than misread it.
const Schema = 1

// File is one document in the bundle, at a `/`-separated path.
type File struct {
	Path string
	Data []byte
}

// Release identifies the build the bundle describes.
type Release struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
}

// Part is one slice of the compressed bundle, as stored in one ConfigMap.
type Part struct {
	Key       string `json:"key"`
	ConfigMap string `json:"configMap"`
	SHA256    string `json:"sha256"`
	Size      int    `json:"size"`
}

// Manifest describes a bundle. It travels beside the parts so a reader can
// tell a complete, consistent set of parts from a half-updated one: the
// ConfigMaps are written one at a time, and the kubelet refreshes a mounted
// ConfigMap on its own schedule.
type Manifest struct {
	Schema int `json:"schema"`
	Release
	// Digest is `sha256:<hex>` of the whole compressed bundle (the parts
	// concatenated in order).
	Digest string `json:"digest"`
	Size   int    `json:"size"`
	// Files is every path in the bundle, sorted.
	Files []string `json:"files"`
	Parts []Part   `json:"parts"`
}

// Bundle is the packed corpus: a deterministic tar.gz, split into parts.
type Bundle struct {
	Manifest Manifest
	Parts    [][]byte
}

// JSON is the manifest as published.
func (b *Bundle) JSON() ([]byte, error) {
	return json.MarshalIndent(b.Manifest, "", "  ")
}

// Pack builds a bundle from files. The output depends only on the files and
// the release — paths are sorted and every timestamp is zero — so the same
// release always produces the same digest and an unchanged bundle is never
// re-published.
func Pack(files []File, rel Release, partBytes, maxParts int) (*Bundle, error) {
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	paths := make([]string, 0, len(sorted))
	for i, f := range sorted {
		if f.Path == "" || strings.HasPrefix(f.Path, "/") || strings.Contains(f.Path, "..") {
			return nil, fmt.Errorf("bundle path %q is not a clean relative path", f.Path)
		}
		if i > 0 && sorted[i-1].Path == f.Path {
			return nil, fmt.Errorf("bundle path %q appears twice", f.Path)
		}
		hdr := &tar.Header{Name: f.Path, Mode: 0o644, Size: int64(len(f.Data)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return nil, err
		}
		paths = append(paths, f.Path)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}

	data := buf.Bytes()
	sum := sha256.Sum256(data)
	b := &Bundle{Manifest: Manifest{
		Schema:  Schema,
		Release: rel,
		Digest:  "sha256:" + hex.EncodeToString(sum[:]),
		Size:    len(data),
		Files:   paths,
	}}
	for off, i := 0, 0; off < len(data); off, i = off+partBytes, i+1 {
		end := min(off+partBytes, len(data))
		chunk := data[off:end]
		ps := sha256.Sum256(chunk)
		b.Parts = append(b.Parts, chunk)
		b.Manifest.Parts = append(b.Manifest.Parts, Part{
			Key:       PartKey(i),
			ConfigMap: ConfigMapName(i),
			SHA256:    hex.EncodeToString(ps[:]),
			Size:      len(chunk),
		})
	}
	if len(b.Parts) > maxParts {
		return nil, fmt.Errorf("knowledge bundle is %d bytes compressed, more than the %d parts of %d bytes the AI runtime mounts; raise MaxParts here AND the projected volume in ai/adhar-ai", len(data), maxParts, partBytes)
	}
	return b, nil
}

// FromFS collects every regular file under fsys, keeping its path.
func FromFS(fsys fs.FS) ([]File, error) {
	var out []File
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		out = append(out, File{Path: path, Data: data})
		return nil
	})
	return out, err
}
