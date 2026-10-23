package adhar

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every Markdown file under docs/ and every example must be in the binary.
// The embed list names directories explicitly (embedding docs/ whole would
// carry its images), so a NEW docs subdirectory is silently left out of the
// AI's knowledge until the pattern grows — this is what notices.
func TestEveryDocAndExampleIsEmbedded(t *testing.T) {
	for _, root := range []string{"docs", "examples"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !strings.HasSuffix(path, ".md") && !strings.HasSuffix(path, ".yaml") {
				return nil
			}
			if _, err := fs.Stat(KnowledgeFS, filepath.ToSlash(path)); err != nil {
				t.Errorf("%s is not embedded in KnowledgeFS; add its directory to the //go:embed list in knowledge.go", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"README.md", "CHANGELOG.md"} {
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := fs.ReadFile(KnowledgeFS, f)
		if err != nil || string(got) != string(want) {
			t.Errorf("%s is not embedded verbatim", f)
		}
	}
}
