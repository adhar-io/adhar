package kind

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestEveryNodeOfThisClusterHasItsProcessLimitLifted(t *testing.T) {
	var calls []string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "ps":
			return "adhar-control-plane\nadhar-worker\n", nil
		case "inspect":
			return "-1\n", nil
		}
		return "", nil
	}
	lifted, err := liftPidsLimit(context.Background(), run, "adhar")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lifted, ",") != "adhar-control-plane,adhar-worker" {
		t.Fatalf("lifted %v", lifted)
	}
	want := []string{
		"ps --filter label=io.x-k8s.kind.cluster=adhar --format {{.Names}}",
		"update --pids-limit=-1 adhar-control-plane",
		"inspect --format {{.HostConfig.PidsLimit}} adhar-control-plane",
		"update --pids-limit=-1 adhar-worker",
		"inspect --format {{.HostConfig.PidsLimit}} adhar-worker",
	}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls\n got %q\nwant %q", calls, want)
	}
}

func TestOnlyPodmanIsTouched(t *testing.T) {
	for bin, want := range map[string]bool{"podman": true, "/usr/bin/podman": true, "docker": false, "nerdctl": false} {
		if isPodman(bin) != want {
			t.Errorf("isPodman(%q) = %t", bin, !want)
		}
	}
}

// The lift must run on both paths that hand a cluster back: a new one, before
// anything is scheduled, and an existing one made before this fix.
func TestReconcileLiftsTheProcessLimitOnNewAndExistingClusters(t *testing.T) {
	b, err := os.ReadFile("cluster.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if n := strings.Count(src, "c.liftNodePidsLimit(ctx)"); n < 2 {
		t.Fatalf("Reconcile lifts the Podman process limit on %d path(s); want the new AND the existing cluster", n)
	}
	created := strings.Index(src, `setupLog.Info("Done creating cluster"`)
	lift := strings.LastIndex(src, "c.liftNodePidsLimit(ctx)")
	cache := strings.LastIndex(src, "c.ensureRegistryCache(ctx)")
	if created < 0 || lift < created || lift > cache {
		t.Error("the process limit is not lifted right after the cluster is created, before the image cache and the platform start")
	}
}

// Podman 4.9.3 on the E2E runner reported success from `podman update` and the
// node still failed with errno=11: a lift is only a lift once it reads back.
func TestALiftThatDidNotTakeIsAnError(t *testing.T) {
	run := func(_ context.Context, args ...string) (string, error) {
		switch args[0] {
		case "ps":
			return "adhar-control-plane\n", nil
		case "inspect":
			return "2048\n", nil
		}
		return "", nil
	}
	if _, err := liftPidsLimit(context.Background(), run, "adhar"); err == nil {
		t.Fatal("a node still limited to 2048 processes was reported as lifted")
	}
}

func TestPodmanCreatesTheNodeWithoutAProcessLimit(t *testing.T) {
	t.Setenv("CONTAINERS_CONF_OVERRIDE", "")
	_ = os.Unsetenv("CONTAINERS_CONF_OVERRIDE")

	restore := createWithoutPidsLimit("/usr/bin/podman")
	path := os.Getenv("CONTAINERS_CONF_OVERRIDE")
	if path == "" {
		t.Fatal("kind create would run Podman without the containers.conf override")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "pids_limit = 0") {
		t.Fatalf("override does not remove the process limit:\n%s", b)
	}
	restore()
	if os.Getenv("CONTAINERS_CONF_OVERRIDE") != "" {
		t.Error("the override outlives kind create")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the override file is left behind")
	}
}

func TestOnlyPodmanGetsTheOverrideAndAUsersOwnIsKept(t *testing.T) {
	t.Setenv("CONTAINERS_CONF_OVERRIDE", "")
	_ = os.Unsetenv("CONTAINERS_CONF_OVERRIDE")
	createWithoutPidsLimit("docker")()
	if os.Getenv("CONTAINERS_CONF_OVERRIDE") != "" {
		t.Error("Docker was given a Podman override")
	}

	t.Setenv("CONTAINERS_CONF_OVERRIDE", "/home/me/mine.conf")
	restore := createWithoutPidsLimit("podman")
	if got := os.Getenv("CONTAINERS_CONF_OVERRIDE"); got != "/home/me/mine.conf" {
		t.Errorf("the user's own override was replaced with %q", got)
	}
	restore()
	if got := os.Getenv("CONTAINERS_CONF_OVERRIDE"); got != "/home/me/mine.conf" {
		t.Errorf("restoring cleared the user's own override (%q)", got)
	}
}

func TestKindCreateRunsUnderTheOverride(t *testing.T) {
	b, err := os.ReadFile("cluster.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	set := strings.Index(src, "createWithoutPidsLimit(")
	create := strings.Index(src, "c.provider.Create(")
	restore := strings.Index(src, "restoreEngineEnv()")
	if set < 0 || create < set || restore < create {
		t.Error("kind create does not run between setting and restoring the Podman override")
	}
}
