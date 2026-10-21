package kind

import (
	"context"
	"path/filepath"
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
// 2026-10-21). The limit is lifted on the running node containers, which
// Podman applies to the live cgroup, so it needs no engine configuration.
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
	setupLog.V(1).Info("lifted Podman's per-container process limit on the kind nodes", "nodes", lifted)
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
		lifted = append(lifted, node)
	}
	return lifted, nil
}
