package utils

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"adhar-io/adhar/api/v1alpha1"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
)

func createLocalRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	assert.NoError(t, err)

	worktree, err := repo.Worktree()
	assert.NoError(t, err)

	for name, data := range files {
		full := filepath.Join(dir, name)
		assert.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		assert.NoError(t, os.WriteFile(full, []byte(data), 0o644))
		_, err = worktree.Add(name)
		assert.NoError(t, err)
	}

	_, err = worktree.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "tester@example.com"},
	})
	assert.NoError(t, err)
	return dir
}

func TestCloneRemoteRepoToDir_LocalPath(t *testing.T) {
	repoDir := createLocalRepo(t, map[string]string{"file.txt": "hello"})
	dest := filepath.Join(t.TempDir(), "clone")

	spec := v1alpha1.RemoteRepositorySpec{Url: repoDir}
	fs, repo, err := CloneRemoteRepoToDir(context.Background(), spec, 1, false, dest, "")
	assert.NoError(t, err)
	assert.NotNil(t, fs)
	assert.NotNil(t, repo)

	head, err := repo.Head()
	assert.NoError(t, err)
	assert.Equal(t, plumbing.ReferenceName("refs/heads/master"), head.Name())
}

func TestCopyTreeToTree(t *testing.T) {
	src := memfs.New()
	assert.NoError(t, src.MkdirAll("manifests/app", 0o755))
	assert.NoError(t, src.MkdirAll("manifests/config", 0o755))
	writeFile := func(path, data string) {
		f, err := src.Create(path)
		assert.NoError(t, err)
		_, err = f.Write([]byte(data))
		assert.NoError(t, err)
		assert.NoError(t, f.Close())
	}
	writeFile("manifests/app/deploy.yaml", "kind: Deployment\n")
	writeFile("manifests/config/values.yml", "foo: bar\n")
	writeFile("manifests/README.md", "ignore\n")

	dst := memfs.New()
	err := CopyTreeToTree(src, dst, "manifests", ".")
	assert.NoError(t, err)

	content, err := ReadWorktreeFile(dst, "app/deploy.yaml")
	assert.NoError(t, err)
	assert.Contains(t, string(content), "Deployment")

	content, err = ReadWorktreeFile(dst, "config/values.yml")
	assert.NoError(t, err)
	assert.Contains(t, string(content), "foo")
}

func TestGetWorktreeYamlFiles(t *testing.T) {
	fs := memfs.New()
	assert.NoError(t, fs.MkdirAll("pkg/nested", 0o755))
	for path, data := range map[string]string{
		"pkg/app.yaml":         "kind: ConfigMap\n",
		"pkg/notes.txt":        "ignore",
		"pkg/nested/app.yml":   "kind: Service\n",
		"pkg/nested/readme.md": "nope",
	} {
		f, err := fs.Create(path)
		assert.NoError(t, err)
		_, err = f.Write([]byte(data))
		assert.NoError(t, err)
		assert.NoError(t, f.Close())
	}

	paths, err := GetWorktreeYamlFiles("pkg", fs, true)
	assert.NoError(t, err)
	assert.ElementsMatch(t, []string{"pkg/app.yaml", "pkg/nested/app.yml"}, paths)

	paths, err = GetWorktreeYamlFiles("pkg", fs, false)
	assert.NoError(t, err)
	assert.ElementsMatch(t, []string{"pkg/app.yaml"}, paths)
}
