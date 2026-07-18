package utils

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/api/v1alpha1"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
)

// setUpFixtureRepo creates a local git repository with two tagged commits and a
// nested directory tree, so clone/copy behavior can be tested without any
// network or external-repo dependency. It returns the repo dir and the two
// tagged commit hashes.
func setUpFixtureRepo(t *testing.T) (repoDir string, v1Hash, v2Hash plumbing.Hash) {
	t.Helper()

	repoDir, err := os.MkdirTemp("", "fixture-repo")
	assert.Nil(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(repoDir) })

	repo, err := git.PlainInit(repoDir, false)
	assert.Nil(t, err)
	wt, err := repo.Worktree()
	assert.Nil(t, err)

	writeFile := func(rel, content string) {
		p := filepath.Join(repoDir, rel)
		assert.Nil(t, os.MkdirAll(filepath.Dir(p), 0o755))
		assert.Nil(t, os.WriteFile(p, []byte(content), 0o644))
	}
	commit := func(msg string) plumbing.Hash {
		_, aErr := wt.Add(".")
		assert.Nil(t, aErr)
		h, cErr := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "test", Email: "test@adhar.io", When: time.Now()},
		})
		assert.Nil(t, cErr)
		return h
	}

	// Commit 1 (tag v1): nested tree with yaml and non-yaml files.
	writeFile("examples/basic/app.yaml", "kind: Application\n")
	writeFile("examples/basic/nested/deploy.yml", "kind: Deployment\n")
	writeFile("examples/basic/README.md", "readme\n")
	writeFile("top.txt", "top\n")
	v1Hash = commit("v1 content")
	_, err = repo.CreateTag("fixture-v1", v1Hash, nil)
	assert.Nil(t, err)

	// Commit 2 (tag v2): additional file.
	writeFile("examples/basic/extra.yaml", "kind: ConfigMap\n")
	v2Hash = commit("v2 content")
	_, err = repo.CreateTag("fixture-v2", v2Hash, nil)
	assert.Nil(t, err)

	return repoDir, v1Hash, v2Hash
}

func TestCloneRemoteRepoToDir(t *testing.T) {
	repoDir, v1Hash, v2Hash := setUpFixtureRepo(t)

	spec := v1alpha1.RemoteRepositorySpec{
		CloneSubmodules: false,
		Path:            "examples/basic",
		Url:             repoDir,
		Ref:             "fixture-v1",
	}
	dir, _ := os.MkdirTemp("", "TestCloneRemoteRepoToDir")
	defer os.RemoveAll(dir)

	// new clone at a tag
	_, repo, err := CloneRemoteRepoToDir(context.Background(), spec, 0, false, dir, "")
	assert.Nil(t, err)
	ref, err := repo.Head()
	assert.Nil(t, err)
	assert.Equal(t, v1Hash.String(), ref.Hash().String())

	// existing clone dir: switch to another ref
	spec.Ref = "fixture-v2"
	_, repo, err = CloneRemoteRepoToDir(context.Background(), spec, 0, false, dir, "")
	assert.Nil(t, err)
	ref, err = repo.Head()
	assert.Nil(t, err)
	assert.Equal(t, v2Hash.String(), ref.Hash().String())
}

func TestCopyTreeToTree(t *testing.T) {
	repoDir, _, _ := setUpFixtureRepo(t)

	spec := v1alpha1.RemoteRepositorySpec{
		CloneSubmodules: false,
		Path:            "examples/basic",
		Url:             repoDir,
		Ref:             "",
	}

	dst := memfs.New()
	src, _, err := CloneRemoteRepoToMemory(context.Background(), spec, 1, false)
	assert.Nil(t, err)

	err = CopyTreeToTree(src, dst, spec.Path, ".")
	assert.Nil(t, err)
	testCopiedFiles(t, src, dst, spec.Path, ".")
}

func testCopiedFiles(t *testing.T, src, dst billy.Filesystem, srcStartPath, dstStartPath string) {
	files, err := src.ReadDir(srcStartPath)
	assert.Nil(t, err)
	assert.NotEqual(t, 0, len(files))

	for i := range files {
		file := files[i]
		if file.Mode().IsRegular() {
			srcB, err := ReadWorktreeFile(src, filepath.Join(srcStartPath, file.Name()))
			assert.Nil(t, err)

			dstB, err := ReadWorktreeFile(dst, filepath.Join(dstStartPath, file.Name()))
			assert.Nil(t, err)
			assert.Equal(t, srcB, dstB)
		}
		if file.IsDir() {
			testCopiedFiles(t, src, dst, filepath.Join(srcStartPath, file.Name()), filepath.Join(dstStartPath, file.Name()))
		}
	}
}

func TestGetWorktreeYamlFiles(t *testing.T) {
	repoDir, _, _ := setUpFixtureRepo(t)

	cloneOptions := &git.CloneOptions{
		URL:               repoDir,
		Depth:             1,
		ShallowSubmodules: true,
	}

	wt := memfs.New()
	_, err := git.CloneContext(context.Background(), memory.NewStorage(), wt, cloneOptions)
	if err != nil {
		t.Fatalf("%s", err.Error())
	}

	// recursive: finds .yaml and .yml files in nested directories
	paths, err := GetWorktreeYamlFiles("./examples", wt, true)
	assert.Equal(t, nil, err)
	assert.NotEqual(t, 0, len(paths))
	for _, s := range paths {
		assert.Equal(t, true, strings.HasSuffix(s, "yaml") || strings.HasSuffix(s, "yml"))
	}

	// non-recursive: ./examples itself contains no yaml files directly
	paths, err = GetWorktreeYamlFiles("./examples", wt, false)
	assert.Equal(t, nil, err)
	assert.Equal(t, 0, len(paths))
}
