package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// minio/mc stopped being pullable anonymously from Docker Hub and Quay, and a
// composition naming it fails only on a node without it cached: the XR is accepted,
// its Job sits in ImagePullBackOff, and nothing says why the bucket never appears.
var minioMCImage = regexp.MustCompile(`image:\s*\S*minio/mc`)

func TestNoCompositionDependsOnMinioMC(t *testing.T) {
	err := filepath.WalkDir(filepath.Join(controlPlaneDir(t), "compositions"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if minioMCImage.Match(raw) {
			t.Errorf("%s runs minio/mc; use the pinned rustfs/rc", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var pinnedRCImage = regexp.MustCompile(`image: docker\.io/rustfs/rc:v\d+\.\d+\.\d+\s`)

func TestBucketJobsUseAPinnedRustFSClient(t *testing.T) {
	for _, rel := range []string{"compositions/bucket/local-rustfs.yaml", "compositions/storage/local-rustfs.yaml"} {
		raw, err := os.ReadFile(filepath.Join(controlPlaneDir(t), rel))
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		if !pinnedRCImage.MatchString(src) {
			t.Errorf("%s does not pin docker.io/rustfs/rc to a release", rel)
		}
		// An in-cluster Service name must never become <bucket>.rustfs...: nothing resolves that.
		if !strings.Contains(src, "--bucket-lookup path") {
			t.Errorf("%s does not force path-style bucket lookup", rel)
		}
		if !strings.Contains(src, "rc bucket create --ignore-existing") {
			t.Errorf("%s does not create its bucket idempotently", rel)
		}
	}
}

// The Job is recreated after its TTL, and every `rc ilm rule add` appends another
// rule (verified against RustFS 1.0.1), so an unguarded add grows without bound.
func TestLifecycleRuleIsOnlyAddedWhenMissing(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/bucket/local-rustfs.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The template, not the header comment explaining it.
	src := string(raw)
	src = src[strings.Index(src, "template: |"):]
	list, add := strings.Index(src, "if ! rc ilm rule list"), strings.Index(src, "rc ilm rule add")
	if list < 0 || add < 0 || add < list {
		t.Fatalf("rc ilm rule add must sit inside the check that the declared rule is missing (list at %d, add at %d)", list, add)
	}
	if fi := strings.Index(src[add:], "fi\n"); fi < 0 {
		t.Fatal("the guard around rc ilm rule add is not closed")
	}
}
