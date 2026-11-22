/*
Copyright 2025.

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

package stack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FS is the platform stack as it was when this binary was built: every package,
// every environment, and the ApplicationSets that wire them. It is what makes
// `adhar up` work from an installed binary alone — the stack used to be read
// from a clone of the repository, so a `brew install adhar` could not bring a
// platform up at all.
//
// `all:` matters: packages carry `.gitkeep` and dot-prefixed files that a plain
// directory pattern silently leaves out, and the seeded GitOps repositories
// would then differ from the ones a source checkout seeds.
//
//go:embed all:packages all:environments *.yaml README.md
var FS embed.FS

// generated holds what the build produces rather than what Git tracks. Today
// that is one archive: the stack directories go:embed cannot carry. A directory
// with its own go.mod is a separate module to the toolchain and is left out of
// FS without a word, and two template skeletons are Go projects.
//
// The archive is written by `go generate` (hack/stack-bundle) on every
// `make build`, `make test` and release, and is gitignored. The directory is
// embedded as a whole — only its .gitkeep is tracked — so a bare `go build` on a
// fresh clone still compiles; that binary simply lacks the two skeletons, and
// inside a checkout the CLI reads the working tree anyway.
//
//go:generate go run ../../hack/stack-bundle .
//go:embed all:generated
var generated embed.FS

// NestedModuleBundle is the archive's path inside platform/stack.
const NestedModuleBundle = "generated/nested-modules.tar.gz"

// walk visits every file of the embedded stack — the embedded tree first, then
// the nested-module archive — with slash-separated paths relative to the stack.
func walk(fn func(path string, content []byte) error) error {
	err := fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return err
		}
		return fn(p, b)
	})
	if err != nil {
		return err
	}
	nestedModules, err := generated.ReadFile(NestedModuleBundle)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // built without `go generate`: see the note on generated
	}
	if err != nil {
		return err
	}
	zr, err := gzip.NewReader(bytes.NewReader(nestedModules))
	if err != nil {
		return fmt.Errorf("reading %s: %w", NestedModuleBundle, err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", NestedModuleBundle, err)
		}
		// Names were written by BuildNestedModuleBundle; refuse anything that
		// could land outside the stack all the same.
		if h.Typeflag != tar.TypeReg || !fs.ValidPath(h.Name) {
			return fmt.Errorf("%s: unexpected entry %q", NestedModuleBundle, h.Name)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		if err := fn(h.Name, b); err != nil {
			return err
		}
	}
}

// BuildNestedModuleBundle archives every directory under stackDir's packages/
// and environments/ that contains a go.mod. The output is deterministic (sorted
// names, no timestamps), so rebuilding an unchanged tree changes nothing in Git.
func BuildNestedModuleBundle(stackDir string) ([]byte, error) {
	var roots []string
	for _, top := range []string{"packages", "environments"} {
		err := filepath.WalkDir(filepath.Join(stackDir, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && d.Name() == "go.mod" {
				roots = append(roots, filepath.Dir(p))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	files := map[string][]byte{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == ".DS_Store" || !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(stackDir, p)
			if err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = b
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	tw := tar.NewWriter(zw)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EnvStackDir overrides where the stack is read from, for every command.
const EnvStackDir = "ADHAR_STACK_DIR"

// Origin says where a resolved stack came from.
type Origin string

const (
	// OriginEmbedded is the stack built into the binary.
	OriginEmbedded Origin = "embedded"
	// OriginExplicit is a directory named by a flag or by ADHAR_STACK_DIR.
	OriginExplicit Origin = "explicit"
)

// Source is a stack on disk, and where it came from.
type Source struct {
	// Dir is an absolute path to a directory laid out like platform/stack.
	Dir    string
	Origin Origin
}

// Embedded reports whether the stack is the read-only copy built into the binary.
func (s Source) Embedded() bool { return s.Origin == OriginEmbedded }

// Describe is a short phrase for a progress line.
func (s Source) Describe() string {
	switch s.Origin {
	case OriginEmbedded:
		return "built into this adhar binary"
	default:
		return s.Dir
	}
}

// Resolve finds the stack a command should use.
//
//  1. explicit — a path the operator named on the command line. It must exist:
//     falling back to the embedded stack when a named directory is missing
//     would push a different stack than the one they asked for.
//  2. $ADHAR_STACK_DIR — the same, for a whole shell session.
//  3. The stack embedded in the binary, extracted to the user cache.
//
// Nothing else, and in particular never ./platform/stack: the CLI behaves the
// same in every working directory, the repository root included, so the stack a
// command pushes is the one the binary was built with — `make build` embeds the
// working tree, so a rebuilt binary carries an edited package. (Until
// 2026-11-19 a source checkout's working tree was preferred, and an
// `adhar upgrade` run from the repository root pushed whatever the tree held
// rather than what the binary shipped.) To push a stack that is not the one
// built in, name it: --stack-dir platform/stack, or ADHAR_STACK_DIR.
func Resolve(explicit string) (Source, error) {
	if explicit == "" {
		explicit = os.Getenv(EnvStackDir)
	}
	if explicit != "" {
		dir, err := filepath.Abs(explicit)
		if err != nil {
			return Source{}, fmt.Errorf("resolving stack directory %q: %w", explicit, err)
		}
		if err := looksLikeStack(dir); err != nil {
			return Source{}, err
		}
		return Source{Dir: dir, Origin: OriginExplicit}, nil
	}
	dir, err := Materialize()
	if err != nil {
		return Source{}, err
	}
	return Source{Dir: dir, Origin: OriginEmbedded}, nil
}

func looksLikeStack(dir string) error {
	if fi, err := os.Stat(filepath.Join(dir, "packages")); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a platform stack directory (no packages/ inside); "+
			"omit the flag to use the stack built into adhar, or write an editable copy with `adhar stack export <dir>`", dir)
	}
	return nil
}

var (
	digestOnce sync.Once
	digestVal  string
	digestErr  error
)

// Digest identifies the embedded stack's content (paths and bytes).
func Digest() (string, error) {
	digestOnce.Do(func() {
		h := sha256.New()
		digestErr = walk(func(p string, b []byte) error {
			fmt.Fprintf(h, "%s\x00%d\x00", p, len(b))
			h.Write(b)
			return nil
		})
		digestVal = hex.EncodeToString(h.Sum(nil))[:16]
	})
	return digestVal, digestErr
}

// CacheEnv overrides where embedded stacks are extracted. Test packages that
// run a command resolving the embedded stack set it to a temporary directory,
// so a `go test` never extracts into — or prunes — the cache a running
// `adhar up` is seeding from.
const CacheEnv = "ADHAR_STACK_CACHE"

// cacheRoot is where embedded stacks are extracted. A variable so tests can
// point it somewhere disposable.
var cacheRoot = func() (string, error) {
	if dir := os.Getenv(CacheEnv); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "adhar", "stack"), nil
}

// pruneAfter is how long an extraction must go unused before another binary's
// Materialize may delete it. A running `adhar up` reads its extraction for the
// whole bring-up, so pruning on sight deleted it from under the run: a
// `go test ./cmd/stack/` (or any second adhar build) started mid-bring-up left
// the CLI retrying "lstat …/stack/<digest>/environments: no such file or
// directory" forever at the GitOps repos stage (2026-11-22).
const pruneAfter = 7 * 24 * time.Hour

// markUsed records that dir was just handed out, so pruneOtherExtractions
// keeps it.
func markUsed(dir string) {
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
}

const completeMarker = ".adhar-stack-complete"

// Materialize writes the embedded stack to the user cache and returns the
// directory. The directory is named by the content digest, so a binary reuses
// its own extraction and never reads another version's; extraction is staged
// beside the target and renamed into place, so an interrupted run cannot leave
// a half-written stack that a later run would seed into Git.
func Materialize() (string, error) {
	digest, err := Digest()
	if err != nil {
		return "", fmt.Errorf("reading the embedded platform stack: %w", err)
	}
	root, err := cacheRoot()
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, digest)
	if intact(target) {
		markUsed(target)
		return target, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("creating the stack cache %s: %w", root, err)
	}
	staging, err := os.MkdirTemp(root, ".extract-")
	if err != nil {
		return "", fmt.Errorf("creating the stack cache %s: %w", root, err)
	}
	defer os.RemoveAll(staging)
	if err := Export(staging); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(staging, completeMarker), []byte(digest+"\n"), 0o644); err != nil {
		return "", err
	}
	// A previous, damaged extraction of the same digest is replaced whole.
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	if err := os.Rename(staging, target); err != nil {
		// Another adhar process may have won the race with an identical tree.
		if intact(target) {
			markUsed(target)
			return target, nil
		}
		return "", fmt.Errorf("installing the extracted stack at %s: %w", target, err)
	}
	markUsed(target)
	pruneOtherExtractions(root, digest, time.Now())
	return target, nil
}

// intact reports whether dir holds a complete, unmodified extraction. Every file
// is compared with the embedded bytes: the cache is a plain directory the user
// can reach, and what is in it is about to be pushed to a cluster.
func intact(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, completeMarker)); err != nil {
		return false
	}
	ok := true
	_ = walk(func(p string, want []byte) error {
		got, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if rerr != nil || !bytes.Equal(want, got) {
			ok = false
			return fs.SkipAll
		}
		return nil
	})
	return ok
}

// pruneOtherExtractions removes the stacks of binaries that were replaced, so
// upgrading adhar does not leave one 45 MB tree behind per release. Only an
// extraction unused for pruneAfter goes: one another adhar process handed out
// recently may still be what that process is reading.
func pruneOtherExtractions(root, keep string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep || strings.HasPrefix(e.Name(), ".extract-") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < pruneAfter {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// Export writes the embedded stack into dir, which must be empty or absent.
// It is what `adhar stack export` runs to hand the operator an editable copy.
func Export(dir string) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty; export the stack into a new directory", dir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return walk(func(p string, b []byte) error {
		out := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, b, modeFor(b))
	})
}

// modeFor restores the one permission bit that matters. An embedded file has no
// mode, and the stack's generator scripts are tracked as executable: seeding
// them as 0644 would show every one as a mode change in the GitOps repositories
// of a cluster that was first seeded from a checkout.
func modeFor(content []byte) os.FileMode {
	if bytes.HasPrefix(content, []byte("#!")) {
		return 0o755
	}
	return 0o644
}

// Files lists the embedded stack's file paths, slash-separated and sorted.
func Files() ([]string, error) {
	var out []string
	err := walk(func(p string, _ []byte) error {
		out = append(out, p)
		return nil
	})
	sort.Strings(out)
	return out, err
}
