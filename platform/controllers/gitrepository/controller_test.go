package gitrepository

import (
	"adhar-io/adhar/platform/utils"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"adhar-io/adhar/api/v1alpha1"

	"code.gitea.io/sdk/gitea"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const addFileContent = "added\n"

type mockGitea struct {
	GiteaClient
	getRepo    func() (*gitea.Repository, *gitea.Response, error)
	createRepo func() (*gitea.Repository, *gitea.Response, error)
}

func (g mockGitea) SetBasicAuth(user, pass string) {}

func (g mockGitea) SetContext(ctx context.Context) {}

func (g mockGitea) CreateOrgRepo(org string, opt gitea.CreateRepoOption) (*gitea.Repository, *gitea.Response, error) {
	if g.createRepo != nil {
		return g.createRepo()
	}
	return &gitea.Repository{}, &gitea.Response{}, nil
}

func (g mockGitea) GetRepo(owner, reponame string) (*gitea.Repository, *gitea.Response, error) {
	if g.getRepo != nil {
		return g.getRepo()
	}
	return &gitea.Repository{}, &gitea.Response{}, nil
}

type expect struct {
	resource v1alpha1.GitRepositoryStatus
	err      error
}

type testCase struct {
	giteaClient GiteaClient
	input       v1alpha1.GitRepository
	expect      expect
}

func (t testCase) giteaProvider(ctx context.Context, repo *v1alpha1.GitRepository, kubeClient client.Client, scheme *runtime.Scheme, tmplConfig v1alpha1.BuildCustomizationSpec) (gitProvider, error) {
	return &giteaProvider{
		Client:      kubeClient,
		Scheme:      scheme,
		giteaClient: t.giteaClient,
		config:      tmplConfig,
	}, nil
}

type fakeClient struct {
	client.Client
	patchObj client.Object
}

func (f *fakeClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	s := obj.(*v1.Secret)
	s.Data = map[string][]byte{
		giteaAdminUsernameKey:   []byte("abc"),
		giteaAdminPasswordKey:   []byte("abc"),
		corev1.TLSCertKey:       []byte("abc"),
		corev1.TLSPrivateKeyKey: []byte("abc"),
	}
	return nil
}

func (f *fakeClient) Status() client.StatusWriter {
	return fakeStatusWriter{}
}

func (f *fakeClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	f.patchObj = obj
	return nil
}

type fakeStatusWriter struct {
	client.StatusWriter
}

func (f fakeStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return nil
}

func setUpLocalRepo() (string, string, error) {
	repoDir, err := os.MkdirTemp("", "test")
	if err != nil {
		return "", "", fmt.Errorf("creating temporary directory: %w", err)
	}
	// create a repo for pushing. MUST BE BARE
	repo, err := git.PlainInit(repoDir, true)
	if err != nil {
		return "", "", fmt.Errorf("repo init: %w", err)
	}

	// init it with a static file (in-memory), set default branch name, then get the hash
	defaultBranchName := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", DefaultBranchName))

	repoConfig, err := repo.Config()
	if err != nil {
		return "", "", fmt.Errorf("get repo config: %w", err)
	}
	repoConfig.Init.DefaultBranch = DefaultBranchName
	if err := repo.SetConfig(repoConfig); err != nil {
		return "", "", fmt.Errorf("set repo config: %w", err)
	}

	h := plumbing.NewSymbolicReference(plumbing.HEAD, defaultBranchName)
	if err := repo.Storer.SetReference(h); err != nil {
		return "", "", fmt.Errorf("set symbolic ref: %w", err)
	}

	fileObject := plumbing.MemoryObject{}
	fileObject.SetType(plumbing.BlobObject)
	w, err := fileObject.Writer()
	if err != nil {
		return "", "", fmt.Errorf("create file writer: %w", err)
	}

	file, err := os.ReadFile("test/resources/file1")
	if err != nil {
		return "", "", fmt.Errorf("reading file from resources dir: %w", err)
	}
	if _, err := w.Write(file); err != nil {
		return "", "", fmt.Errorf("write file contents: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", "", fmt.Errorf("close writer: %w", err)
	}

	fileHash, err := repo.Storer.SetEncodedObject(&fileObject)
	if err != nil {
		return "", "", fmt.Errorf("store blob: %w", err)
	}

	treeEntry := object.TreeEntry{
		Name: "file1",
		Mode: filemode.Regular,
		Hash: fileHash,
	}

	tree := object.Tree{
		Entries: []object.TreeEntry{treeEntry},
	}

	treeObject := plumbing.MemoryObject{}
	if err := tree.Encode(&treeObject); err != nil {
		return "", "", fmt.Errorf("encode tree: %w", err)
	}

	initHash, err := repo.Storer.SetEncodedObject(&treeObject)
	if err != nil {
		return "", "", fmt.Errorf("store tree: %w", err)
	}

	commit := object.Commit{
		Author: object.Signature{
			Name:  gitCommitAuthorName,
			Email: gitCommitAuthorEmail,
			When:  time.Now(),
		},
		Message:  "init",
		TreeHash: initHash,
	}

	commitObject := plumbing.MemoryObject{}
	if err := commit.Encode(&commitObject); err != nil {
		return "", "", fmt.Errorf("encode commit: %w", err)
	}

	commitHash, err := repo.Storer.SetEncodedObject(&commitObject)
	if err != nil {
		return "", "", fmt.Errorf("store commit: %w", err)
	}

	if err := repo.Storer.SetReference(plumbing.NewHashReference(defaultBranchName, commitHash)); err != nil {
		return "", "", fmt.Errorf("set hash ref: %w", err)
	}

	return repoDir, commitHash.String(), nil
}

func setupDir() (string, error) {
	tempDir, err := os.MkdirTemp("", "test")
	if err != nil {
		return "", fmt.Errorf("creating temporary directory: %w", err)
	}

	file, err := os.ReadFile("test/resources/file1")
	if err != nil {
		return "", fmt.Errorf("reading file from resources dir: %w", err)
	}
	err = os.WriteFile(filepath.Join(tempDir, "file1"), file, 0644)
	if err != nil {
		return "", fmt.Errorf("writing file to temp dir: %w", err)
	}

	err = os.WriteFile(filepath.Join(tempDir, "add"), []byte(addFileContent), 0644)
	if err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	return tempDir, nil
}

func TestGitRepositoryContentReconcile(t *testing.T) {
	ctx := context.Background()
	localRepoDir, _, err := setUpLocalRepo()
	defer func() {
		_ = os.RemoveAll(localRepoDir)
	}()
	if err != nil {
		t.Fatalf("failed setting up local git repo: %v", err)
	}

	srcDir, err := setupDir()
	defer func() {
		_ = os.RemoveAll(srcDir)
	}()
	if err != nil {
		t.Fatalf("failed to set up dirs: %v", err)
	}

	testCloneDir, err := os.MkdirTemp("", "gitrepo-test")
	if err != nil {
		t.Fatalf("failed to create clone dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(testCloneDir)
	}()

	m := metav1.ObjectMeta{
		Name:      "test",
		Namespace: "test",
	}
	resource := v1alpha1.GitRepository{
		ObjectMeta: m,
		Spec: v1alpha1.GitRepositorySpec{
			Source: v1alpha1.GitRepositorySource{
				Path: srcDir,
				Type: "local",
			},
		},
	}

	t.Run("files modified", func(t *testing.T) {
		p := giteaProvider{
			Client:      &fakeClient{},
			giteaClient: mockGitea{},
		}
		creds := gitProviderCredentials{username: "tester", password: "secret"}
		// add file to source directory, reconcile, clone the repo and check if the added file exists
		err = p.updateRepoContent(ctx, &resource, repoInfo{cloneUrl: localRepoDir}, creds, testCloneDir, utils.NewRepoLock())
		if err != nil {
			t.Fatalf("failed adding %v", err)
		}

		repo, _ := git.PlainClone(testCloneDir, false, &git.CloneOptions{
			URL: localRepoDir,
		})
		c, err := os.ReadFile(filepath.Join(testCloneDir, "add"))
		if err != nil {
			t.Fatalf("failed to read file at %s. %v", filepath.Join(testCloneDir, "add"), err)
		}
		if string(c) != addFileContent {
			t.Fatalf("expected %s, got %s", addFileContent, c)
		}

		// remove added file, reconcile, pull, check if the file is removed
		err = os.Remove(filepath.Join(srcDir, "add"))
		if err != nil {
			t.Fatalf("failed to remove added file %v", err)
		}
		err = p.updateRepoContent(ctx, &resource, repoInfo{cloneUrl: localRepoDir}, creds, testCloneDir, utils.NewRepoLock())
		if err != nil {
			t.Fatalf("failed removing %v", err)
		}

		w, _ := repo.Worktree()
		err = w.Pull(&git.PullOptions{})
		if err != nil {
			t.Fatalf("failed pulling changes %v", err)
		}

		_, err = os.Stat(filepath.Join(testCloneDir, "add"))
		if err == nil {
			t.Fatalf("file should not exist")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("received unexpected error %v", err)
		}
	})
}

func TestGitRepositoryContentReconcileEmbedded(t *testing.T) {
	ctx := context.Background()
	localRepoDir, _, err := setUpLocalRepo()
	defer func() {
		_ = os.RemoveAll(localRepoDir)
	}()
	if err != nil {
		t.Fatalf("failed setting up local git repo: %v", err)
	}

	tmpDir, _ := os.MkdirTemp("", "add")
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	m := metav1.ObjectMeta{
		Name:      "test",
		Namespace: "test",
	}
	resource := v1alpha1.GitRepository{
		ObjectMeta: m,
		Spec: v1alpha1.GitRepositorySpec{
			Source: v1alpha1.GitRepositorySource{
				EmbeddedAppName: "nginx",
				Type:            "embedded",
			},
			Provider: v1alpha1.Provider{
				InternalGitURL: "http://adhar.io",
			},
		},
	}

	t.Run("should update content", func(t *testing.T) {
		p := giteaProvider{
			Client:      &fakeClient{},
			giteaClient: mockGitea{},
		}
		err = p.updateRepoContent(ctx, &resource, repoInfo{cloneUrl: localRepoDir}, gitProviderCredentials{}, tmpDir, utils.NewRepoLock())
		assert.Error(t, err)
	})
}

func TestGitRepositoryReconcile(t *testing.T) {
	localReoDir, hash, err := setUpLocalRepo()
	defer func() {
		_ = os.RemoveAll(localReoDir)
	}()
	if err != nil {
		t.Fatalf("failed setting up local git repo: %v", err)
	}
	resourcePath, err := filepath.Abs("./test/resources")
	if err != nil {
		t.Fatalf("failed to get absolute path: %v", err)
	}
	updateDir, _, _ := setUpLocalRepo()
	defer func() {
		_ = os.RemoveAll(updateDir)
	}()

	addDir, err := setupDir()
	fmt.Println(addDir)
	defer func() {
		_ = os.RemoveAll(addDir)
	}()
	if err != nil {
		t.Fatalf("failed to set up dirs: %v", err)
	}

	tmpDir, _ := os.MkdirTemp("", "gitrepo-test")
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	m := metav1.ObjectMeta{
		Name:      "test",
		Namespace: "test",
	}

	cases := map[string]testCase{
		"no op": {
			giteaClient: mockGitea{
				getRepo: func() (*gitea.Repository, *gitea.Response, error) {
					return &gitea.Repository{CloneURL: localReoDir}, nil, nil
				},
			},
			input: v1alpha1.GitRepository{
				ObjectMeta: m,
				Spec: v1alpha1.GitRepositorySpec{
					Source: v1alpha1.GitRepositorySource{
						Path: resourcePath,
						Type: "local",
					},
					Provider: v1alpha1.Provider{
						Name:           v1alpha1.GitProviderGitea,
						InternalGitURL: "http://adhar.io",
					},
				},
			},
			expect: expect{
				resource: v1alpha1.GitRepositoryStatus{
					ExternalGitRepositoryUrl: localReoDir,
					LatestCommit:             v1alpha1.Commit{Hash: hash},
					Synced:                   true,
					InternalGitRepositoryUrl: "http://adhar.io/giteaAdmin/test-test.git",
				},
			},
		},
		"update": {
			giteaClient: mockGitea{
				getRepo: func() (*gitea.Repository, *gitea.Response, error) {
					return &gitea.Repository{CloneURL: updateDir}, nil, nil
				},
			},
			input: v1alpha1.GitRepository{
				ObjectMeta: m,
				Spec: v1alpha1.GitRepositorySpec{
					Source: v1alpha1.GitRepositorySource{
						Path: addDir,
						Type: "local",
					},
					Provider: v1alpha1.Provider{
						Name:           v1alpha1.GitProviderGitea,
						InternalGitURL: "http://adhar.io",
					},
				},
			},
			expect: expect{
				resource: v1alpha1.GitRepositoryStatus{
					ExternalGitRepositoryUrl: updateDir,
					Synced:                   true,
					InternalGitRepositoryUrl: "http://adhar.io/giteaAdmin/test-test.git",
				},
			},
		},
	}

	ctx := context.Background()

	t.Run("repo updates", func(t *testing.T) {
		for k := range cases {
			v := cases[k]
			r := GitRepositoryReconciler{
				Client:          &fakeClient{},
				GitProviderFunc: v.giteaProvider,
				TempDir:         tmpDir,
				RepoMap:         utils.NewRepoLock(),
			}
			_, err := r.reconcileGitRepo(ctx, &v.input)
			if v.expect.err == nil && err != nil {
				t.Fatalf("failed %s: %v", k, err)
			}

			if v.expect.resource.LatestCommit.Hash == "" {
				v.expect.resource.LatestCommit.Hash = v.input.Status.LatestCommit.Hash
			}
			assert.Equal(t, v.input.Status, v.expect.resource)
		}
	})
}

func TestGitRepositoryPostReconcile(t *testing.T) {
	c := fakeClient{}
	tmpDir, _ := os.MkdirTemp("", "repo-updates-test")
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()
	reconciler := GitRepositoryReconciler{
		Client:  &c,
		TempDir: tmpDir,
		RepoMap: utils.NewRepoLock(),
	}
	testTime := time.Now().Format(time.RFC3339Nano)
	repo := v1alpha1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test",
			Annotations: map[string]string{
				v1alpha1.CliStartTimeAnnotation: testTime,
			},
		},
	}

	reconciler.postProcessReconcile(context.Background(), ctrl.Request{}, &repo)
	annotations := c.patchObj.GetAnnotations()
	v, ok := annotations[v1alpha1.LastObservedCLIStartTimeAnnotation]
	if !ok {
		t.Fatalf("expected annotation not found: %s", v1alpha1.LastObservedCLIStartTimeAnnotation)
	}
	if v != testTime {
		t.Fatalf("annotation values does not match")
	}

	repo.Annotations[v1alpha1.LastObservedCLIStartTimeAnnotation] = "abc"
	reconciler.postProcessReconcile(context.Background(), ctrl.Request{}, &repo)
	v = annotations[v1alpha1.LastObservedCLIStartTimeAnnotation]
	if v != testTime {
		t.Fatalf("annotation values does not match")
	}
}
