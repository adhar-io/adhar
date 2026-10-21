package kind

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"adhar-io/adhar/platform/utils"
)

// Podman caps every container at 2048 processes (containers.conf
// `pids_limit`); Docker sets no limit. A Kind node is ONE container holding
// every pod's processes and threads, so once the platform grew past that the
// node could no longer start anything: pods sat in ContainerCreating with
// "runc create failed … fork/exec … resource temporarily unavailable" and
// "failed to create new OS thread (errno=11)", and `adhar up` on Podman timed
// out on every run from 2026-10-17 while Docker passed (E2E state dump,
// 2026-10-21).
//
// A new node is CREATED without the limit (createWithoutPidsLimit); this
// lift is the second line, for a cluster made before that and for a run
// where the override could not be set. Lifting alone was not enough: on the
// runner's Podman 4.9.3 the E2E still failed with errno=11 after `podman
// update` reported success, while Podman 5.x applies it to the live cgroup.
func (c *Cluster) liftNodePidsLimit(ctx context.Context) {
	eng := utils.DetectContainerEngine()
	if !eng.Available || !isPodman(eng.Binary) {
		return
	}
	lifted, err := liftPidsLimit(ctx, realEngineRunner(eng.Binary), c.name)
	if err != nil {
		setupLog.Info("could not lift Podman's process limit on the kind node; a large platform may fail to start pods", "error", err)
		return
	}
	setupLog.Info("lifted Podman's per-container process limit on the kind nodes", "nodes", lifted)
}

// podmanPidsOverride is a containers.conf fragment that creates containers
// with no process limit (0 = unlimited).
const podmanPidsOverride = "[containers]\npids_limit = 0\n"

// createWithoutPidsLimit points Podman at a containers.conf override for the
// duration of `kind create`, so the node container never gets the 2048 limit:
// its cgroup starts at pids.max=max and systemd inside it sizes its own
// DefaultTasksMax from that, not from 2048. Kind shells out to podman with
// this process's environment, which is how the override reaches it. An
// override the user set themselves is left alone (there is only one) and the
// post-create lift covers that case. The returned func restores the
// environment and removes the file.
func createWithoutPidsLimit(binary string) (restore func()) {
	if !isPodman(binary) || os.Getenv("CONTAINERS_CONF_OVERRIDE") != "" {
		return func() {}
	}
	f, err := os.CreateTemp("", "adhar-podman-*.conf")
	if err != nil {
		return func() {}
	}
	name := f.Name()
	_, werr := f.WriteString(podmanPidsOverride)
	cerr := f.Close()
	if werr != nil || cerr != nil || os.Setenv("CONTAINERS_CONF_OVERRIDE", name) != nil {
		_ = os.Remove(name)
		return func() {}
	}
	return func() {
		_ = os.Unsetenv("CONTAINERS_CONF_OVERRIDE")
		_ = os.Remove(name)
	}
}

func isPodman(binary string) bool {
	return strings.HasPrefix(filepath.Base(binary), "podman")
}

// liftPidsLimit removes the process limit from every node container of the
// named cluster and returns the nodes it updated.
func liftPidsLimit(ctx context.Context, run engineRunner, cluster string) ([]string, error) {
	out, err := run(ctx, "ps", "--filter", "label=io.x-k8s.kind.cluster="+cluster, "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	var lifted []string
	for _, node := range strings.Fields(out) {
		if _, err := run(ctx, "update", "--pids-limit=-1", node); err != nil {
			return lifted, err
		}
		// `update` exiting 0 is not proof: read the limit back.
		got, err := run(ctx, "inspect", "--format", "{{.HostConfig.PidsLimit}}", node)
		if err != nil {
			return lifted, err
		}
		if n, perr := strconv.ParseInt(strings.TrimSpace(got), 10, 64); perr == nil && n > 0 {
			return lifted, fmt.Errorf("node %s still has a process limit of %d after podman update", node, n)
		}
		lifted = append(lifted, node)
	}
	return lifted, nil
}
