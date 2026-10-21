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
		if args[0] == "ps" {
			return "adhar-control-plane\nadhar-worker\n", nil
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
		"update --pids-limit=-1 adhar-worker",
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
