//go:build e2e

package docker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/tests/e2e"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	ctrl "sigs.k8s.io/controller-runtime"
)

func CleanUpDocker(t *testing.T) {
	t.Log("cleaning up docker env")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, err := e2e.RunCommand(ctx, `docker ps -aqf name=adhar-control-plane`, 10*time.Second)
	assert.NoError(t, err, fmt.Sprintf("error while listing docker containers: %s, %s", err, b))

	conts := strings.TrimSpace(string(b))
	if len(conts) == 0 {
		return
	}
	b, err = e2e.RunCommand(ctx, fmt.Sprintf("docker container rm -f %s", conts), 60*time.Second)
	assert.NoError(t, err, fmt.Sprintf("error while removing docker containers: %s, %s", err, b))

	b, err = e2e.RunCommand(ctx, "docker system prune -f", 60*time.Second)
	assert.NoError(t, err, fmt.Sprintf("error while pruning system: %s, %s", err, b))

	b, err = e2e.RunCommand(ctx, "docker volume prune -f", 60*time.Second)
	assert.NoError(t, err, fmt.Sprintf("error while pruning volumes: %s, %s", err, b))
	t.Log("finished cleaning up docker env")
}

func Test_CreateDocker(t *testing.T) {
	slogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctrl.SetLogger(logr.FromSlogHandler(slogger.Handler()))

	testCreate(t)
	testCreatePath(t)
	testCreatePort(t)
	testCustomPkg(t)
}

// test adhar create
func testCreate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	defer CleanUpDocker(t)

	t.Log("running adhar up")
	cmd := exec.CommandContext(ctx, e2e.AdharBinaryLocation, "up")
	b, err := cmd.CombinedOutput()
	assert.NoError(t, err, b)

	kubeClient, err := e2e.GetKubeClient()
	assert.NoError(t, err, fmt.Sprintf("error while getting client: %s", err))

	e2e.TestArgoCDApps(ctx, t, kubeClient, e2e.CorePackages)

	argoBaseUrl := fmt.Sprintf("https://%s:%s/argocd", e2e.DefaultBaseDomain, e2e.DefaultPort)
	giteaBaseUrl := fmt.Sprintf("https://%s:%s/gitea", e2e.DefaultBaseDomain, e2e.DefaultPort)
	e2e.TestCoreEndpoints(ctx, t, argoBaseUrl, giteaBaseUrl)

	e2e.TestGiteaRegistry(ctx, t, "docker", fmt.Sprintf("gitea.%s", e2e.DefaultBaseDomain), e2e.DefaultPort)
}

// test adhar up --use-path-routing
func testCreatePath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	defer CleanUpDocker(t)

	t.Log("running adhar up --use-path-routing")
	cmd := exec.CommandContext(ctx, e2e.AdharBinaryLocation, "up", "--use-path-routing", "--package=../../../platform/stack")
	b, err := cmd.CombinedOutput()
	assert.NoError(t, err, fmt.Sprintf("error while running create: %s, %s", err, b))

	kubeClient, err := e2e.GetKubeClient()
	assert.NoError(t, err, fmt.Sprintf("error while getting client: %s", err))

	e2e.TestArgoCDApps(ctx, t, kubeClient, e2e.CorePackages)

	argoBaseUrl := fmt.Sprintf("https://%s:%s/argocd", e2e.DefaultBaseDomain, e2e.DefaultPort)
	giteaBaseUrl := fmt.Sprintf("https://%s:%s/gitea", e2e.DefaultBaseDomain, e2e.DefaultPort)
	e2e.TestCoreEndpoints(ctx, t, argoBaseUrl, giteaBaseUrl)

	e2e.TestGiteaRegistry(ctx, t, "docker", e2e.DefaultBaseDomain, e2e.DefaultPort)
}

func testCreatePort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	defer CleanUpDocker(t)

	port := "2443"
	t.Logf("running adhar up --port %s", port)
	cmd := exec.CommandContext(ctx, e2e.AdharBinaryLocation, "up", "--use-path-routing", "--port", port, "--package=../../../platform/stack")
	b, err := cmd.CombinedOutput()
	assert.NoError(t, err, fmt.Sprintf("error while running up: %s, %s", err, b))

	kubeClient, err := e2e.GetKubeClient()
	assert.NoError(t, err, fmt.Sprintf("error while getting client: %s", err))

	e2e.TestArgoCDApps(ctx, t, kubeClient, e2e.CorePackages)

	argoBaseUrl := fmt.Sprintf("https://%s:%s/argocd", e2e.DefaultBaseDomain, port)
	giteaBaseUrl := fmt.Sprintf("https://%s:%s/gitea", e2e.DefaultBaseDomain, port)
	e2e.TestCoreEndpoints(ctx, t, argoBaseUrl, giteaBaseUrl)
}

func testCustomPkg(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	defer CleanUpDocker(t)

	cmdString := "up --use-path-routing --package ../../../pkg/controllers/custompackage/test/resources/customPackages/testDir"

	t.Log(fmt.Sprintf("running %s", cmdString))
	cmd := exec.CommandContext(ctx, e2e.AdharBinaryLocation, strings.Split(cmdString, " ")...)
	b, err := cmd.CombinedOutput()
	assert.NoError(t, err, fmt.Sprintf("error while running up: %s, %s", err, b))

	kubeClient, err := e2e.GetKubeClient()

	assert.NoError(t, err, fmt.Sprintf("error while getting client: %s", err))
	if err != nil {
		assert.FailNow(t, "failed creating cluster")
	}

	e2e.TestArgoCDApps(ctx, t, kubeClient, e2e.CorePackages)

	giteaBaseUrl := fmt.Sprintf("https://%s:%s/gitea", e2e.DefaultBaseDomain, e2e.DefaultPort)

	expectedApps := map[string]string{
		"my-app":  "argocd",
		"my-app2": "argocd",
	}
	e2e.TestArgoCDApps(ctx, t, kubeClient, expectedApps)
	repos, err := e2e.GetGiteaRepos(ctx, giteaBaseUrl)
	assert.NoError(t, err)
	expectedRepoNames := map[string]struct{}{
		"adhar-adhar-my-app-app1":  {},
		"adhar-adhar-my-app2-app2": {},
	}

	for i := range repos {
		repo := repos[i]
		_, ok := expectedRepoNames[repo.Name]
		if ok {
			t.Logf("deleting repo %s", repo.Name)
			delete(expectedRepoNames, repo.Name)
		}
	}
	assert.Empty(t, expectedRepoNames)
}
