package knowledge

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/spf13/cobra"

	adhar "adhar-io/adhar"
)

func fakeTree() *cobra.Command {
	root := &cobra.Command{Use: "adhar", Short: "Adhar CLI"}
	root.PersistentFlags().String("kubeconfig", "", "Path to the kubeconfig")
	get := &cobra.Command{Use: "get", Short: "Display resources"}
	get.PersistentFlags().StringP("output", "o", "table", "Output format")
	apps := &cobra.Command{
		Use: "apps [name]", Short: "List applications", Aliases: []string{"applications"},
		Long: "Lists every Argo CD application the platform manages.", Example: "adhar get apps -o json",
		Run: func(*cobra.Command, []string) {},
	}
	apps.Flags().Bool("all", false, "Include disabled packages")
	secret := &cobra.Command{Use: "internal", Short: "not for people", Hidden: true, Run: func(*cobra.Command, []string) {}}
	get.AddCommand(apps)
	root.AddCommand(get, secret)
	return root
}

// unpack verifies the parts against the manifest and returns the bundle's files.
func unpack(t *testing.T, b *Bundle) map[string]string {
	t.Helper()
	var all []byte
	for i, p := range b.Parts {
		sum := sha256.Sum256(p)
		if got := hex.EncodeToString(sum[:]); got != b.Manifest.Parts[i].SHA256 {
			t.Fatalf("part %d digest %s, manifest says %s", i, got, b.Manifest.Parts[i].SHA256)
		}
		all = append(all, p...)
	}
	sum := sha256.Sum256(all)
	if "sha256:"+hex.EncodeToString(sum[:]) != b.Manifest.Digest || len(all) != b.Manifest.Size {
		t.Fatalf("concatenated parts do not match the manifest digest/size")
	}
	gz, err := gzip.NewReader(bytes.NewReader(all))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		out[h.Name] = string(data)
	}
	return out
}

func TestTheBundleCarriesDocsExamplesChangelogAndTheCLIReference(t *testing.T) {
	rel := Release{Version: "v9.9.9", Commit: "abc1234", BuildDate: "2026-10-23"}
	b, err := Assemble(adhar.KnowledgeFS, fakeTree(), rel)
	if err != nil {
		t.Fatal(err)
	}
	files := unpack(t, b)
	for _, want := range []string{
		"README.md", "CHANGELOG.md", "docs/ARCHITECTURE.md", "docs/adr/0024-agentic-ai-platform.md",
		"examples/README.md", "examples/config.yaml", "cli/adhar.md", "cli/adhar-get.md", ReleaseFile,
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("bundle lacks %s", want)
		}
	}
	if b.Manifest.Version != "v9.9.9" || b.Manifest.Commit != "abc1234" || b.Manifest.Schema != Schema {
		t.Errorf("manifest does not identify the release: %+v", b.Manifest.Release)
	}
	if !strings.Contains(files[ReleaseFile], "v9.9.9") {
		t.Errorf("release document does not name the version:\n%s", files[ReleaseFile])
	}
	if len(b.Manifest.Files) != len(files) {
		t.Errorf("manifest lists %d files, bundle holds %d", len(b.Manifest.Files), len(files))
	}
	for path := range files {
		if strings.HasPrefix(path, "docs/images/") && !strings.HasSuffix(path, ".md") {
			t.Errorf("binary asset %s shipped in a text corpus", path)
		}
	}
}

func TestTheCLIReferenceComesFromTheCommandTree(t *testing.T) {
	ref := map[string]string{}
	for _, f := range CommandReference(fakeTree()) {
		ref[f.Path] = string(f.Data)
	}
	get := ref["cli/adhar-get.md"]
	for _, want := range []string{
		"## `adhar get apps`", "List applications", "Aliases: `applications`.",
		"Lists every Argo CD application", "--all", "Include disabled packages",
		"adhar get apps -o json", "Usage: `adhar get apps [name] [flags]`",
		"--output", // the group's own persistent flag, on the group's section
	} {
		if !strings.Contains(get, want) {
			t.Errorf("cli/adhar-get.md lacks %q:\n%s", want, get)
		}
	}
	if !strings.Contains(ref["cli/adhar.md"], "--kubeconfig") || !strings.Contains(ref["cli/adhar.md"], "`adhar get` — Display resources") {
		t.Errorf("root page lacks global flags or the command list:\n%s", ref["cli/adhar.md"])
	}
	for path, body := range ref {
		if strings.Contains(body, "internal") || strings.Contains(path, "internal") {
			t.Errorf("hidden command leaked into %s", path)
		}
	}
	if CommandReference(nil) != nil {
		t.Error("no command tree must mean no reference, not an empty page")
	}
}

func TestTheBundleIsDeterministicAndVersioned(t *testing.T) {
	rel := Release{Version: "v1.0.0", Commit: "a"}
	a, err := Assemble(adhar.KnowledgeFS, fakeTree(), rel)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Assemble(adhar.KnowledgeFS, fakeTree(), rel)
	if a.Manifest.Digest != b.Manifest.Digest {
		t.Fatal("the same release produced two digests; every reconcile would re-publish")
	}
	c, _ := Assemble(adhar.KnowledgeFS, fakeTree(), Release{Version: "v1.0.1", Commit: "b"})
	if c.Manifest.Digest == a.Manifest.Digest {
		t.Fatal("a new release with unchanged docs kept the digest; an upgraded controller would never re-publish")
	}
}

func TestTheBundleSplitsIntoPartsAndRefusesMoreThanTheRuntimeMounts(t *testing.T) {
	var big []byte // incompressible: a sha256 chain
	for seed := sha256.Sum256(nil); len(big) < 64*1024; seed = sha256.Sum256(seed[:]) {
		big = append(big, seed[:]...)
	}
	fsys := fstest.MapFS{"docs/a.md": {Data: big}, "docs/b.md": {Data: []byte("# b\n")}}
	files, _ := FromFS(fsys)
	b, err := Pack(files, Release{Version: "v1"}, 16*1024, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Parts) < 2 {
		t.Fatalf("expected the bundle to span parts, got %d", len(b.Parts))
	}
	if got := unpack(t, b); got["docs/b.md"] != "# b\n" || len(got["docs/a.md"]) != len(big) {
		t.Fatal("reassembled parts do not reproduce the files")
	}
	if _, err := Pack(files, Release{}, 16*1024, 1); err == nil {
		t.Fatal("a bundle needing more parts than the runtime mounts must be refused, not silently truncated")
	}
	if _, err := Pack([]File{{Path: "../etc/passwd"}}, Release{}, PartBytes, MaxParts); err == nil {
		t.Fatal("a path escaping the bundle root must be refused")
	}
}

// The real corpus must leave room to grow: if this fails, raise MaxParts here
// and the projected volume in ai/adhar-ai together.
func TestTheRealCorpusFitsWithHeadroom(t *testing.T) {
	b, err := Assemble(adhar.KnowledgeFS, fakeTree(), Release{Version: "v0"})
	if err != nil {
		t.Fatal(err)
	}
	limit := PartBytes * MaxParts
	t.Logf("knowledge bundle: %d files, %d bytes compressed, %d part(s), %.0f%% of capacity",
		len(b.Manifest.Files), b.Manifest.Size, len(b.Parts), 100*float64(b.Manifest.Size)/float64(limit))
	if b.Manifest.Size > limit*6/10 {
		t.Errorf("bundle is %d bytes, over 60%% of the %d bytes the runtime mounts", b.Manifest.Size, limit)
	}
}

func TestEveryConfigMapFitsTheAPIServerLimitAndPartZeroCarriesTheManifest(t *testing.T) {
	b, err := Assemble(adhar.KnowledgeFS, fakeTree(), Release{Version: "v0"})
	if err != nil {
		t.Fatal(err)
	}
	cms, err := ConfigMaps(b, "adhar-system")
	if err != nil {
		t.Fatal(err)
	}
	for i, cm := range cms {
		raw, _ := json.Marshal(cm)
		if len(raw) >= 1<<20 {
			t.Errorf("%s serialises to %d bytes, over the 1 MiB ConfigMap limit", cm.Name, len(raw))
		}
		if cm.Name != ConfigMapName(i) || cm.Labels[LabelBundle] != "true" || cm.Annotations[AnnotationDigest] != b.Manifest.Digest {
			t.Errorf("%s is not labelled/annotated as part %d of %s", cm.Name, i, b.Manifest.Digest)
		}
		if _, ok := cm.BinaryData[PartKey(i)]; !ok {
			t.Errorf("%s lacks %s", cm.Name, PartKey(i))
		}
	}
	var m Manifest
	if err := json.Unmarshal([]byte(cms[0].Data[ManifestKey]), &m); err != nil || m.Digest != b.Manifest.Digest {
		t.Fatalf("part 0 does not carry the manifest: %v", err)
	}
}
